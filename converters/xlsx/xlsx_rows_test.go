package xlsx

import (
	"bytes"
	"context"
	"encoding/csv"
	"strconv"
	"strings"
	"testing"
)

// --- булевы ячейки ---

func TestSheetToCsvBooleans(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		cell   string
		styler *XlsxStyler
		want   string
	}{
		{"1 is TRUE", `<c r="A1" t="b"><v>1</v></c>`, nil, "TRUE\n"},
		{"0 is FALSE", `<c r="A1" t="b"><v>0</v></c>`, nil, "FALSE\n"},
		{"true word", `<c r="A1" t="b"><v>true</v></c>`, nil, "TRUE\n"},
		{"false word", `<c r="A1" t="b"><v>false</v></c>`, nil, "FALSE\n"},
		{"case and spaces are ignored", `<c r="A1" t="b"><v> True </v></c>`, nil, "TRUE\n"},
		{"empty value stays empty", `<c r="A1" t="b"><v></v></c>`, nil, "\n"},
		{"cell without value stays empty", `<c r="A1" t="b"/>`, nil, "\n"},
		{"unexpected value is kept as is", `<c r="A1" t="b"><v>2</v></c>`, nil, "2\n"},
		// Булева ячейка в столбце с форматом даты не превращается в дату.
		{"date style does not apply to booleans", `<c r="A1" s="1" t="b"><v>1</v></c>`, dateStyler, "TRUE\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := sheetToCSV(t, newTestConverter(",", false), sheetXML("A1", rowXML(tt.cell)), nil, tt.styler)
			if err != nil {
				t.Fatalf("sheetToCsv failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// --- восстановление пропущенных строк ---

// newRestoreConverter создаёт конвертер с включённой настройкой restoreEmptyRows.
func newRestoreConverter(padRows bool) *Converter {
	c := newTestConverter(",", padRows)
	c.Settings.RestoreEmptyRows = true
	return c
}

func rowAt(n int, cells ...string) string {
	return `<row r="` + strconv.Itoa(n) + `">` + strings.Join(cells, "") + `</row>`
}

func TestSheetToCsvRestoresEmptyRows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sheet   string
		padRows bool
		want    string
	}{
		{
			// Регрессия: подряд идущие строки не порождают лишних строк.
			name: "consecutive rows are not duplicated",
			sheet: sheetXML("A1:B3",
				rowAt(1, `<c r="A1"><v>1</v></c>`, `<c r="B1"><v>11</v></c>`),
				rowAt(2, `<c r="A2"><v>2</v></c>`, `<c r="B2"><v>22</v></c>`),
				rowAt(3, `<c r="A3"><v>3</v></c>`, `<c r="B3"><v>33</v></c>`)),
			want: "1,11\n2,22\n3,33\n",
		},
		{
			// Регрессия: восстановленные строки пустые, данные предыдущей строки в них не попадают.
			name: "restored rows are empty and as wide as the table",
			sheet: sheetXML("A1:B4",
				rowAt(1, `<c r="A1"><v>1</v></c>`, `<c r="B1"><v>11</v></c>`),
				rowAt(4, `<c r="A4"><v>4</v></c>`, `<c r="B4"><v>44</v></c>`)),
			want: "1,11\n,\n,\n4,44\n",
		},
		{
			name: "leading empty rows are restored",
			sheet: sheetXML("A3:B3",
				rowAt(3, `<c r="A3"><v>x</v></c>`, `<c r="B3"><v>y</v></c>`)),
			want: ",\n,\nx,y\n",
		},
		{
			name: "restored rows keep full width without PadRows while data rows stay trimmed",
			sheet: sheetXML("A1:C4",
				rowAt(1, `<c r="A1"><v>1</v></c>`),
				rowAt(4, `<c r="A4"><v>4</v></c>`)),
			want: "1\n,,\n,,\n4\n",
		},
		{
			name:    "restored rows with PadRows",
			padRows: true,
			sheet: sheetXML("A1:C4",
				rowAt(1, `<c r="A1"><v>1</v></c>`),
				rowAt(4, `<c r="A4"><v>4</v></c>`)),
			want: "1,,\n,,\n,,\n4,,\n",
		},
		{
			name: "rows without the r attribute follow the previous one",
			sheet: sheetXML("A1:A3",
				rowAt(1, `<c r="A1"><v>1</v></c>`),
				rowXML(`<c r="A2"><v>2</v></c>`),
				rowAt(3, `<c r="A3"><v>3</v></c>`)),
			want: "1\n2\n3\n",
		},
		{
			name: "a row without r after a gap does not restore anything",
			sheet: sheetXML("A1:B6",
				rowAt(1, `<c r="A1"><v>1</v></c>`),
				rowAt(4, `<c r="A4"><v>4</v></c>`),
				rowXML(`<c r="A5"><v>5</v></c>`)),
			want: "1\n,\n,\n4\n5\n",
		},
		{
			name: "explicit empty row and a gap after it",
			sheet: sheetXML("A1:B5",
				rowAt(1, `<c r="A1"><v>1</v></c>`, `<c r="B1"><v>2</v></c>`),
				`<row r="2"/>`,
				rowAt(5, `<c r="A5"><v>5</v></c>`, `<c r="B5"><v>6</v></c>`)),
			want: "1,2\n\n,\n,\n5,6\n",
		},
		{
			name: "duplicate and out-of-order row numbers add nothing",
			sheet: sheetXML("A1:A3",
				rowAt(2, `<c r="A2"><v>2</v></c>`),
				rowAt(2, `<c r="A2"><v>2</v></c>`),
				rowAt(1, `<c r="A1"><v>1</v></c>`)),
			want: "\n2\n2\n1\n",
		},
		{
			name: "invalid row number is treated as missing",
			sheet: sheetXML("A1:A2",
				`<row r="abc"><c r="A1"><v>1</v></c></row>`,
				`<row r="-5"><c r="A2"><v>2</v></c></row>`),
			want: "1\n2\n",
		},
		{
			name: "column offset of the dimension is kept",
			sheet: sheetXML("B1:C3",
				rowAt(1, `<c r="B1"><v>1</v></c>`, `<c r="C1"><v>2</v></c>`),
				rowAt(3, `<c r="B3"><v>3</v></c>`, `<c r="C3"><v>4</v></c>`)),
			want: "1,2\n,\n3,4\n",
		},
		{
			// Без <dimension> ширина считается по самой правой ячейке листа.
			name: "gap width without dimension comes from the widest cell",
			sheet: sheetXML("",
				rowAt(1, `<c r="A1"><v>1</v></c>`),
				rowAt(3, `<c r="A3"><v>3</v></c>`, `<c r="D3"><v>4</v></c>`)),
			want: "1\n,,,\n3,,,4\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := sheetToCSV(t, newRestoreConverter(tt.padRows), tt.sheet, nil, nil)
			if err != nil {
				t.Fatalf("sheetToCsv failed: %v", err)
			}
			if got != tt.want {
				t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

// По умолчанию (restoreEmptyRows выключена) пропущенные строки не восстанавливаются.
func TestSheetToCsvDoesNotRestoreEmptyRowsByDefault(t *testing.T) {
	t.Parallel()

	sheet := sheetXML("A1:B4",
		rowAt(1, `<c r="A1"><v>1</v></c>`, `<c r="B1"><v>11</v></c>`),
		rowAt(4, `<c r="A4"><v>4</v></c>`, `<c r="B4"><v>44</v></c>`))
	got, err := sheetToCSV(t, newTestConverter(",", false), sheet, nil, nil)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}
	if want := "1,11\n4,44\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Пропуск больше лимита не восстанавливается целиком, а номера строк после него не «плывут»:
// следующие пропуски обрабатываются как обычно.
func TestSheetToCsvTooLargeGapDoesNotAffectLaterGaps(t *testing.T) {
	t.Parallel()

	sheet := sheetXML("A1:Z1",
		rowAt(1, `<c r="A1"><v>1</v></c>`),
		rowAt(1048576, `<c r="A1048576"><v>9</v></c>`),
		rowAt(1048579, `<c r="A1048579"><v>12</v></c>`))
	got, err := sheetToCSV(t, newRestoreConverter(false), sheet, nil, nil)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}
	empty := strings.Repeat(",", 25) + "\n"
	if want := "1\n9\n" + empty + empty + "12\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

// Файл с ячейкой в очень далёкой строке не должен раздувать CSV: слишком большой пропуск
// не восстанавливается (а сама строка и данные не теряются).
func TestSheetToCsvHugeGapIsNotRestored(t *testing.T) {
	t.Parallel()

	// Ширина 26 столбцов, пропуск около миллиона строк: порядка 27 млн ячеек, больше лимита.
	sheet := sheetXML("A1:Z1",
		rowAt(1, `<c r="A1"><v>1</v></c>`),
		rowAt(1048576, `<c r="A1048576"><v>9</v></c>`))
	got, err := sheetToCSV(t, newRestoreConverter(false), sheet, nil, nil)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}
	if want := "1\n9\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestSheetToCsvModerateGapIsRestored(t *testing.T) {
	t.Parallel()

	sheet := sheetXML("A1:B1", rowAt(1, `<c r="A1"><v>1</v></c>`), rowAt(2001, `<c r="A2001"><v>9</v></c>`))
	got, err := sheetToCSV(t, newRestoreConverter(false), sheet, nil, nil)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}
	if want := "1\n" + strings.Repeat(",\n", 1999) + "9\n"; got != want {
		t.Errorf("unexpected CSV: %d lines", strings.Count(got, "\n"))
	}
}

func TestWriteEmptyRows(t *testing.T) {
	t.Parallel()

	write := func(count, width, budget int) (string, int) {
		var buf bytes.Buffer
		w := csv.NewWriter(&buf)
		w.Comma = ';'
		c := NewConverter(context.Background(), "", nil)
		if err := c.writeEmptyRows(w, count, width, &budget); err != nil {
			t.Fatalf("writeEmptyRows: %v", err)
		}
		w.Flush()
		return buf.String(), budget
	}

	if got, left := write(3, 3, 100); got != ";;\n;;\n;;\n" || left != 91 {
		t.Errorf("width 3: got %q, budget left %d", got, left)
	}
	if got, left := write(2, 1, 100); got != "\n\n" || left != 98 {
		t.Errorf("width 1: got %q, budget left %d", got, left)
	}
	if got, left := write(2, 0, 100); got != "\n\n" || left != 98 {
		t.Errorf("width 0: got %q, budget left %d", got, left)
	}
	if got, left := write(0, 5, 100); got != "" || left != 100 {
		t.Errorf("count 0: got %q, budget left %d", got, left)
	}
	// Ровно по бюджету помещается, на одну ячейку больше - нет, и бюджет не тратится.
	if got, left := write(4, 2, 8); got == "" || left != 0 {
		t.Errorf("exact fit: got %q, budget left %d", got, left)
	}
	if got, left := write(4, 2, 7); got != "" || left != 7 {
		t.Errorf("one cell over: got %q, budget left %d", got, left)
	}
	// Переполнение count*width не приводит к записи.
	if got, _ := write(1<<40, 1<<40, maxRestoredCells); got != "" {
		t.Errorf("overflowing gap must be skipped, got %d bytes", len(got))
	}
}

// Бюджет общий на лист: много небольших пропусков в сумме не должны превысить лимит.
func TestWriteEmptyRowsBudgetIsSharedBetweenGaps(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	c := NewConverter(context.Background(), "", nil)
	budget := 10

	for _, count := range []int{2, 2, 1} { // по 3 ячейки на строку: 6, затем 6 (не помещается), затем 3
		if err := c.writeEmptyRows(w, count, 3, &budget); err != nil {
			t.Fatal(err)
		}
	}
	w.Flush()

	if want := ",,\n,,\n" + ",,\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}
	if budget != 1 {
		t.Errorf("budget left = %d, want 1", budget)
	}
}

func TestWriteEmptyRowsStopsOnCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := NewConverter(ctx, "", nil)

	var buf bytes.Buffer
	budget := maxRestoredCells
	if err := c.writeEmptyRows(csv.NewWriter(&buf), 10, 2, &budget); err == nil {
		t.Error("expected context error")
	}
}

// Предварительный проход: пропускает содержимое ячеек, не падает на пустом листе.
func TestGetColumnIndexes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		sheet        string
		padRows      bool
		wantS, wantE int
	}{
		{"dimension wins", sheetXML("B2:D9", rowAt(2, `<c r="Z2"><v>1</v></c>`)), true, 2, 4},
		{"dimension is used even when the width is not needed", sheetXML("B2:D9", rowAt(2, `<c r="Z2"><v>1</v></c>`)), false, 2, 4},
		{"single cell dimension", sheetXML("C5", rowAt(5, `<c r="C5"><v>1</v></c>`)), true, 3, 3},
		{"no dimension: widest cell", sheetXML("", rowAt(1, `<c r="A1"><v>1</v></c>`, `<c r="E1"><v>2</v></c>`)), true, 1, 5},
		{"no dimension: inline strings are skipped without parsing", sheetXML("", rowAt(1, `<c r="C1" t="inlineStr"><is><t>x</t></is></c>`)), true, 1, 3},
		{"no dimension and no cells", sheetXML(""), true, 1, 1},
		// Ширина не нужна: лист без <dimension> не сканируется целиком, границы по умолчанию.
		{"no dimension, width not needed: no full scan", sheetXML("", rowAt(1, `<c r="A1"><v>1</v></c>`, `<c r="E1"><v>2</v></c>`)), false, 1, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", tt.padRows)
			f := singleZipFile(t, "xl/worksheets/sheet1.xml", tt.sheet)
			s, e, err := c.getColumnIndexes(f)
			if err != nil {
				t.Fatalf("getColumnIndexes: %v", err)
			}
			if s != tt.wantS || e != tt.wantE {
				t.Errorf("got (%d, %d), want (%d, %d)", s, e, tt.wantS, tt.wantE)
			}
		})
	}
}

// Левая граница берётся из <dimension> независимо от настроек: вывод не должен зависеть от того,
// включены ли padRows и restoreEmptyRows.
func TestSheetToCsvDimensionOffsetDoesNotDependOnSettings(t *testing.T) {
	t.Parallel()

	sheet := sheetXML("B1:C2",
		rowAt(1, `<c r="B1"><v>1</v></c>`, `<c r="C1"><v>2</v></c>`),
		rowAt(2, `<c r="B2"><v>3</v></c>`, `<c r="C2"><v>4</v></c>`))

	for name, mutate := range map[string]func(*Converter){
		"defaults": func(c *Converter) {},
		"padRows":  func(c *Converter) { c.Settings.PadRows = true },
		"restore":  func(c *Converter) { c.Settings.RestoreEmptyRows = true },
		"both":     func(c *Converter) { c.Settings.PadRows, c.Settings.RestoreEmptyRows = true, true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			c := newTestConverter(",", false)
			mutate(c)
			got, err := sheetToCSV(t, c, sheet, nil, nil)
			if err != nil {
				t.Fatalf("sheetToCsv failed: %v", err)
			}
			if want := "1,2\n3,4\n"; got != want {
				t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
			}
		})
	}
}

// Не поместившийся в бюджет пропуск пропускается целиком и ничего не тратит: следующий небольшой
// пропуск восстанавливается как обычно, расстояние между строками 9 и 12 сохраняется.
func TestSheetToCsvOversizedGapIsSkippedWholeAndSmallGapsStillWork(t *testing.T) {
	t.Parallel()

	// 5 999 998 пустых строк по 2 ячейки - почти 12 млн, больше лимита.
	sheet := sheetXML("A1:B1",
		rowAt(1, `<c r="A1"><v>1</v></c>`),
		rowAt(6000000, `<c r="A6000000"><v>9</v></c>`),
		rowAt(6000003, `<c r="A6000003"><v>12</v></c>`))
	got, err := sheetToCSV(t, newRestoreConverter(false), sheet, nil, nil)
	if err != nil {
		t.Fatalf("sheetToCsv failed: %v", err)
	}
	if want := "1\n9\n,\n,\n12\n"; got != want {
		t.Errorf("CSV mismatch:\n got: %q\nwant: %q", got, want)
	}
}
