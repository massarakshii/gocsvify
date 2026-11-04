package accdb

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gocsvify/settings"
)

func convert(t *testing.T, content []byte, delimiter string) (string, error) {
	t.Helper()
	src := filepath.Join(t.TempDir(), "db.accdb")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	c := NewConverter(context.Background(), src, &settings.Settings{Delimiter: delimiter})
	return out, c.Convert(out)
}

func TestConvertRejectsNonAccessFiles(t *testing.T) {
	t.Parallel()

	for name, data := range map[string][]byte{
		"empty": {},
		"text":  bytes.Repeat([]byte("hello "), 1000),
		"zeros": make([]byte, 8192),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			out, err := convert(t, data, ";")
			if err == nil {
				t.Fatal("expected error")
			}
			if entries, _ := os.ReadDir(out); len(entries) != 0 {
				t.Errorf("no csv expected, got %d files", len(entries))
			}
		})
	}
}

func TestConvertMissingFile(t *testing.T) {
	t.Parallel()

	c := NewConverter(context.Background(), filepath.Join(t.TempDir(), "no.accdb"), &settings.Settings{})
	if err := c.Convert(t.TempDir()); err == nil {
		t.Fatal("expected error")
	}
}

func TestNewConverterFields(t *testing.T) {
	t.Parallel()

	s := &settings.Settings{Delimiter: ","}
	c := NewConverter(context.Background(), "x.accdb", s)
	if c.FilePath != "x.accdb" || c.Settings != s || c.Ctx == nil {
		t.Errorf("unexpected converter: %+v", c)
	}
}

const sample = "testdata/sample.accdb"

func TestConvertSample(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	c := NewConverter(context.Background(), sample, &settings.Settings{Delimiter: ";"})
	if err := c.Convert(out); err != nil {
		t.Fatalf("Convert: %v", err)
	}
	// По одному csv на таблицу (в алфавитном порядке), в каждом - заголовок.
	want := []string{
		"КодДетали;Название;Цвет;Вес;Эскиз\n",
		"КодПоставщика;Поставщик;Город;Рейтинг\n", // Копия "Поставщики"
		"КодПоставщика;КодДетали;Дата;Количество\n",
		"КодПоставщика;Поставщик;Город;Рейтинг\n",
		"Код\n",
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != len(want) {
		t.Fatalf("got %d files, want %d", len(entries), len(want))
	}
	for i, w := range want {
		got, err := os.ReadFile(filepath.Join(out, strconv.Itoa(i)+".csv"))
		if err != nil || string(got) != w {
			t.Errorf("%d.csv = %q (%v), want %q", i, got, err, w)
		}
	}
}

func TestConvertSampleDelimiters(t *testing.T) {
	t.Parallel()

	for delim, want := range map[string]string{",": "Код\n", "": "Код\n"} {
		out := t.TempDir()
		c := NewConverter(context.Background(), sample, &settings.Settings{Delimiter: delim})
		if err := c.Convert(out); err != nil {
			t.Fatalf("delim %q: %v", delim, err)
		}
		if got, _ := os.ReadFile(filepath.Join(out, "4.csv")); string(got) != want {
			t.Errorf("delim %q: 4.csv = %q", delim, got)
		}
	}
	// Разделитель по умолчанию (пустая строка) - «;».
	out := t.TempDir()
	if err := NewConverter(context.Background(), sample, &settings.Settings{}).Convert(out); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(out, "0.csv")); string(got) != "КодДетали;Название;Цвет;Вес;Эскиз\n" {
		t.Errorf("default delimiter: %q", got)
	}
}

func TestConvertSampleOutputDirMissing(t *testing.T) {
	t.Parallel()

	c := NewConverter(context.Background(), sample, &settings.Settings{Delimiter: ";"})
	if err := c.Convert(filepath.Join(t.TempDir(), "no", "dir")); err == nil {
		t.Fatal("expected error")
	}
}

func TestConvertNorthwind(t *testing.T) {
	t.Parallel()

	out := t.TempDir()
	c := NewConverter(context.Background(), "testdata/northwind.accdb", &settings.Settings{Delimiter: ";"})
	if err := c.Convert(out); err != nil {
		t.Fatalf("Convert: %v", err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 20 {
		t.Fatalf("got %d csv files, want 20 (по одному на таблицу)", len(entries))
	}
	// Таблицы идут по алфавиту: 0 - Customers (29 строк + заголовок), 12 - Products (45 + заголовок).
	for idx, wantLines := range map[int]int{0: 30, 12: 46} {
		b, err := os.ReadFile(filepath.Join(out, strconv.Itoa(idx)+".csv"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Count(string(b), "\n"); got != wantLines {
			t.Errorf("%d.csv: %d lines, want %d", idx, got, wantLines)
		}
	}
}

func TestConvertNorthwindCanceledContextDoesNotHang(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Нативный ридер не прерывается по контексту, но обязан корректно завершиться.
	_ = NewConverter(ctx, "testdata/northwind.accdb", &settings.Settings{Delimiter: ";"}).Convert(t.TempDir())
}
