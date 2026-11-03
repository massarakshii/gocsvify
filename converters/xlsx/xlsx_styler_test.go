package xlsx

// Общие хелперы (singleZipFile, buildZip, ...) лежат в xlsx_test.go:
// оба тестовых файла нужно копировать вместе.

import (
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
)

const stylesHeader = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` +
	`<styleSheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">`

// --- NewXlsxStyler / SetStyles ---

func TestNewXlsxStylerReadsNumFmts(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<numFmts count="3">`+
		`<numFmt numFmtId="164" formatCode="yyyy\-mm\-dd"/>`+
		`<numFmt numFmtId="165" formatCode="0.00"/>`+
		`<numFmt numFmtId="166" formatCode="[$-409]h:mm:ss AM/PM"/>`+
		`</numFmts>`+
		`<cellXfs count="1"><xf numFmtId="0"/></cellXfs>`+
		`</styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}

	want := map[int]string{
		164: `yyyy\-mm\-dd`,
		165: "0.00",
		166: "[$-409]h:mm:ss AM/PM",
	}
	if !reflect.DeepEqual(styler.Nfts, want) {
		t.Errorf("numFmts mismatch:\n got: %+v\nwant: %+v", styler.Nfts, want)
	}
	if wantXf := []CellXf{{NumFmtId: 0}}; !reflect.DeepEqual(styler.Styles.Xf, wantXf) {
		t.Errorf("cellXfs mismatch:\n got: %+v\nwant: %+v", styler.Styles.Xf, wantXf)
	}
}

func TestNewXlsxStylerWithoutNumFmts(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<cellXfs count="1"><xf numFmtId="14"/></cellXfs></styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}
	if len(styler.Nfts) != 0 {
		t.Errorf("expected no custom numFmts, got %+v", styler.Nfts)
	}
	if want := []CellXf{{NumFmtId: 14}}; !reflect.DeepEqual(styler.Styles.Xf, want) {
		t.Errorf("cellXfs mismatch:\n got: %+v\nwant: %+v", styler.Styles.Xf, want)
	}
}

func TestNewXlsxStylerDecodesEntitiesAndUnicode(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<numFmts count="1"><numFmt numFmtId="170" formatCode="0.00&quot; руб.&quot;"/></numFmts></styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}
	if len(styler.Nfts) != 1 {
		t.Fatalf("expected 1 numFmt, got %d", len(styler.Nfts))
	}
	if got, want := styler.Nfts[170], `0.00" руб."`; got != want {
		t.Errorf("FormatCode = %q, want %q", got, want)
	}
}

func TestNewXlsxStylerMissingAttributesGiveZeroValues(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<numFmts count="2"><numFmt numFmtId="164"/><numFmt formatCode="0.0"/></numFmts></styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}
	// numFmt без formatCode даёт пустой код, numFmt без numFmtId попадает под id 0.
	want := map[int]string{164: "", 0: "0.0"}
	if !reflect.DeepEqual(styler.Nfts, want) {
		t.Errorf("numFmts mismatch:\n got: %+v\nwant: %+v", styler.Nfts, want)
	}
}

func TestNewXlsxStylerErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
	}{
		{"malformed XML", stylesHeader + `<numFmts><numFmt numFmtId="164" formatCode="x"/>`},
		{"mismatched tags", `<styleSheet><numFmts></styleSheet>`},
		{"non-numeric numFmtId", stylesHeader + `<numFmts><numFmt numFmtId="abc" formatCode="0"/></numFmts></styleSheet>`},
		{"not XML at all", `this is not xml <<<`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			styler, err := NewXlsxStyler(singleZipFile(t, "xl/styles.xml", tt.content))
			if err == nil {
				t.Fatalf("expected error, got styler %+v", styler)
			}
			if styler != nil {
				t.Errorf("expected nil styler on error, got %+v", styler)
			}
		})
	}
}

// Повторный SetStyles заменяет, а не дополняет список стилей.
func TestSetStylesReplacesPreviousStyles(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<numFmts count="1"><numFmt numFmtId="164" formatCode="0.0"/></numFmts></styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}
	if err := styler.SetStyles(); err != nil {
		t.Fatalf("second SetStyles failed: %v", err)
	}
	if len(styler.Nfts) != 1 {
		t.Errorf("expected 1 numFmt after repeated SetStyles, got %d", len(styler.Nfts))
	}
}

func TestNewXlsxStylerBuiltInDateStyles(t *testing.T) {
	t.Parallel()

	styler, err := NewXlsxStyler(singleZipFile(t, "xl/styles.xml", stylesHeader+`</styleSheet>`))
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}

	var ids []int
	for id := range styler.DateStyles {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	wantIDs := []int{14, 15, 16, 17, 18, 19, 20, 21, 22, 45, 46, 47}
	if !reflect.DeepEqual(ids, wantIDs) {
		t.Errorf("built-in date style ids = %v, want %v", ids, wantIDs)
	}
	if got := styler.DateStyles[14]; got != "m/d/yyyy" {
		t.Errorf("DateStyles[14] = %q, want %q", got, "m/d/yyyy")
	}
	if got := styler.DateStyles[22]; got != "m/d/yyyy h:mm" {
		t.Errorf("DateStyles[22] = %q, want %q", got, "m/d/yyyy h:mm")
	}
}

// --- CheckDateFormatCode ---

func TestCheckDateFormatCode(t *testing.T) {
	t.Parallel()

	styler := &XlsxStyler{}

	tests := []struct {
		name string
		code string
		want bool
	}{
		// форматы даты и времени
		{"ISO date", "yyyy-mm-dd", true},
		{"russian date", "dd.mm.yyyy", true},
		{"two-digit year", "dd/mm/yy", true},
		{"month/day/year", "m/d/yyyy", true},
		{"day-month-year short", "d-mmm-yy", true},
		{"month-year", "mmm-yy", true},
		{"time", "h:mm", true},
		{"time with seconds", "h:mm:ss", true},
		{"time with AM/PM", "h:mm:ss AM/PM", true},
		{"minutes and seconds", "mm:ss", true},
		{"fractional seconds", "mm:ss.0", true},
		{"date and time", "dd.mm.yyyy hh:mm:ss", true},
		{"upper case", "DD.MM.YYYY", true},
		{"mixed case", "Dd.Mm.YyYy", true},
		{"only seconds", "s", true},
		{"only day", "d", true},
		{"only four-digit year", "yyyy", true},
		{"only two-digit year", "yy", true},
		// проверка ищет только yy, m, d, h, s, поэтому коды эры (g, e) датой не считаются
		{"era year is not recognized", "ggge", false},

		// скобки: условия, цвета, локали и т.п. вырезаются
		{"elapsed hours", "[h]:mm:ss", true},
		{"elapsed minutes", "[mm]:ss", true},
		{"locale prefix before date", "[$-409]d-mmm-yy", true},
		{"locale F800", "[$-F800]dddd, mmmm dd, yyyy", true},
		{"color before date", "[Red]dd.mm.yyyy", true},
		{"condition with time", "[<1]h:mm", true},
		{"date chars only inside brackets", "[Red]0.00", false},
		{"locale currency in brackets", "[$€-407] #,##0.00", false},
		{"locale currency, dollar", "[$$-409]#,##0.00", false},
		{"condition before number", "[>=100]0.00", false},
		{"color and locale before number", "[Red][$-409]#,##0", false},

		// экранирование
		{"escaped dashes in date", `yyyy\-mm\-dd`, true},
		{"single escaped char", `\m`, false},
		{"all chars escaped", `\d\m`, false},
		{"escaped h", `\h`, false},
		{"escaped s", `\s`, false},
		{"escaped e", `\e`, false},
		{"escaped yy", `\yy`, false},
		{"escaped first, unescaped second occurrence", `\mm`, true},
		{"two escaped m", `\m\m`, false},
		{"escaped backslash, then m", `\\m`, true},
		{"three backslashes, then m", `\\\m`, false},
		{"four backslashes, then m", `\\\\m`, true},
		{"escaped text next to real date", `\d dd.mm.yyyy`, true},

		// не даты
		{"General", "General", false},
		{"general lower", "general", false},
		{"GENERAL upper", "GENERAL", false},
		{"empty", "", false},
		{"integer", "0", false},
		{"two decimals", "0.00", false},
		{"thousands", "#,##0", false},
		{"thousands with decimals", "#,##0.00", false},
		{"percent", "0%", false},
		{"percent with decimals", "0.00%", false},
		{"text", "@", false},
		{"fraction", "# ?/?", false},
		{"accounting", `_-* #,##0.00_-;\-* #,##0.00_-;_-* "-"??_-;_-@_-`, false},
		{"cyrillic text in format", "0.00 руб.", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code := tt.code
			if got := styler.CheckDateFormatCode(&code); got != tt.want {
				t.Errorf("CheckDateFormatCode(%q) = %v, want %v", tt.code, got, tt.want)
			}
		})
	}
}

func TestCheckDateFormatCodeNil(t *testing.T) {
	t.Parallel()

	if (&XlsxStyler{}).CheckDateFormatCode(nil) {
		t.Error("expected false for nil format code")
	}
}

func TestCheckDateFormatCodeDoesNotModifyInput(t *testing.T) {
	t.Parallel()

	code := "[Red]DD.MM.YYYY"
	(&XlsxStyler{}).CheckDateFormatCode(&code)
	if code != "[Red]DD.MM.YYYY" {
		t.Errorf("format code was modified: %q", code)
	}
}

// TestCheckDateFormatCodeKnownFalsePositives документирует текущее поведение:
// проверка ищет буквы d, m, s, e и т.п. по всей строке формата, не пропуская
// текст в двойных кавычках и экспоненту. Поэтому перечисленные ниже форматы,
// не являющиеся датами, распознаются как даты. Если проверку доработают,
// ожидания здесь нужно поменять на false.
func TestCheckDateFormatCodeKnownFalsePositives(t *testing.T) {
	t.Parallel()

	styler := &XlsxStyler{}

	tests := []struct {
		name string
		code string
	}{
		{"quoted text with m", `0.00"m"`},
		{"quoted currency code", `#,##0.00" USD"`},
		{"quoted word with s", `0 "pcs"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code := tt.code
			if !styler.CheckDateFormatCode(&code) {
				t.Errorf("CheckDateFormatCode(%q) = false; the known false positive is gone, update the test", tt.code)
			}
		})
	}
}

// --- CheckStyleDate ---

func TestCheckStyleDate(t *testing.T) {
	t.Parallel()

	styler := &XlsxStyler{
		DateStyles: map[int]string{14: "m/d/yyyy", 21: "h:mm:ss", 22: "m/d/yyyy h:mm"},
		Nfts: map[int]string{
			164: "yyyy-mm-dd",
			165: "0.00",
			21:  "0.00", // встроенный ID важнее кода
			166: "General",
			167: "[h]:mm",
		},
		// styleId - индекс в cellXfs.
		Styles: CellXfs{Xf: []CellXf{
			{NumFmtId: 0},   // styleId 0: General
			{NumFmtId: 164}, // styleId 1: пользовательская дата
			{NumFmtId: 165}, // styleId 2: число
			{NumFmtId: 14},  // styleId 3: встроенная дата без кода
			{NumFmtId: 21},  // styleId 4: встроенный ID важнее кода
			{NumFmtId: 166}, // styleId 5: General
			{NumFmtId: 167}, // styleId 6: прошедшее время
			{NumFmtId: 168}, // styleId 7: неизвестный ID, пустой код
			{NumFmtId: 22},  // styleId 8: встроенная дата с временем
		}},
	}

	tests := []struct {
		name    string
		styleID int
		want    bool
	}{
		{"zero (General style)", 0, false},
		{"negative", -1, false},
		{"very negative", -100, false},
		{"custom date code", 1, true},
		{"custom number code", 2, false},
		{"built-in date id without code", 3, true},
		{"built-in date id wins over number code", 4, true},
		{"General code", 5, false},
		{"elapsed time code", 6, true},
		{"unknown id with empty code", 7, false},
		{"built-in date-time id", 8, true},
		{"first out of range", 9, false},
		{"far out of range", 1000, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := styler.CheckStyleDate(tt.styleID); got != tt.want {
				t.Errorf("CheckStyleDate(%d) = %v, want %v", tt.styleID, got, tt.want)
			}
		})
	}
}

func TestCheckStyleDateEmptyStyles(t *testing.T) {
	t.Parallel()

	styler := &XlsxStyler{DateStyles: map[int]string{14: "m/d/yyyy"}}
	for _, id := range []int{-1, 0, 1, 2} {
		if styler.CheckStyleDate(id) {
			t.Errorf("CheckStyleDate(%d) = true for a styler without styles", id)
		}
	}
}

// Сквозная проверка: стили, прочитанные из styles.xml, используются в CheckStyleDate.
func TestCheckStyleDateFromParsedFile(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<numFmts count="3">`+
		`<numFmt numFmtId="164" formatCode="dd.mm.yyyy"/>`+
		`<numFmt numFmtId="165" formatCode="#,##0.00"/>`+
		`<numFmt numFmtId="14" formatCode="m/d/yyyy"/>`+
		`</numFmts>`+
		`<cellXfs count="5">`+
		`<xf numFmtId="0"/>`+ // 0: General
		`<xf numFmtId="164"/>`+ // 1: пользовательская дата
		`<xf numFmtId="165"/>`+ // 2: число
		`<xf numFmtId="14"/>`+ // 3: встроенная дата
		`<xf numFmtId="99"/>`+ // 4: неизвестный ID
		`</cellXfs></styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}

	want := map[int]bool{0: false, 1: true, 2: false, 3: true, 4: false}
	for id, w := range want {
		if got := styler.CheckStyleDate(id); got != w {
			t.Errorf("CheckStyleDate(%d) = %v, want %v", id, got, w)
		}
	}
}

// Атрибут s ячейки - индекс в <cellXfs>, а встроенные форматы (например, 14)
// не имеют записи в <numFmts>: дата распознаётся по numFmtId самого xf.
func TestStylerUsesCellXfsIndex(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<cellXfs count="2">`+
		`<xf numFmtId="0"/>`+
		`<xf numFmtId="14" applyNumberFormat="1"/>`+ // s="1": дата в cellXfs
		`</cellXfs></styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}
	if len(styler.Nfts) != 0 {
		t.Fatalf("no custom numFmts expected, got %+v", styler.Nfts)
	}
	if len(styler.Styles.Xf) != 2 {
		t.Fatalf("expected 2 cellXfs, got %+v", styler.Styles.Xf)
	}
	if styler.CheckStyleDate(0) {
		t.Error("CheckStyleDate(0) = true, want false (General)")
	}
	if !styler.CheckStyleDate(1) {
		t.Error("CheckStyleDate(1) = false, want true (built-in format 14)")
	}
	if styler.CheckStyleDate(2) {
		t.Error("CheckStyleDate(2) = true, want false (out of range)")
	}
}

// cellStyleXfs и dxfs содержат похожие теги, но не должны попадать в стили ячеек.
func TestStylerIgnoresCellStyleXfsAndDxfs(t *testing.T) {
	t.Parallel()

	file := singleZipFile(t, "xl/styles.xml", stylesHeader+
		`<numFmts count="1"><numFmt numFmtId="164" formatCode="0.00"/></numFmts>`+
		`<cellStyleXfs count="1"><xf numFmtId="14"/></cellStyleXfs>`+
		`<cellXfs count="1"><xf numFmtId="164"/></cellXfs>`+
		`<dxfs count="1"><dxf><numFmt numFmtId="164" formatCode="dd.mm.yyyy"/></dxf></dxfs>`+
		`</styleSheet>`)

	styler, err := NewXlsxStyler(file)
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}
	if want := []CellXf{{NumFmtId: 164}}; !reflect.DeepEqual(styler.Styles.Xf, want) {
		t.Errorf("cellXfs mismatch:\n got: %+v\nwant: %+v", styler.Styles.Xf, want)
	}
	if got := styler.Nfts[164]; got != "0.00" {
		t.Errorf("Nfts[164] = %q, want %q (dxfs must not override)", got, "0.00")
	}
	if styler.CheckStyleDate(0) {
		t.Error("CheckStyleDate(0) = true, want false")
	}
}

func TestStylerFormatCodeWithManyEntriesKeepsOrder(t *testing.T) {
	t.Parallel()

	var sb strings.Builder
	sb.WriteString(stylesHeader + `<numFmts count="50">`)
	for i := 0; i < 50; i++ {
		code := "0.00"
		if i%2 == 0 {
			code = "dd.mm.yyyy"
		}
		sb.WriteString(`<numFmt numFmtId="` + strconv.Itoa(164+i) + `" formatCode="` + code + `"/>`)
	}
	sb.WriteString(`</numFmts><cellXfs count="50">`)
	for i := 0; i < 50; i++ {
		sb.WriteString(`<xf numFmtId="` + strconv.Itoa(164+i) + `"/>`)
	}
	sb.WriteString(`</cellXfs></styleSheet>`)

	styler, err := NewXlsxStyler(singleZipFile(t, "xl/styles.xml", sb.String()))
	if err != nil {
		t.Fatalf("NewXlsxStyler failed: %v", err)
	}
	if len(styler.Nfts) != 50 || len(styler.Styles.Xf) != 50 {
		t.Fatalf("expected 50 numFmts and 50 cellXfs, got %d and %d", len(styler.Nfts), len(styler.Styles.Xf))
	}
	for i := 0; i < 50; i++ {
		if got, want := styler.CheckStyleDate(i), i%2 == 0; got != want {
			t.Errorf("CheckStyleDate(%d) = %v, want %v", i, got, want)
		}
	}
}
