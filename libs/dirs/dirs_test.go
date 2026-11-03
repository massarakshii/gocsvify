package dirs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCreateDirsCreatesNested(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	list := []string{
		filepath.Join(root, "a"),
		filepath.Join(root, "b", "c", "d"),
		filepath.Join(root, "папка с пробелами"),
	}
	if err := CreateDirs(list); err != nil {
		t.Fatalf("CreateDirs: %v", err)
	}
	for _, d := range list {
		if st, err := os.Stat(d); err != nil || !st.IsDir() {
			t.Errorf("%s: not created (%v)", d, err)
		}
	}
}

func TestCreateDirsIdempotentAndKeepsContent(t *testing.T) {
	t.Parallel()

	d := filepath.Join(t.TempDir(), "x")
	if err := CreateDirs([]string{d}); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(d, "keep.txt")
	if err := os.WriteFile(f, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CreateDirs([]string{d, d}); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if _, err := os.Stat(f); err != nil {
		t.Errorf("existing content must survive: %v", err)
	}
}

func TestCreateDirsEmptyList(t *testing.T) {
	t.Parallel()
	if err := CreateDirs(nil); err != nil {
		t.Fatal(err)
	}
}

func TestCreateDirsFailsWhenParentIsFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CreateDirs([]string{filepath.Join(file, "sub")}); err == nil {
		t.Fatal("expected error: parent is a regular file")
	}
}

// Если на месте директории лежит обычный файл, это ошибка (раньше Stat-ошибка молча игнорировалась).
func TestCreateDirsPathIsFile(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CreateDirs([]string{file}); err == nil {
		t.Fatal("expected error: path is a regular file")
	}
}

func TestCreateDirsStopsAtFirstError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, "file")
	_ = os.WriteFile(file, nil, 0o644)
	later := filepath.Join(root, "later")
	err := CreateDirs([]string{filepath.Join(file, "bad"), later})
	if err == nil {
		t.Fatal("expected error")
	}
	if _, statErr := os.Stat(later); statErr == nil {
		t.Error("directories after the failing one must not be created")
	}
}
