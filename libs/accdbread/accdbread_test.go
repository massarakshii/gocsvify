package accdbread

import (
	"bytes"
	"encoding/csv"
	"errors"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeHeader строит файл с корректной сигнатурой Jet/ACE и заданной версией; остальное - нули.
func fakeHeader(version uint32, sig string, pages, pageSize int) []byte {
	data := make([]byte, pages*pageSize)
	data[1] = 1 // сигнатура начала файла: 00 01 00 00
	copy(data[4:], sig)
	data[0x14] = byte(version)
	data[0x15] = byte(version >> 8)
	return data
}

func writeTmp(t testing.TB, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "db.accdb")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOpenRejectsInvalidFiles(t *testing.T) {
	t.Parallel()

	tests := map[string][]byte{
		"empty":        {},
		"too short":    make([]byte, 100),
		"zeros":        make([]byte, 8192),
		"text":         bytes.Repeat([]byte("not an access file "), 500),
		"wrong sig":    fakeHeader(1, "Standard XYZ DB", 4, 4096),
		"unknown ver":  fakeHeader(0x20, "Standard Jet DB", 4, 4096),
		"unknown ver2": fakeHeader(0x7F, "Standard ACE DB", 4, 4096),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			db, err := Open(writeTmp(t, data), 0)
			if err == nil {
				db.Close()
				t.Fatal("expected error")
			}
		})
	}
}

func TestOpenAcceptsKnownVersions(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		ver  uint32
		sig  string
		size int
	}{{0, "Standard Jet DB", 2048}, {1, "Standard Jet DB", 4096}, {2, "Standard ACE DB", 4096}, {3, "Standard ACE DB", 4096}, {0x0603, "Standard ACE DB", 4096}} {
		db, err := Open(writeTmp(t, fakeHeader(tc.ver, tc.sig, 6, tc.size)), 0)
		if err != nil {
			t.Errorf("ver %#x: %v", tc.ver, err)
			continue
		}
		db.Close()
	}
}

func TestOpenMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := Open(filepath.Join(t.TempDir(), "nope.accdb"), 0); err == nil {
		t.Fatal("expected error")
	}
	if _, err := GetTables(filepath.Join(t.TempDir(), "nope.accdb")); err == nil {
		t.Fatal("GetTables: expected error")
	}
	var buf bytes.Buffer
	if err := ConvertToCsv(filepath.Join(t.TempDir(), "nope.accdb"), "T", &buf, ';', 0); err == nil {
		t.Fatal("ConvertToCsv: expected error")
	}
}

// База с корректным заголовком, но без каталога таблиц: ошибка либо пустой список, без паники.
func TestEmptyDatabaseDoesNotPanic(t *testing.T) {
	t.Parallel()

	for _, size := range []int{2048, 4096} {
		ver := uint32(1)
		if size == 2048 {
			ver = 0
		}
		p := writeTmp(t, fakeHeader(ver, "Standard Jet DB", 8, size))
		tables, err := GetTables(p)
		if err == nil && len(tables) != 0 {
			t.Errorf("size %d: unexpected tables %v", size, tables)
		}
		var buf bytes.Buffer
		err = ConvertToCsv(p, "Missing", &buf, ';', 0)
		if err == nil {
			t.Errorf("size %d: expected an error for a missing table", size)
		}
	}
}

func TestErrTableNotFoundIsExported(t *testing.T) {
	t.Parallel()
	if errors.Unwrap(errors.New("x")) != nil || ErrTableNotFound == nil {
		t.Fatal("ErrTableNotFound must be a sentinel error")
	}
}

// Случайный мусор после валидного заголовка не должен приводить к панике или бесконечному циклу.
func TestRandomPagesDoNotPanic(t *testing.T) {
	t.Parallel()

	rng := rand.New(rand.NewSource(7))
	for i := 0; i < 300; i++ {
		size := 4096
		ver := uint32(1)
		if i%3 == 0 {
			size, ver = 2048, 0
		}
		data := fakeHeader(ver, "Standard Jet DB", 12, size)
		for off := size; off < len(data); off++ {
			if rng.Intn(3) == 0 {
				data[off] = byte(rng.Intn(256))
			}
		}
		p := writeTmp(t, data)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("iteration %d: panic: %v", i, r)
				}
			}()
			tables, err := GetTables(p)
			if err != nil {
				return
			}
			for _, tb := range tables {
				var buf bytes.Buffer
				_ = ConvertToCsv(p, tb.Name, &buf, ';', 100)
			}
		}()
	}
}

func FuzzOpen(f *testing.F) {
	f.Add(fakeHeader(1, "Standard Jet DB", 4, 4096))
	f.Fuzz(func(t *testing.T, data []byte) {
		p := filepath.Join(t.TempDir(), "f.accdb")
		_ = os.WriteFile(p, data, 0o644)
		if tables, err := GetTables(p); err == nil {
			for _, tb := range tables {
				var buf bytes.Buffer
				_ = ConvertToCsv(p, tb.Name, &buf, ';', 10)
			}
		}
	})
}

// --- реальная база (converters/accdb/testdata/sample.accdb) ---
// В ней 5 пользовательских таблиц, строк данных нет - проверяется разбор схемы и заголовков.

const samplePath = "../../converters/accdb/testdata/sample.accdb"

var sampleHeaders = map[string]string{
	"Детали":             "КодДетали;Название;Цвет;Вес;Эскиз\n",
	`Копия "Поставщики"`: "КодПоставщика;Поставщик;Город;Рейтинг\n",
	"Поставки":           "КодПоставщика;КодДетали;Дата;Количество\n",
	"Поставщики":         "КодПоставщика;Поставщик;Город;Рейтинг\n",
	"ПоставщикиДетали":   "Код\n",
}

func TestSampleTables(t *testing.T) {
	t.Parallel()

	tables, err := GetTables(samplePath)
	if err != nil {
		t.Fatalf("GetTables: %v", err)
	}
	got := map[string]bool{}
	for i, tb := range tables {
		got[tb.Name] = true
		if tb.Err != nil {
			t.Errorf("%s: table error %v", tb.Name, tb.Err)
		}
		if tb.System {
			t.Errorf("%s: system tables must be excluded", tb.Name)
		}
		if i > 0 && tables[i-1].Name > tb.Name {
			t.Errorf("tables must be sorted by name: %q before %q", tables[i-1].Name, tb.Name)
		}
	}
	if len(got) != len(sampleHeaders) {
		t.Errorf("got %d tables %v, want %d", len(got), got, len(sampleHeaders))
	}
	for name := range sampleHeaders {
		if !got[name] {
			t.Errorf("table %q not found", name)
		}
	}
}

func TestSampleConvertToCsv(t *testing.T) {
	t.Parallel()

	for name, want := range sampleHeaders {
		var buf bytes.Buffer
		if err := ConvertToCsv(samplePath, name, &buf, ';', 0); err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if buf.String() != want {
			t.Errorf("%s:\n got: %q\nwant: %q", name, buf.String(), want)
		}
	}
}

func TestSampleConvertToCsvOptions(t *testing.T) {
	t.Parallel()

	// Имя таблицы без учёта регистра.
	var buf bytes.Buffer
	if err := ConvertToCsv(samplePath, "ДЕТАЛИ", &buf, ',', 0); err != nil {
		t.Fatal(err)
	}
	if want := "КодДетали,Название,Цвет,Вес,Эскиз\n"; buf.String() != want {
		t.Errorf("got %q, want %q", buf.String(), want)
	}

	// Несуществующая таблица.
	err := ConvertToCsv(samplePath, "Нет такой", &buf, ';', 0)
	if !errors.Is(err, ErrTableNotFound) {
		t.Errorf("err = %v, want ErrTableNotFound", err)
	}
}

func TestSampleWarningsAreZero(t *testing.T) {
	t.Parallel()

	db, err := Open(samplePath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables, err := db.Tables(false)
	if err != nil {
		t.Fatal(err)
	}
	for _, tb := range tables {
		if err := db.DumpCSV(tb, &bytes.Buffer{}, ';', 0); err != nil {
			t.Errorf("%s: %v", tb.Name, err)
		}
	}
	if bad, lval := db.Warnings(); bad != 0 || lval != 0 {
		t.Errorf("warnings: badRows=%d badLval=%d", bad, lval)
	}
}

// Повреждения реальной базы не приводят к панике.
func TestSampleCorruptedDoesNotPanic(t *testing.T) {
	t.Parallel()

	orig, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	rng := rand.New(rand.NewSource(3))
	for i := 0; i < 150; i++ {
		data := append([]byte(nil), orig...)
		for k := rng.Intn(40) + 1; k > 0; k-- {
			data[0x100+rng.Intn(len(data)-0x100)] = byte(rng.Intn(256))
		}
		if i%10 == 0 {
			data = data[:rng.Intn(len(data))]
		}
		p := writeTmp(t, data)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("iteration %d: panic: %v", i, r)
				}
			}()
			tables, err := GetTables(p)
			if err != nil {
				return
			}
			for _, tb := range tables {
				_ = ConvertToCsv(p, tb.Name, &bytes.Buffer{}, ';', 0)
			}
		}()
	}
}

// --- Northwind 2007 (converters/accdb/testdata/northwind.accdb): реальные данные ---

const northwindPath = "../../converters/accdb/testdata/northwind.accdb"

// Число строк по таблицам известно по оригинальной базе Northwind 2007.
var northwindRows = map[string]int{
	"Customers": 29, "Employee Privileges": 1, "Employees": 9, "Inventory Transaction Types": 4,
	"Inventory Transactions": 102, "Invoices": 35, "Order Details": 58, "Order Details Status": 6,
	"Orders": 48, "Orders Status": 4, "Orders Tax Status": 2, "Privileges": 1, "Products": 45,
	"Purchase Order Details": 55, "Purchase Order Status": 4, "Purchase Orders": 28,
	"Sales Reports": 5, "Shippers": 3, "Strings": 62, "Suppliers": 10,
}

func northwindTable(t *testing.T, name string) [][]string {
	t.Helper()
	var buf bytes.Buffer
	if err := ConvertToCsv(northwindPath, name, &buf, ';', 0); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	r := csv.NewReader(&buf)
	r.Comma = ';'
	recs, err := r.ReadAll() // также проверяет, что число полей во всех строках одинаково
	if err != nil {
		t.Fatalf("%s: invalid CSV: %v", name, err)
	}
	return recs
}

func TestNorthwindRowCounts(t *testing.T) {
	t.Parallel()

	tables, err := GetTables(northwindPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != len(northwindRows) {
		t.Errorf("got %d tables, want %d", len(tables), len(northwindRows))
	}
	for _, tb := range tables {
		want, ok := northwindRows[tb.Name]
		if !ok {
			t.Errorf("unexpected table %q", tb.Name)
			continue
		}
		if got := len(northwindTable(t, tb.Name)) - 1; got != want {
			t.Errorf("%s: %d rows, want %d", tb.Name, got, want)
		}
	}
}

func TestNorthwindValues(t *testing.T) {
	t.Parallel()

	find := func(recs [][]string, id string) []string {
		for _, r := range recs[1:] {
			if r[0] == id {
				return r
			}
		}
		t.Fatalf("row with id %q not found", id)
		return nil
	}

	emp := northwindTable(t, "Employees")
	wantHeader := []string{"ID", "Company", "Last Name", "First Name", "E-mail Address", "Job Title", "Business Phone",
		"Home Phone", "Mobile Phone", "Fax Number", "Address", "City", "State/Province", "ZIP/Postal Code",
		"Country/Region", "Web Page", "Notes", "Attachments"}
	if !reflect.DeepEqual(emp[0], wantHeader) {
		t.Errorf("Employees header = %q", emp[0])
	}
	wantNancy := []string{"1", "Northwind Traders", "Freehafer", "Nancy", "nancy@northwindtraders.com", "Sales Representative",
		"(123)555-0100", "(123)555-0102", "", "(123)555-0103", "123 1st Avenue", "Seattle", "WA", "99999", "USA",
		"#http://northwindtraders.com#", "", "1"}
	if got := find(emp, "1"); !reflect.DeepEqual(got, wantNancy) {
		t.Errorf("Employees/1:\n got: %q\nwant: %q", got, wantNancy)
	}
	// Длинный MEMO-текст (Notes) читается целиком.
	if notes := find(emp, "2")[16]; !strings.HasPrefix(notes, "Joined the company as a sales representative") || !strings.HasSuffix(notes, "vice president of sales.") {
		t.Errorf("Employees/2 notes = %q", notes)
	}

	// Числа (валюта/целые), булевы значения, пустые (NULL) поля.
	prod := northwindTable(t, "Products")
	wantChai := []string{"1", "1", "NWTB-1", "Northwind Traders Chai", "", "13.5000", "18.0000", "10", "40", "10 boxes x 20 bags", "false", "10", "Beverages", "1"}
	if got := find(prod, "1"); got[1] != "1" || !reflect.DeepEqual(got, wantChai) {
		// у Products первая колонка - Supplier IDs, ID во второй
		t.Errorf("Products first row = %q", prod[1])
	}

	// Даты.
	orders := northwindTable(t, "Orders")
	o30 := find(orders, "30")
	if o30[4] != "2006-01-15 00:00:00" || o30[5] != "2006-01-22 00:00:00" {
		t.Errorf("Orders/30 dates = %q, %q", o30[4], o30[5])
	}
	tx := northwindTable(t, "Inventory Transactions")
	if got := find(tx, "35")[2]; got != "2006-03-22 16:02:28" {
		t.Errorf("Inventory Transactions/35 created date = %q", got)
	}

	// Текст с кавычками и запятыми экранируется корректно (SQL внутри значения).
	reports := northwindTable(t, "Sales Reports")
	var found bool
	for _, r := range reports[1:] {
		if r[0] == "Category" && r[3] == "SELECT DISTINCT [Category] FROM [Products] ORDER BY [Category];" {
			found = true
		}
	}
	if !found {
		t.Errorf("Sales Reports rows = %q", reports[1:])
	}
}

func TestNorthwindNoWarnings(t *testing.T) {
	t.Parallel()

	db, err := Open(northwindPath, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tables, _ := db.Tables(false)
	for _, tb := range tables {
		if err := db.DumpCSV(tb, io.Discard, ';', 0); err != nil {
			t.Errorf("%s: %v", tb.Name, err)
		}
	}
	if bad, lval := db.Warnings(); bad != 0 || lval != 0 {
		t.Errorf("warnings: badRows=%d badLval=%d", bad, lval)
	}
}

func TestNorthwindLimit(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	if err := ConvertToCsv(northwindPath, "Customers", &buf, ';', 5); err != nil {
		t.Fatal(err)
	}
	recs, _ := csv.NewReader(strings.NewReader(strings.ReplaceAll(buf.String(), ";", ","))).ReadAll()
	if len(recs) != 6 { // заголовок + 5
		t.Errorf("limit 5: got %d records, want 6", len(recs))
	}
}

func TestNorthwindCorruptedDoesNotPanic(t *testing.T) {
	t.Parallel()

	orig, err := os.ReadFile(northwindPath)
	if err != nil {
		t.Fatal(err)
	}
	if testing.Short() {
		t.Skip("долгий тест (читает 3.6 МБ базу десятки раз)")
	}
	rng := rand.New(rand.NewSource(11))
	for i := 0; i < 20; i++ {
		data := append([]byte(nil), orig...)
		for k := rng.Intn(200) + 1; k > 0; k-- {
			data[0x100+rng.Intn(len(data)-0x100)] = byte(rng.Intn(256))
		}
		p := writeTmp(t, data)
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("iteration %d: panic: %v", i, r)
				}
			}()
			tables, err := GetTables(p)
			if err != nil {
				return
			}
			for _, tb := range tables {
				_ = ConvertToCsv(p, tb.Name, io.Discard, ';', 0)
			}
		}()
	}
}
