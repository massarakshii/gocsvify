package xlsx

// Тесты требуют Go 1.25+ и GOEXPERIMENT=jsonv2: пакет импортирует
// gocsvify/settings, который использует encoding/json/v2.
//
// В этом файле лежат хелперы, общие с xlsx_styler_test.go.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"gocsvify/settings"
)

// --- helpers ---

const (
	xmlDecl = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>`
	mainNS  = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
)

type zipEntry struct {
	name    string
	content string
}

// buildZip собирает zip-архив в памяти; порядок записей сохраняется.
func buildZip(t *testing.T, entries ...zipEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("failed to create zip entry %s: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.content)); err != nil {
			t.Fatalf("failed to write zip entry %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}
	return buf.Bytes()
}

// zipFiles возвращает записи архива, собранного в памяти, в порядке добавления.
func zipFiles(t *testing.T, entries ...zipEntry) []*zip.File {
	t.Helper()

	data := buildZip(t, entries...)
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("failed to read in-memory zip: %v", err)
	}
	return zr.File
}

func singleZipFile(t *testing.T, name, content string) *zip.File {
	t.Helper()
	return zipFiles(t, zipEntry{name, content})[0]
}

// writeXlsx записывает архив во временную папку и возвращает путь к файлу.
func writeXlsx(t *testing.T, entries ...zipEntry) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "book.xlsx")
	if err := os.WriteFile(path, buildZip(t, entries...), 0o644); err != nil {
		t.Fatalf("failed to write xlsx: %v", err)
	}
	return path
}

func sheetXML(dimension string, rows ...string) string {
	var sb strings.Builder
	sb.WriteString(xmlDecl + `<worksheet xmlns="` + mainNS + `">`)
	if dimension != "" {
		sb.WriteString(`<dimension ref="` + dimension + `"/>`)
	}
	sb.WriteString(`<sheetData>`)
	for _, r := range rows {
		sb.WriteString(r)
	}
	sb.WriteString(`</sheetData></worksheet>`)
	return sb.String()
}

func rowXML(cells ...string) string {
	return `<row>` + strings.Join(cells, "") + `</row>`
}

func sharedStringsXML(items ...string) string {
	var sb strings.Builder
	sb.WriteString(xmlDecl + `<sst xmlns="` + mainNS + `" count="` + strconv.Itoa(len(items)) +
		`" uniqueCount="` + strconv.Itoa(len(items)) + `">`)
	for _, it := range items {
		sb.WriteString(`<si><t>` + it + `</t></si>`)
	}
	sb.WriteString(`</sst>`)
	return sb.String()
}

const relNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

// workbookXML строит workbook.xml с двумя листами (rId1 -> sheet1, rId2 -> sheet2).
func workbookXML(workbookPr string) string {
	return workbookXMLSheets(workbookPr,
		`<sheet name="Sheet1" sheetId="1" r:id="rId1"/>`,
		`<sheet name="Sheet2" sheetId="2" r:id="rId2"/>`)
}

func workbookXMLSheets(workbookPr string, sheets ...string) string {
	return xmlDecl + `<workbook xmlns="` + mainNS + `" xmlns:r="` + relNS + `">` + workbookPr +
		`<sheets>` + strings.Join(sheets, "") + `</sheets></workbook>`
}

// workbookRelsXML строит xl/_rels/workbook.xml.rels: rIdN -> worksheets/sheetN.xml.
func workbookRelsXML(n int) string {
	var sb strings.Builder
	sb.WriteString(xmlDecl + `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">`)
	for i := 1; i <= n; i++ {
		sb.WriteString(`<Relationship Id="rId` + strconv.Itoa(i) + `" Type="` + relNS + `/worksheet" Target="worksheets/sheet` + strconv.Itoa(i) + `.xml"/>`)
	}
	sb.WriteString(`</Relationships>`)
	return sb.String()
}

func newTestConverter(delimiter string, padRows bool) *Converter {
	return NewConverter(context.Background(), "", &settings.Settings{Delimiter: delimiter, PadRows: padRows})
}

// sheetToCSV прогоняет XML листа через sheetToCsv и возвращает получившийся CSV
// (разделитель - запятая).
func sheetToCSV(t *testing.T, c *Converter, sheet string, shared []string, styler *XlsxStyler) (string, error) {
	t.Helper()

	file := singleZipFile(t, "xl/worksheets/sheet1.xml", sheet)

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	err := c.sheetToCsv(file, shared, w, styler)
	w.Flush()
	return buf.String(), err
}

func readFile(t *testing.T, path string) string {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read %s: %v", path, err)
	}
	return string(data)
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("failed to read dir %s: %v", dir, err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

// --- NewConverter ---

func TestNewConverter(t *testing.T) {
	t.Parallel()

	ctx := context.WithValue(context.Background(), struct{}{}, "marker")
	s := &settings.Settings{Delimiter: ";"}

	c := NewConverter(ctx, "/path/to/book.xlsx", s)

	if c.Ctx != ctx {
		t.Error("Ctx was not stored")
	}
	if c.FilePath != "/path/to/book.xlsx" {
		t.Errorf("FilePath = %q", c.FilePath)
	}
	if c.Settings != s {
		t.Error("Settings pointer was not stored")
	}
	if c.StartDate != "1899-12-30" {
		t.Errorf("StartDate = %q, want %q", c.StartDate, "1899-12-30")
	}
	if c.DigitKeys == nil || len(c.DigitKeys) != 0 {
		t.Errorf("DigitKeys must be an empty non-nil map, got %#v", c.DigitKeys)
	}
}

// --- prepareColumnKey / getDigitKey ---

func TestPrepareColumnKey(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{"A", "A"},
		{"A1", "A"},
		{"AB12", "AB"},
		{"XFD1048576", "XFD"},
		{"12", ""},
		{"", ""},
		{"A1B2", "AB"},
	}
	for _, tt := range tests {
		if got := prepareColumnKey(tt.in); got != tt.want {
			t.Errorf("prepareColumnKey(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestGetDigitKey(t *testing.T) {
	t.Parallel()

	tests := []struct {
		key  string
		want int
	}{
		{"A", 1},
		{"B", 2},
		{"Z", 26},
		{"AA", 27},
		{"AB", 28},
		{"AZ", 52},
		{"BA", 53},
		{"ZZ", 702},
		{"AAA", 703},
		{"XFD", 16384}, // последний столбец в Excel
		{"A1", 1},
		{"AB12", 28},
		{"XFD1048576", 16384},
		{"", 0},
		{"1", 0},
	}

	c := newTestConverter(",", false)
	for _, tt := range tests {
		if got := c.getDigitKey(tt.key); got != tt.want {
			t.Errorf("getDigitKey(%q) = %d, want %d", tt.key, got, tt.want)
		}
	}
}

func TestGetDigitKeyCachesByColumnLetters(t *testing.T) {
	t.Parallel()

	c := newTestConverter(",", false)
	for _, key := range []string{"A", "A1", "A2", "A100", "B7", "B8"} {
		c.getDigitKey(key)
	}

	want := map[string]int{"A": 1, "B": 2}
	if !reflect.DeepEqual(c.DigitKeys, want) {
		t.Errorf("DigitKeys = %v, want %v", c.DigitKeys, want)
	}

	// Повторный вызов берёт значение из кэша.
	c.DigitKeys["A"] = 999
	if got := c.getDigitKey("A5"); got != 999 {
		t.Errorf("getDigitKey did not use the cache: got %d", got)
	}
}

// --- formatDate ---

func TestFormatDate1900(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		serial float64
		want   string
	}{
		{"zero is midnight (time only)", 0, "00:00:00"},
		{"noon", 0.5, "12:00:00"},
		{"6 am", 0.25, "06:00:00"},
		{"6 pm", 0.75, "18:00:00"},
		{"one second", 1.0 / 86400, "00:00:01"},
		{"serial 1", 1, "1900-01-01"},
		{"serial 61 is 1900-03-01", 61, "1900-03-01"},
		{"unix epoch", 25569, "1970-01-01"},
		{"2000-01-01", 36526, "2000-01-01"},
		{"leap day 2020", 43890, "2020-02-29"},
		{"2023-01-01", 44927, "2023-01-01"},
		{"2023-03-15", 45000, "2023-03-15"},
		{"date with noon", 45000.5, "2023-03-15 12:00:00"},
		{"date with one second", 45000 + 1.0/86400, "2023-03-15 00:00:01"},
		{"date with 23:59:59", 45000 + 86399.0/86400.0, "2023-03-15 23:59:59"},
		{"floating point noise rounds to 08:00:00", 45000.3333333333, "2023-03-15 08:00:00"},
		{"almost midnight rolls over to the next day", 45000 + 86399.6/86400.0, "2023-03-16 00:00:00"},
	}

	c := newTestConverter(",", false)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := c.formatDate(tt.serial); got != tt.want {
				t.Errorf("formatDate(%v) = %q, want %q", tt.serial, got, tt.want)
			}
		})
	}
}

func TestFormatDate1904(t *testing.T) {
	t.Parallel()

	tests := []struct {
		serial float64
		want   string
	}{
		{0, "00:00:00"},
		{1, "1904-01-02"},
		{43538, "2023-03-15"}, // то же число, что 45000 в системе 1900
		{43538.5, "2023-03-15 12:00:00"},
	}

	c := newTestConverter(",", false)
	c.StartDate = "1904-01-01"
	for _, tt := range tests {
		if got := c.formatDate(tt.serial); got != tt.want {
			t.Errorf("formatDate(%v) = %q, want %q", tt.serial, got, tt.want)
		}
	}
}

// --- getStartDate ---

func TestGetStartDate(t *testing.T) {
	t.Parallel()

	const (
		def  = "1899-12-30"
		leap = "1904-01-01"
	)

	tests := []struct {
		name       string
		workbookPr string
		want       string
	}{
		{"no workbookPr", ``, def},
		{"workbookPr without date1904", `<workbookPr defaultThemeVersion="124226"/>`, def},
		{"date1904=1", `<workbookPr date1904="1"/>`, leap},
		{"date1904=true", `<workbookPr date1904="true"/>`, leap},
		{"date1904=TRUE", `<workbookPr date1904="TRUE"/>`, leap},
		{"date1904=True", `<workbookPr date1904="True"/>`, leap},
		{"date1904 with other attributes", `<workbookPr codeName="ThisWorkbook" date1904="1" filterPrivacy="1"/>`, leap},
		{"date1904=0", `<workbookPr date1904="0"/>`, def},
		{"date1904=false", `<workbookPr date1904="false"/>`, def},
		{"date1904 empty", `<workbookPr date1904=""/>`, def},
		{"date1904 unknown value", `<workbookPr date1904="yes"/>`, def},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", false)
			file := singleZipFile(t, "xl/workbook.xml", workbookXML(tt.workbookPr))

			got, err := c.getStartDate(file)
			if err != nil {
				t.Fatalf("getStartDate failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("getStartDate = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGetStartDateMalformedXML(t *testing.T) {
	t.Parallel()

	c := newTestConverter(",", false)
	file := singleZipFile(t, "xl/workbook.xml", `<workbook><sheets><sheet></sheets>`)

	got, err := c.getStartDate(file)
	if err == nil {
		t.Fatal("expected error for malformed XML")
	}
	if got != "1899-12-30" {
		t.Errorf("expected the default date together with the error, got %q", got)
	}
}

// --- parseSharedStrings ---

func TestParseSharedStrings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want []string
	}{
		{
			name: "plain strings keep order",
			body: `<si><t>a</t></si><si><t>b</t></si><si><t>c</t></si>`,
			want: []string{"a", "b", "c"},
		},
		{
			name: "rich text runs are concatenated",
			body: `<si><r><rPr><b/></rPr><t>Hel</t></r><r><t xml:space="preserve">lo </t></r><r><t>world</t></r></si>`,
			want: []string{"Hello world"},
		},
		{
			name: "whitespace is preserved",
			body: `<si><t xml:space="preserve">  padded  </t></si>`,
			want: []string{"  padded  "},
		},
		{
			name: "empty t",
			body: `<si><t/></si><si><t></t></si>`,
			want: []string{"", ""},
		},
		{
			name: "si without t still takes an index",
			body: `<si/><si><t>x</t></si>`,
			want: []string{"", "x"},
		},
		{
			name: "xml entities are decoded",
			body: `<si><t>a &amp; b &lt;tag&gt; &quot;q&quot;</t></si>`,
			want: []string{`a & b <tag> "q"`},
		},
		{
			name: "character reference for newline",
			body: `<si><t>line1&#10;line2</t></si>`,
			want: []string{"line1\nline2"},
		},
		{
			name: "cyrillic, CJK and emoji",
			body: `<si><t>Привет</t></si><si><t>文件</t></si><si><t>😀</t></si>`,
			want: []string{"Привет", "文件", "😀"},
		},
		{
			name: "delimiter and quotes are kept as is",
			body: `<si><t>a;b,c "d"</t></si>`,
			want: []string{`a;b,c "d"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", false)
			file := singleZipFile(t, "xl/sharedStrings.xml",
				xmlDecl+`<sst xmlns="`+mainNS+`">`+tt.body+`</sst>`)

			got, err := c.parseSharedStrings(file)
			if err != nil {
				t.Fatalf("parseSharedStrings failed: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseSharedStringsEmpty(t *testing.T) {
	t.Parallel()

	c := newTestConverter(",", false)
	file := singleZipFile(t, "xl/sharedStrings.xml", xmlDecl+`<sst xmlns="`+mainNS+`" count="0" uniqueCount="0"/>`)

	got, err := c.parseSharedStrings(file)
	if err != nil {
		t.Fatalf("parseSharedStrings failed: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected no strings, got %q", got)
	}
}

func TestParseSharedStringsMalformedXML(t *testing.T) {
	t.Parallel()

	c := newTestConverter(",", false)
	file := singleZipFile(t, "xl/sharedStrings.xml", `<sst><si><t>unclosed</si></sst>`)

	got, err := c.parseSharedStrings(file)
	if err == nil {
		t.Fatalf("expected error, got %q", got)
	}
	if got != nil {
		t.Errorf("expected nil result on error, got %q", got)
	}
}

func TestParseSharedStringsMany(t *testing.T) {
	t.Parallel()

	const n = 2000
	items := make([]string, n)
	for i := range items {
		items[i] = "item " + strconv.Itoa(i)
	}

	c := newTestConverter(",", false)
	file := singleZipFile(t, "xl/sharedStrings.xml", sharedStringsXML(items...))

	got, err := c.parseSharedStrings(file)
	if err != nil {
		t.Fatalf("parseSharedStrings failed: %v", err)
	}
	if !reflect.DeepEqual(got, items) {
		t.Errorf("shared strings mismatch: got %d items, want %d", len(got), n)
	}
}

// --- sheetToCsv ---

var testShared = []string{
	"Имя",            // 0
	"Возраст",        // 1
	"Анна",           // 2
	"a,b",            // 3
	`say "hi"`,       // 4
	"line1\nline2",   // 5
	"semi;colon",     // 6
	" leading space", // 7
}

// dateStyler: styleId (индекс в cellXfs) 0 - General, 1 - пользовательская дата,
// 2 - число, 3 - встроенный формат даты 14.
var dateStyler = &XlsxStyler{
	DateStyles: map[int]string{14: "m/d/yyyy"},
	Nfts: map[int]string{
		164: "yyyy-mm-dd",
		165: "0.00",
	},
	Styles: CellXfs{Xf: []CellXf{
		{NumFmtId: 0},
		{NumFmtId: 164},
		{NumFmtId: 165},
		{NumFmtId: 14},
	}},
}

func TestSheetToCsv(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		padRows bool
		sheet   string
		want    string
	}{
		{
			name: "shared strings and numbers",
			sheet: sheetXML("A1:B2",
				rowXML(`<c r="A1" t="s"><v>0</v></c>`, `<c r="B1" t="s"><v>1</v></c>`),
				rowXML(`<c r="A2" t="s"><v>2</v></c>`, `<c r="B2"><v>30</v></c>`)),
			want: "Имя,Возраст\nАнна,30\n",
		},
		{
			name:  "gap between cells is filled",
			sheet: sheetXML("A1:C1", rowXML(`<c r="A1"><v>1</v></c>`, `<c r="C1"><v>3</v></c>`)),
			want:  "1,,3\n",
		},
		{
			name:  "leading gap is filled",
			sheet: sheetXML("A1:C1", rowXML(`<c r="C1"><v>3</v></c>`)),
			want:  ",,3\n",
		},
		{
			name:  "dimension starting at B skips column A",
			sheet: sheetXML("B1:C1", rowXML(`<c r="B1"><v>1</v></c>`, `<c r="C1"><v>2</v></c>`)),
			want:  "1,2\n",
		},
		{
			name:  "dimension starting at B, gap inside",
			sheet: sheetXML("B1:D1", rowXML(`<c r="B1"><v>1</v></c>`, `<c r="D1"><v>3</v></c>`)),
			want:  "1,,3\n",
		},
		{
			name:  "single-cell dimension",
			sheet: sheetXML("B2", rowXML(`<c r="B2"><v>7</v></c>`)),
			want:  "7\n",
		},
		{
			name:  "trailing empty columns are not added without PadRows",
			sheet: sheetXML("A1:D1", rowXML(`<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`)),
			want:  "1,2\n",
		},
		{
			name:    "PadRows pads the row up to the last column",
			padRows: true,
			sheet:   sheetXML("A1:D1", rowXML(`<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`)),
			want:    "1,2,,\n",
		},
		{
			name:    "PadRows does not touch a full row",
			padRows: true,
			sheet:   sheetXML("A1:B1", rowXML(`<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`)),
			want:    "1,2\n",
		},
		{
			name:    "PadRows with dimension starting at B",
			padRows: true,
			sheet:   sheetXML("B1:D1", rowXML(`<c r="B1"><v>1</v></c>`)),
			want:    "1,,\n",
		},
		{
			name:  "cells without r are placed one after another",
			sheet: sheetXML("A1:B1", rowXML(`<c><v>1</v></c>`, `<c><v>2</v></c>`)),
			want:  "1,2\n",
		},
		{
			name:  "self-closing empty cell keeps its column",
			sheet: sheetXML("A1:B1", rowXML(`<c r="A1"/>`, `<c r="B1"><v>2</v></c>`)),
			want:  ",2\n",
		},
		{
			name: "inline strings: plain, rich and mixed",
			sheet: sheetXML("A1:C1", rowXML(
				`<c r="A1" t="inlineStr"><is><t>plain</t></is></c>`,
				`<c r="B1" t="inlineStr"><is><r><t>he</t></r><r><t>llo</t></r></is></c>`,
				`<c r="C1" t="inlineStr"><is><t>x</t><r><t>yz</t></r></is></c>`)),
			want: "plain,hello,xyz\n",
		},
		{
			name: "empty inline string",
			sheet: sheetXML("A1:B1", rowXML(
				`<c r="A1" t="inlineStr"><is><t></t></is></c>`,
				`<c r="B1"><v>1</v></c>`)),
			want: ",1\n",
		},
		{
			name: "shared string index out of range or invalid gives empty value",
			sheet: sheetXML("A1:D1", rowXML(
				`<c r="A1" t="s"><v>99</v></c>`,
				`<c r="B1" t="s"><v>abc</v></c>`,
				`<c r="C1" t="s"><v></v></c>`,
				`<c r="D1" t="s"><v>0</v></c>`)),
			want: ",,,Имя\n",
		},
		{
			name: "other cell types keep the raw value",
			sheet: sheetXML("A1:E1", rowXML(
				`<c r="A1" t="str"><v>hello</v></c>`,
				`<c r="B1" t="b"><v>1</v></c>`,
				`<c r="C1" t="e"><v>#DIV/0!</v></c>`,
				`<c r="D1" t="n"><v>3.14</v></c>`,
				`<c r="E1" t="d"><v>2023-01-01</v></c>`)),
			want: "hello,1,#DIV/0!,3.14,2023-01-01\n",
		},
		{
			name: "csv quoting of delimiter, quotes and newlines",
			sheet: sheetXML("A1:D1", rowXML(
				`<c r="A1" t="s"><v>3</v></c>`,
				`<c r="B1" t="s"><v>4</v></c>`,
				`<c r="C1" t="s"><v>5</v></c>`,
				`<c r="D1" t="s"><v>7</v></c>`)),
			want: "\"a,b\",\"say \"\"hi\"\"\",\"line1\nline2\",\" leading space\"\n",
		},
		{
			name:  "other delimiter is not special for the comma writer",
			sheet: sheetXML("A1:A1", rowXML(`<c r="A1" t="s"><v>6</v></c>`)),
			want:  "semi;colon\n",
		},
		{
			name:  "two-letter columns in dimension",
			sheet: sheetXML("Z1:AB1", rowXML(`<c r="Z1"><v>1</v></c>`, `<c r="AB1"><v>3</v></c>`)),
			want:  "1,,3\n",
		},
		{
			name:  "far column fills 26 empty cells before it",
			sheet: sheetXML("A1:AA1", rowXML(`<c r="AA1"><v>9</v></c>`)),
			want:  strings.Repeat(",", 26) + "9\n",
		},
		{
			name: "rows are written one after another",
			sheet: sheetXML("A1:A3",
				`<row r="1"><c r="A1"><v>1</v></c></row>`,
				`<row r="2"><c r="A2"><v>2</v></c></row>`,
				`<row r="3"><c r="A3"><v>3</v></c></row>`),
			want: "1\n2\n3\n",
		},
		{
			// Пропущенные в файле номера строк не восстанавливаются пустыми строками.
			name: "missing row numbers are not restored",
			sheet: sheetXML("A1:A5",
				`<row r="1"><c r="A1"><v>1</v></c></row>`,
				`<row r="5"><c r="A5"><v>5</v></c></row>`),
			want: "1\n5\n",
		},
		{
			name: "empty row gives an empty line",
			sheet: sheetXML("A1:A3",
				`<row r="1"><c r="A1"><v>1</v></c></row>`,
				`<row r="2"/>`,
				`<row r="3"><c r="A3"><v>3</v></c></row>`),
			want: "1\n\n3\n",
		},
		{
			name:  "sheet without rows gives empty output",
			sheet: sheetXML("A1"),
			want:  "",
		},
		{
			name:  "sheet without dimension and without PadRows",
			sheet: sheetXML("", rowXML(`<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`)),
			want:  "1,2\n",
		},
		{
			name:  "xml entities in inline strings",
			sheet: sheetXML("A1", rowXML(`<c r="A1" t="inlineStr"><is><t>a &amp; b &lt; c</t></is></c>`)),
			want:  "a & b < c\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", tt.padRows)

			got, err := sheetToCSV(t, c, tt.sheet, testShared, nil)
			if err != nil {
				t.Fatalf("sheetToCsv failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// Без <dimension> правая граница по умолчанию - столбец Z: при PadRows
// каждая строка дополняется до 26 столбцов.
func TestSheetToCsvNoDimensionPadsTo26Columns(t *testing.T) {
	t.Parallel()

	c := newTestConverter(",", true)
	got, err := sheetToCSV(t, c, sheetXML("", rowXML(`<c r="A1"><v>1</v></c>`)), nil, nil)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}
	if want := "1" + strings.Repeat(",", 25) + "\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestSheetToCsvDates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		cell      string
		styler    *XlsxStyler
		startDate string
		want      string
	}{
		{"custom date style, date only", `<c r="A1" s="1"><v>45000</v></c>`, dateStyler, "", "2023-03-15\n"},
		{"custom date style, date and time", `<c r="A1" s="1"><v>45000.5</v></c>`, dateStyler, "", "2023-03-15 12:00:00\n"},
		{"custom date style, time only", `<c r="A1" s="1"><v>0.5</v></c>`, dateStyler, "", "12:00:00\n"},
		{"built-in date format id", `<c r="A1" s="3"><v>45000</v></c>`, dateStyler, "", "2023-03-15\n"},
		{"number style keeps the raw value", `<c r="A1" s="2"><v>45000</v></c>`, dateStyler, "", "45000\n"},
		{"style 0 keeps the raw value", `<c r="A1" s="0"><v>45000</v></c>`, dateStyler, "", "45000\n"},
		{"no style attribute keeps the raw value", `<c r="A1"><v>45000</v></c>`, dateStyler, "", "45000\n"},
		{"style out of range keeps the raw value", `<c r="A1" s="99"><v>45000</v></c>`, dateStyler, "", "45000\n"},
		{"negative style keeps the raw value", `<c r="A1" s="-1"><v>45000</v></c>`, dateStyler, "", "45000\n"},
		{"non-numeric style keeps the raw value", `<c r="A1" s="abc"><v>45000</v></c>`, dateStyler, "", "45000\n"},
		{"nil styler keeps the raw value", `<c r="A1" s="1"><v>45000</v></c>`, nil, "", "45000\n"},
		{"1904 date system", `<c r="A1" s="1"><v>43538</v></c>`, dateStyler, "1904-01-01", "2023-03-15\n"},
		{"1904 date system with time", `<c r="A1" s="1"><v>43538.5</v></c>`, dateStyler, "1904-01-01", "2023-03-15 12:00:00\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", false)
			if tt.startDate != "" {
				c.StartDate = tt.startDate
			}

			got, err := sheetToCSV(t, c, sheetXML("A1", rowXML(tt.cell)), nil, tt.styler)
			if err != nil {
				t.Fatalf("sheetToCsv failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// Строковые ячейки (shared и inline) с датой в стиле не преобразуются в дату.
func TestSheetToCsvStringCellsIgnoreDateStyle(t *testing.T) {
	t.Parallel()

	c := newTestConverter(",", false)
	sheet := sheetXML("A1:B1", rowXML(
		`<c r="A1" s="1" t="s"><v>2</v></c>`,
		`<c r="B1" s="1" t="inlineStr"><is><t>text</t></is></c>`))

	got, err := sheetToCSV(t, c, sheet, testShared, dateStyler)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}
	if want := "Анна,text\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Ячейка с датой в стиле, но без числового значения, не превращается в дату:
// пустое значение остаётся пустым, нечисловое сохраняется как есть.
func TestSheetToCsvDateStyledCellWithoutNumericValue(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		cells []string
		want  string
	}{
		{"self-closing cell", []string{`<c r="A1" s="1"/>`, `<c r="B1"><v>2</v></c>`}, ",2\n"},
		{"empty v", []string{`<c r="A1" s="1"><v></v></c>`, `<c r="B1"><v>2</v></c>`}, ",2\n"},
		{"non-numeric v", []string{`<c r="A1" s="1"><v>abc</v></c>`, `<c r="B1"><v>2</v></c>`}, "abc,2\n"},
		{"text that looks like a date", []string{`<c r="A1" s="1"><v>2023-03-15</v></c>`, `<c r="B1"><v>2</v></c>`}, "2023-03-15,2\n"},
		{"zero is still a date", []string{`<c r="A1" s="1"><v>0</v></c>`, `<c r="B1"><v>2</v></c>`}, "00:00:00,2\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", false)
			got, err := sheetToCSV(t, c, sheetXML("A1:B1", rowXML(tt.cells...)), nil, dateStyler)
			if err != nil {
				t.Fatalf("sheetToCsv failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestSheetToCsvContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := newTestConverter(",", false)
	c.Ctx = ctx

	got, err := sheetToCSV(t, c, sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`)), nil, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if got != "" {
		t.Errorf("expected no output after cancellation, got %q", got)
	}
}

func TestSheetToCsvMalformedXML(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		sheet string
	}{
		{"unclosed elements", `<worksheet><sheetData><row><c r="A1"><v>1</v></c>`},
		{"mismatched tags", `<worksheet><sheetData><row></sheetData></worksheet>`},
		{"broken cell", `<worksheet><sheetData><row><c r="A1"><v>1</c></row></sheetData></worksheet>`},
		{"not XML", `not xml at all <<<`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", false)
			if _, err := sheetToCSV(t, c, tt.sheet, nil, nil); err == nil {
				t.Fatal("expected error for malformed XML")
			}
		})
	}
}

func TestSheetToCsvManyRows(t *testing.T) {
	t.Parallel()

	const rows = 5000
	var sb strings.Builder
	for i := 1; i <= rows; i++ {
		n := strconv.Itoa(i)
		sb.WriteString(rowXML(
			`<c r="A`+n+`"><v>`+n+`</v></c>`,
			`<c r="B`+n+`" t="s"><v>0</v></c>`,
			`<c r="C`+n+`"><v>`+strconv.Itoa(i*2)+`</v></c>`))
	}

	c := newTestConverter(",", false)
	got, err := sheetToCSV(t, c, sheetXML("A1:C"+strconv.Itoa(rows), sb.String()), testShared, nil)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != rows {
		t.Fatalf("expected %d lines, got %d", rows, len(lines))
	}
	if lines[0] != "1,Имя,2" {
		t.Errorf("first line = %q", lines[0])
	}
	if want := "5000,Имя,10000"; lines[rows-1] != want {
		t.Errorf("last line = %q, want %q", lines[rows-1], want)
	}
}

// --- Convert ---

const convertStyles = stylesHeader +
	`<numFmts count="2">` +
	`<numFmt numFmtId="164" formatCode="yyyy\-mm\-dd"/>` +
	`<numFmt numFmtId="165" formatCode="0.00"/>` +
	`</numFmts>` +
	// s=0 - General, s=1 - дата, s=2 - число
	`<cellXfs count="3"><xf numFmtId="0"/><xf numFmtId="164"/><xf numFmtId="165"/></cellXfs>` +
	`</styleSheet>`

func convertSheet1() string {
	return sheetXML("A1:C2",
		rowXML(
			`<c r="A1" t="s"><v>0</v></c>`,
			`<c r="B1" t="s"><v>1</v></c>`,
			`<c r="C1" t="s"><v>2</v></c>`),
		rowXML(
			`<c r="A2" t="s"><v>3</v></c>`,
			`<c r="B2" s="2"><v>30.5</v></c>`,
			`<c r="C2" s="1"><v>45000</v></c>`))
}

func newConvertEntries() []zipEntry {
	return []zipEntry{
		{"[Content_Types].xml", xmlDecl + `<Types/>`},
		{"xl/workbook.xml", workbookXML(`<workbookPr/>`)},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML(2)},
		{"xl/styles.xml", convertStyles},
		{"xl/sharedStrings.xml", sharedStringsXML("Имя", "Сумма", "Дата", "Анна")},
		{"xl/worksheets/sheet1.xml", convertSheet1()},
	}
}

func runConvert(t *testing.T, path, delimiter string) (outDir string, err error) {
	t.Helper()

	outDir = t.TempDir()
	c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: delimiter})
	return outDir, c.Convert(outDir)
}

func TestConvertHappyPath(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, newConvertEntries()...)

	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}

	if got, want := listDir(t, outDir), []string{"0.csv"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output files = %v, want %v", got, want)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "Имя;Сумма;Дата\nАнна;30.5;2023-03-15\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestConvertMultipleSheets(t *testing.T) {
	t.Parallel()

	entries := newConvertEntries()
	entries = append(entries, zipEntry{"xl/worksheets/sheet2.xml",
		sheetXML("A1:B1", rowXML(`<c r="A1"><v>7</v></c>`, `<c r="B1" t="s"><v>3</v></c>`))})

	outDir, err := runConvert(t, writeXlsx(t, entries...), ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}

	if got, want := listDir(t, outDir), []string{"0.csv", "1.csv"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("output files = %v, want %v", got, want)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "Имя;Сумма;Дата\nАнна;30.5;2023-03-15\n"; got != want {
		t.Errorf("0.csv mismatch:\n got: %q\nwant: %q", got, want)
	}
	if got, want := readFile(t, filepath.Join(outDir, "1.csv")), "7;Анна\n"; got != want {
		t.Errorf("1.csv mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Без workbook.xml листы нумеруются по числу в имени файла (sheet2 < sheet10),
// а не по порядку в архиве и не лексикографически.
func TestConvertWithoutWorkbookSortsSheetsNumerically(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t,
		zipEntry{"xl/worksheets/sheet10.xml", sheetXML("A1", rowXML(`<c r="A1"><v>10</v></c>`))},
		zipEntry{"xl/worksheets/sheet2.xml", sheetXML("A1", rowXML(`<c r="A1"><v>2</v></c>`))},
		zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))},
	)

	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	for i, want := range []string{"1\n", "2\n", "10\n"} {
		if got := readFile(t, filepath.Join(outDir, strconv.Itoa(i)+".csv")); got != want {
			t.Errorf("%d.csv = %q, want %q", i, got, want)
		}
	}
}

// Порядок листов задаёт <sheet> в workbook.xml, а не имена файлов.
func TestConvertFollowsWorkbookSheetOrder(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t,
		zipEntry{"xl/workbook.xml", workbookXMLSheets(``,
			`<sheet name="B" sheetId="2" r:id="rId2"/>`,
			`<sheet name="A" sheetId="1" r:id="rId1"/>`)},
		zipEntry{"xl/_rels/workbook.xml.rels", workbookRelsXML(2)},
		zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))},
		zipEntry{"xl/worksheets/sheet2.xml", sheetXML("A1", rowXML(`<c r="A1"><v>2</v></c>`))},
	)

	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got := readFile(t, filepath.Join(outDir, "0.csv")); got != "2\n" {
		t.Errorf("0.csv = %q, want sheet2 first", got)
	}
	if got := readFile(t, filepath.Join(outDir, "1.csv")); got != "1\n" {
		t.Errorf("1.csv = %q, want sheet1 second", got)
	}
}

// Скрытые листы пропускаются, пока не включён WithHidden.
func TestConvertHiddenSheets(t *testing.T) {
	t.Parallel()

	entries := []zipEntry{
		{"xl/workbook.xml", workbookXMLSheets(``,
			`<sheet name="A" sheetId="1" r:id="rId1"/>`,
			`<sheet name="H" sheetId="2" state="hidden" r:id="rId2"/>`)},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML(2)},
		{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))},
		{"xl/worksheets/sheet2.xml", sheetXML("A1", rowXML(`<c r="A1"><v>2</v></c>`))},
	}
	path := writeXlsx(t, entries...)

	for _, tc := range []struct {
		withHidden bool
		want       []string
	}{{false, []string{"0.csv"}}, {true, []string{"0.csv", "1.csv"}}} {
		outDir := t.TempDir()
		c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: ";", WithHidden: tc.withHidden})
		if err := c.Convert(outDir); err != nil {
			t.Fatalf("Convert(withHidden=%v) failed: %v", tc.withHidden, err)
		}
		if got := listDir(t, outDir); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("withHidden=%v: files = %v, want %v", tc.withHidden, got, tc.want)
		}
	}
}

func TestConvertIgnoresNonSheetEntries(t *testing.T) {
	t.Parallel()

	entries := newConvertEntries()
	entries = append(entries,
		zipEntry{"xl/worksheets/_rels/sheet1.xml.rels", xmlDecl + `<Relationships/>`},
		zipEntry{"xl/worksheets/notes.xml", `<not-a-sheet/>`},
		zipEntry{"xl/worksheets/sheet1.xml.bak", `garbage`},
		zipEntry{"xl/charts/sheet9.xml", `<chart/>`},
		zipEntry{"docProps/app.xml", `<Properties/>`},
	)

	outDir, err := runConvert(t, writeXlsx(t, entries...), ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got, want := listDir(t, outDir), []string{"0.csv"}; !reflect.DeepEqual(got, want) {
		t.Errorf("output files = %v, want %v", got, want)
	}
}

func TestConvertWithoutSheets(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/workbook.xml", workbookXML(``)})

	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if names := listDir(t, outDir); len(names) != 0 {
		t.Errorf("expected no output files, got %v", names)
	}
}

// Без sharedStrings.xml, styles.xml и workbook.xml работают inline-строки и числа.
func TestConvertWithoutOptionalParts(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1:C1", rowXML(
		`<c r="A1" t="inlineStr"><is><t>text</t></is></c>`,
		`<c r="B1" s="1"><v>45000</v></c>`,
		`<c r="C1" t="s"><v>0</v></c>`))})

	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	// Без styles.xml даты не распознаются, без sharedStrings индекс 0 даёт пустую строку.
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "text;45000;\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Порядок файлов в архиве не важен: стили и словарь строк читаются до листов.
func TestConvertSheetBeforeSharedPartsInArchive(t *testing.T) {
	t.Parallel()

	entries := []zipEntry{
		{"xl/worksheets/sheet1.xml", convertSheet1()},
		{"xl/sharedStrings.xml", sharedStringsXML("Имя", "Сумма", "Дата", "Анна")},
		{"xl/styles.xml", convertStyles},
		{"xl/workbook.xml", workbookXML(`<workbookPr/>`)},
		{"xl/_rels/workbook.xml.rels", workbookRelsXML(2)},
	}

	outDir, err := runConvert(t, writeXlsx(t, entries...), ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "Имя;Сумма;Дата\nАнна;30.5;2023-03-15\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestConvert1904DateSystem(t *testing.T) {
	t.Parallel()

	entries := newConvertEntries()
	entries[1] = zipEntry{"xl/workbook.xml", workbookXML(`<workbookPr date1904="1"/>`)}
	// То же число 43538 в системе 1904 - это 2023-03-15.
	entries[5] = zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1" s="1"><v>43538</v></c>`))}

	path := writeXlsx(t, entries...)
	outDir := t.TempDir()
	c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: ";"})
	if err := c.Convert(outDir); err != nil {
		t.Fatalf("Convert failed: %v", err)
	}

	if c.StartDate != "1904-01-01" {
		t.Errorf("StartDate = %q, want 1904-01-01", c.StartDate)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "2023-03-15\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestConvertDelimiters(t *testing.T) {
	t.Parallel()

	sheet := sheetXML("A1:C1", rowXML(
		`<c r="A1"><v>1</v></c>`,
		`<c r="B1" t="inlineStr"><is><t>two</t></is></c>`,
		`<c r="C1"><v>3</v></c>`))

	tests := []struct {
		name      string
		delimiter string
		want      string
	}{
		{"semicolon", ";", "1;two;3\n"},
		{"comma", ",", "1,two,3\n"},
		{"tab", "\t", "1\ttwo\t3\n"},
		{"pipe", "|", "1|two|3\n"},
		{"multi-byte rune", "§", "1§two§3\n"},
		{"only the first rune of a long delimiter is used", ";;", "1;two;3\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheet})
			outDir, err := runConvert(t, path, tt.delimiter)
			if err != nil {
				t.Fatalf("Convert failed: %v", err)
			}
			if got := readFile(t, filepath.Join(outDir, "0.csv")); got != tt.want {
				t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestConvertQuotesFieldsContainingDelimiter(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1:B1", rowXML(
		`<c r="A1" t="inlineStr"><is><t>a;b</t></is></c>`,
		`<c r="B1" t="inlineStr"><is><t>plain</t></is></c>`))})

	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "\"a;b\";plain\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestConvertPadRows(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1:C2",
		rowXML(`<c r="A1"><v>1</v></c>`),
		rowXML(`<c r="A2"><v>1</v></c>`, `<c r="B2"><v>2</v></c>`, `<c r="C2"><v>3</v></c>`))})

	outDir := t.TempDir()
	c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: ";", PadRows: true})
	if err := c.Convert(outDir); err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "1;;\n1;2;3\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestConvertOverwritesExistingCsv(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))})
	outDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(outDir, "0.csv"), []byte("old content that is much longer\n"), 0o644); err != nil {
		t.Fatalf("failed to prepare old csv: %v", err)
	}

	c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: ";"})
	if err := c.Convert(outDir); err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got := readFile(t, filepath.Join(outDir, "0.csv")); got != "1\n" {
		t.Errorf("0.csv = %q, old content must be replaced", got)
	}
}

func TestConvertTwiceGivesSameResult(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, newConvertEntries()...)
	c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: ";"})

	var results []string
	for i := 0; i < 2; i++ {
		outDir := t.TempDir()
		if err := c.Convert(outDir); err != nil {
			t.Fatalf("Convert #%d failed: %v", i+1, err)
		}
		results = append(results, readFile(t, filepath.Join(outDir, "0.csv")))
	}
	if results[0] != results[1] {
		t.Errorf("results differ between runs:\n 1: %q\n 2: %q", results[0], results[1])
	}
}

// --- Convert: ошибки ---

func TestConvertSourceFileErrors(t *testing.T) {
	t.Parallel()

	notZip := filepath.Join(t.TempDir(), "not_a_zip.xlsx")
	if err := os.WriteFile(notZip, []byte("this is definitely not a zip archive"), 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}
	empty := filepath.Join(t.TempDir(), "empty.xlsx")
	if err := os.WriteFile(empty, nil, 0o644); err != nil {
		t.Fatalf("failed to write file: %v", err)
	}

	tests := []struct {
		name string
		path string
	}{
		{"missing file", filepath.Join(t.TempDir(), "missing.xlsx")},
		{"not a zip archive", notZip},
		{"empty file", empty},
		{"directory", t.TempDir()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outDir, err := runConvert(t, tt.path, ";")
			if err == nil {
				t.Fatal("expected error")
			}
			if names := listDir(t, outDir); len(names) != 0 {
				t.Errorf("no files must be created on error, got %v", names)
			}
		})
	}
}

func TestConvertMissingFileIsNotExistError(t *testing.T) {
	t.Parallel()

	_, err := runConvert(t, filepath.Join(t.TempDir(), "missing.xlsx"), ";")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist, got %v", err)
	}
}

func TestConvertMalformedParts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		replace    string // имя записи, содержимое которой портим
		content    string
		wantSubstr string
	}{
		{"malformed sharedStrings", "xl/sharedStrings.xml", `<sst><si><t>x</si></sst>`, "failed to parse shared strings"},
		{"malformed styles", "xl/styles.xml", stylesHeader + `<numFmts><numFmt numFmtId="164" formatCode="x"/>`, "failed to create styles"},
		{"styles with non-numeric numFmtId", "xl/styles.xml", stylesHeader + `<numFmts><numFmt numFmtId="x"/></numFmts></styleSheet>`, "failed to create styles"},
		{"malformed sheet", "xl/worksheets/sheet1.xml", `<worksheet><sheetData><row><c r="A1"><v>1</v></c>`, "failed to create CSV"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entries := newConvertEntries()
			for i := range entries {
				if entries[i].name == tt.replace {
					entries[i].content = tt.content
				}
			}

			outDir, err := runConvert(t, writeXlsx(t, entries...), ";")
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantSubstr) {
				t.Errorf("error %q does not contain %q", err, tt.wantSubstr)
			}
			// Недописанный CSV не остаётся на диске.
			if names := listDir(t, outDir); len(names) != 0 {
				t.Errorf("no files must remain on error, got %v", names)
			}
		})
	}
}

// Если ошибка случилась на втором листе, первый CSV остаётся на диске,
// а недописанный второй удаляется.
func TestConvertKeepsEarlierSheetsOnLaterFailure(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t,
		zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))},
		zipEntry{"xl/worksheets/sheet2.xml", `<worksheet><sheetData><row>`},
	)

	outDir, err := runConvert(t, path, ";")
	if err == nil {
		t.Fatal("expected error")
	}
	if got, want := listDir(t, outDir), []string{"0.csv"}; !reflect.DeepEqual(got, want) {
		t.Errorf("output files = %v, want %v", got, want)
	}
	if got := readFile(t, filepath.Join(outDir, "0.csv")); got != "1\n" {
		t.Errorf("0.csv = %q, want %q", got, "1\n")
	}
}

func TestConvertOutputDirectoryMissing(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))})
	missing := filepath.Join(t.TempDir(), "no", "such", "dir")

	c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: ";"})
	err := c.Convert(missing)
	if err == nil {
		t.Fatal("expected error when the output directory does not exist")
	}
	if !strings.Contains(err.Error(), "failed to create CSV") {
		t.Errorf("unexpected error text: %v", err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected the error to wrap fs.ErrNotExist, got %v", err)
	}
	if _, statErr := os.Stat(missing); !errors.Is(statErr, fs.ErrNotExist) {
		t.Errorf("output directory must not be created, Stat error: %v", statErr)
	}
}

// Ошибка отмены контекста возвращается (с сохранением через %w), недописанный CSV удаляется.
func TestConvertContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	path := writeXlsx(t, newConvertEntries()...)
	outDir := t.TempDir()
	c := NewConverter(ctx, path, &settings.Settings{Delimiter: ";"})

	err := c.Convert(outDir)
	if err == nil {
		t.Fatal("expected error for a canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected the error to wrap context.Canceled, got %v", err)
	}
	if !strings.Contains(err.Error(), "failed to create CSV") {
		t.Errorf("unexpected error text: %v", err)
	}
	if names := listDir(t, outDir); len(names) != 0 {
		t.Errorf("no files must remain after cancellation, got %v", names)
	}
}

// Символ, недопустимый как разделитель csv.Writer, даёт ошибку при записи
// первой же строки, файл при этом удаляется.
func TestConvertInvalidDelimiter(t *testing.T) {
	t.Parallel()

	for _, delimiter := range []string{"\n", "\r", `"`} {
		t.Run(strconv.Quote(delimiter), func(t *testing.T) {
			t.Parallel()

			path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1:B1", rowXML(
				`<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`))})

			outDir, err := runConvert(t, path, delimiter)
			if err == nil {
				t.Fatal("expected error for an invalid csv delimiter")
			}
			if !strings.Contains(err.Error(), "failed to create CSV") {
				t.Errorf("unexpected error text: %v", err)
			}
			if names := listDir(t, outDir); len(names) != 0 {
				t.Errorf("no files must remain on error, got %v", names)
			}
		})
	}
}

// Пустой Delimiter в настройках заменяется на ";" (defaultDelimiter).
func TestConvertEmptyDelimiterFallsBackToSemicolon(t *testing.T) {
	t.Parallel()

	if defaultDelimiter != ";" {
		t.Fatalf("defaultDelimiter = %q, want %q", defaultDelimiter, ";")
	}

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1:B1", rowXML(
		`<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`))})

	outDir, err := runConvert(t, path, "")
	if err != nil {
		t.Fatalf("Convert failed for an empty delimiter: %v", err)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "1;2\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Заданный в настройках разделитель не заменяется значением по умолчанию.
func TestConvertExplicitDelimiterIsNotOverridden(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1:B1", rowXML(
		`<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`))})

	outDir, err := runConvert(t, path, ",")
	if err != nil {
		t.Fatalf("Convert failed: %v", err)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "1,2\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Ошибка разбора xl/workbook.xml возвращается из Convert, CSV не создаются.
func TestConvertWorkbookParseError(t *testing.T) {
	t.Parallel()

	entries := newConvertEntries()
	entries[1] = zipEntry{"xl/workbook.xml", `<workbook><sheets><sheet></sheets>`}

	outDir, err := runConvert(t, writeXlsx(t, entries...), ";")
	if err == nil {
		t.Fatal("expected error for a malformed workbook.xml")
	}
	if !strings.Contains(err.Error(), "failed to get the date system for formatting") {
		t.Errorf("unexpected error text: %v", err)
	}
	var syntaxErr *xml.SyntaxError
	if !errors.As(err, &syntaxErr) {
		t.Errorf("expected the error to wrap *xml.SyntaxError, got %v", err)
	}
	if names := listDir(t, outDir); len(names) != 0 {
		t.Errorf("no files must be created on error, got %v", names)
	}
}

// Ошибки отдельных частей файла сохраняют исходную причину (%w).
func TestConvertWrapsUnderlyingErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		replace string
		content string
	}{
		{"workbook", "xl/workbook.xml", `<workbook><sheets></workbook>`},
		{"sharedStrings", "xl/sharedStrings.xml", `<sst><si><t>x</si></sst>`},
		{"styles", "xl/styles.xml", `<styleSheet><numFmts></styleSheet>`},
		{"sheet", "xl/worksheets/sheet1.xml", `<worksheet><sheetData><row></sheetData></worksheet>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			entries := newConvertEntries()
			for i := range entries {
				if entries[i].name == tt.replace {
					entries[i].content = tt.content
				}
			}

			_, err := runConvert(t, writeXlsx(t, entries...), ";")
			if err == nil {
				t.Fatal("expected error")
			}
			var syntaxErr *xml.SyntaxError
			if !errors.As(err, &syntaxErr) {
				t.Errorf("expected the error to wrap *xml.SyntaxError, got %v", err)
			}
		})
	}
}

// Фонетическая подсказка <rPh> (японский furigana) не должна склеиваться с основным текстом.
func TestParseSharedStringsSkipsPhoneticRuns(t *testing.T) {
	t.Parallel()

	xmlData := xmlDecl + `<sst xmlns="` + mainNS + `" count="3" uniqueCount="3">` +
		`<si><t>漢字</t><rPh sb="0" eb="2"><t>カンジ</t></rPh><phoneticPr fontId="1"/></si>` +
		`<si><r><t>he</t></r><r><t>llo</t></r><rPh sb="0" eb="1"><t>XX</t></rPh></si>` +
		`<si><t>plain</t></si></sst>`
	c := NewConverter(context.Background(), "", &settings.Settings{})
	got, err := c.parseSharedStrings(singleZipFile(t, "xl/sharedStrings.xml", xmlData))
	if err != nil {
		t.Fatalf("parseSharedStrings: %v", err)
	}
	if want := []string{"漢字", "hello", "plain"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

// t="inlineStr" без <is> не должен приводить к панике (nil-указатель).
func TestInlineStrWithoutIsElement(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml",
		sheetXML("A1:C1", rowXML(
			`<c r="A1" t="inlineStr"/>`,
			`<c r="B1" t="inlineStr"><v>7</v></c>`,
			`<c r="C1"><v>1</v></c>`))})
	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if got := readFile(t, filepath.Join(outDir, "0.csv")); got != ";;1\n" {
		t.Errorf("got %q, want %q", got, ";;1\n")
	}
}

// Пропуск колонок внутри строки заполняется пустыми значениями по атрибуту r.
func TestSparseRowsAreAlignedByColumn(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml",
		sheetXML("A1:E2",
			rowXML(`<c r="A1"><v>1</v></c>`, `<c r="D1"><v>4</v></c>`),
			rowXML(`<c r="C2"><v>3</v></c>`))})
	outDir, err := runConvert(t, path, ";")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if got, want := readFile(t, filepath.Join(outDir, "0.csv")), "1;;;4\n;;3\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// Выходной каталог недоступен: ошибка возвращается, а не теряется.
func TestConvertOutputDirMissing(t *testing.T) {
	t.Parallel()

	path := writeXlsx(t, zipEntry{"xl/worksheets/sheet1.xml", sheetXML("A1", rowXML(`<c r="A1"><v>1</v></c>`))})
	c := NewConverter(context.Background(), path, &settings.Settings{Delimiter: ";"})
	if err := c.Convert(filepath.Join(t.TempDir(), "no", "such")); err == nil {
		t.Fatal("expected error")
	}
}
