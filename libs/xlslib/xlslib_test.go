package xlslib

import (
	"bytes"
	"math/rand"
	"os"
	"reflect"
	"testing"
	"time"
)

// Реальный xls (LibreOffice): лист «Лист1» 4x4 и лист «Second» с 1,2,3.
const samplePath = "../../converters/xls/testdata/sample.xls"

func loadSample(t testing.TB) []byte {
	t.Helper()
	data, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatalf("sample.xls: %v", err)
	}
	return data
}

func TestReadSheetsSample(t *testing.T) {
	t.Parallel()

	sheets, err := ReadSheets(bytes.NewReader(loadSample(t)))
	if err != nil {
		t.Fatalf("ReadSheets: %v", err)
	}
	if len(sheets) != 2 {
		t.Fatalf("got %d sheets, want 2", len(sheets))
	}
	if sheets[0].Name != "Лист1" || sheets[1].Name != "Second" {
		t.Errorf("sheet names: %q, %q", sheets[0].Name, sheets[1].Name)
	}
	want0 := [][]string{
		{"Имя", "Сумма", "Дата", "Флаг"},
		{"Анна", "30.5", "2023-03-15", "TRUE"},
		{"Борис", "7", "2023-03-15 12:30:00", ""},
		{"a;b", `c"d`, "line\nbreak", ""},
	}
	if !reflect.DeepEqual(sheets[0].Rows, want0) {
		t.Errorf("sheet 0:\n got: %q\nwant: %q", sheets[0].Rows, want0)
	}
	if want1 := [][]string{{"1", "2", "3"}}; !reflect.DeepEqual(sheets[1].Rows, want1) {
		t.Errorf("sheet 1: got %q, want %q", sheets[1].Rows, want1)
	}
}

func TestReadSheetsRejectsGarbage(t *testing.T) {
	t.Parallel()

	for name, data := range map[string][]byte{
		"nil":               nil,
		"empty":             {},
		"short":             []byte("abc"),
		"text":              bytes.Repeat([]byte("hello "), 200),
		"zeros":             make([]byte, 4096),
		"only OLE sig":      append(append([]byte{}, oleSignature...), make([]byte, 600)...),
		"xlsx (zip) as xls": append([]byte("PK\x03\x04"), make([]byte, 2000)...),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if sheets, err := ReadSheets(bytes.NewReader(data)); err == nil {
				t.Errorf("expected error, got %d sheets", len(sheets))
			}
		})
	}
}

// Любые повреждения файла должны давать ошибку или частичный результат, но не панику и не зависание.
func TestReadSheetsCorruptedInputsDoNotPanic(t *testing.T) {
	t.Parallel()

	orig := loadSample(t)
	rng := rand.New(rand.NewSource(1))
	deadline := time.Now().Add(20 * time.Second)

	try := func(label string, data []byte) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s: panic: %v", label, r)
			}
		}()
		_, _ = ReadSheets(bytes.NewReader(data))
	}

	// Обрезка на разных длинах.
	for n := 0; n < len(orig); n += 37 {
		try("truncate", orig[:n])
	}
	// Случайные порчи байтов.
	for i := 0; i < 3000 && time.Now().Before(deadline); i++ {
		data := append([]byte(nil), orig...)
		for k := rng.Intn(8) + 1; k > 0; k-- {
			data[rng.Intn(len(data))] = byte(rng.Intn(256))
		}
		try("mutate", data)
	}
	// Порча заголовка OLE целиком.
	for i := 0; i < 500; i++ {
		data := append([]byte(nil), orig...)
		for off := 8; off < 512; off++ {
			if rng.Intn(10) == 0 {
				data[off] = byte(rng.Intn(256))
			}
		}
		try("header", data)
	}
}

func FuzzReadSheets(f *testing.F) {
	f.Add(loadSample(f))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadSheets(bytes.NewReader(data))
	})
}
