package xls

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"xls_converter/settings"
)

// testdata/sample.xls: лист «Лист1» (4 колонки, 4 строки, в т.ч. с ;, " и переводом строки)
// и лист «Second» со строкой 1,2,3. Создан в LibreOffice из xlsx.
const sample = "testdata/sample.xls"

func convert(t *testing.T, src, delimiter string) (string, error) {
	t.Helper()
	out := t.TempDir()
	c := NewConverter(context.Background(), src, &settings.Settings{Delimiter: delimiter})
	return out, c.Convert(out)
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestConvertSample(t *testing.T) {
	t.Parallel()

	out, err := convert(t, sample, ";")
	if err != nil {
		t.Fatalf("Convert: %v", err)
	}
	want0 := "Имя;Сумма;Дата;Флаг\n" +
		"Анна;30.5;2023-03-15;TRUE\n" +
		"Борис;7;2023-03-15 12:30:00;\n" +
		"\"a;b\";\"c\"\"d\";\"line\nbreak\";\n"
	if got := read(t, filepath.Join(out, "0.csv")); got != want0 {
		t.Errorf("0.csv:\n got: %q\nwant: %q", got, want0)
	}
	if got := read(t, filepath.Join(out, "1.csv")); got != "1;2;3\n" {
		t.Errorf("1.csv = %q", got)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 2 {
		t.Errorf("expected exactly 2 csv files, got %d", len(entries))
	}
}

func TestConvertDelimiters(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ delim, want string }{
		{",", "1,2,3\n"},
		{"\t", "1\t2\t3\n"},
		{"|", "1|2|3\n"},
		{"", "1;2;3\n"}, // пустой разделитель -> значение по умолчанию
	} {
		out, err := convert(t, sample, tc.delim)
		if err != nil {
			t.Fatalf("delim %q: %v", tc.delim, err)
		}
		if got := read(t, filepath.Join(out, "1.csv")); got != tc.want {
			t.Errorf("delim %q: got %q, want %q", tc.delim, got, tc.want)
		}
	}
}

func TestConvertCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := t.TempDir()
	err := NewConverter(ctx, sample, &settings.Settings{Delimiter: ";"}).Convert(out)
	if err == nil {
		t.Fatal("expected context error")
	}
	if entries, _ := os.ReadDir(out); len(entries) != 0 {
		t.Errorf("no files expected for a canceled context, got %d", len(entries))
	}
}

func TestConvertOutputDirMissing(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "no", "such", "dir")
	err := NewConverter(context.Background(), sample, &settings.Settings{Delimiter: ";"}).Convert(missing)
	if err == nil {
		t.Fatal("expected error when output dir does not exist")
	}
}

func TestSaveSheetQuoting(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	c := NewConverter(context.Background(), "", &settings.Settings{Delimiter: ","})
	rows := [][]string{{"a,b", "c", ""}, {}, {"только", "кириллица"}}
	if err := c.saveSheet(rows, 3, out); err != nil {
		t.Fatalf("saveSheet: %v", err)
	}
	want := "\"a,b\",c,\n\nтолько,кириллица\n"
	// csv.Writer пропускает пустую запись? нет - пишет пустую строку.
	if got := read(t, filepath.Join(out, "3.csv")); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestConvertMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := convert(t, filepath.Join(t.TempDir(), "nope.xls"), ";"); err == nil {
		t.Fatal("expected error")
	}
}

func TestConvertNotAnXls(t *testing.T) {
	t.Parallel()
	p := filepath.Join(t.TempDir(), "bad.xls")
	if err := os.WriteFile(p, []byte(strings.Repeat("not an xls ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := convert(t, p, ";"); err == nil {
		t.Fatal("expected error")
	}
}
