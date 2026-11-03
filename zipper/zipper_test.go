package zipper

import (
	"archive/zip"
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- helpers ---

// newDirs returns a source directory (files to pack) and a path for the
// resulting archive located in a separate directory, as in production.
func newDirs(t *testing.T) (srcDir, zipPath string) {
	t.Helper()
	srcDir = t.TempDir()
	zipPath = filepath.Join(t.TempDir(), "test.zip")
	return srcDir, zipPath
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("failed to create file %s: %v", p, err)
	}
	return p
}

// readZip opens the archive and returns a map: entry name -> content.
// It also checks that entry names are unique.
func readZip(t *testing.T, zipPath string) map[string]string {
	t.Helper()

	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		t.Fatalf("failed to open ZIP file: %v", err)
	}
	defer zr.Close()

	return entriesToMap(t, &zr.Reader)
}

func entriesToMap(t *testing.T, zr *zip.Reader) map[string]string {
	t.Helper()

	result := make(map[string]string, len(zr.File))
	for _, f := range zr.File {
		if _, dup := result[f.Name]; dup {
			t.Errorf("duplicate entry in ZIP archive: %s", f.Name)
		}

		rc, err := f.Open()
		if err != nil {
			t.Fatalf("failed to open entry %s: %v", f.Name, err)
		}
		var buf bytes.Buffer
		_, err = buf.ReadFrom(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("failed to read entry %s: %v", f.Name, err)
		}
		result[f.Name] = buf.String()
	}
	return result
}

// assertEntries compares archive content with the expected name -> content map.
func assertEntries(t *testing.T, got, want map[string]string) {
	t.Helper()

	if len(got) != len(want) {
		t.Errorf("expected %d entries in ZIP archive, got %d (%v)", len(want), len(got), keys(got))
	}
	for name, wantContent := range want {
		gotContent, ok := got[name]
		if !ok {
			t.Errorf("expected entry %q not found in ZIP archive", name)
			continue
		}
		if gotContent != wantContent {
			t.Errorf("entry %q: content mismatch (want len %d, got len %d)", name, len(wantContent), len(gotContent))
		}
	}
	for name := range got {
		if _, ok := want[name]; !ok {
			t.Errorf("unexpected entry in ZIP archive: %q", name)
		}
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// --- ZipFiles ---

// TestZipFilesHappyPath tests successful creation of ZIP archive from directory with files.
func TestZipFilesHappyPath(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	writeFile(t, srcDir, "file1.txt", "Hello, World!")
	writeFile(t, srcDir, "file2.txt", "Another file content")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"file1.txt": "Hello, World!",
		"file2.txt": "Another file content",
	})
}

// TestZipFilesHiddenFiles tests that hidden files (starting with '.') are skipped.
func TestZipFilesHiddenFiles(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	writeFile(t, srcDir, "file1.txt", "Visible content")
	writeFile(t, srcDir, ".gitignore", ".gitignore content")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"file1.txt": "Visible content",
	})
}

// TestZipFilesDirectories tests that subdirectories (and their content) are skipped.
func TestZipFilesDirectories(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	writeFile(t, srcDir, "file1.txt", "Content of file1")

	subDir := filepath.Join(srcDir, "subdir")
	if err := os.Mkdir(subDir, 0o755); err != nil {
		t.Fatalf("failed to create directory %s: %v", subDir, err)
	}
	writeFile(t, subDir, "file2.txt", "Content of file2")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"file1.txt": "Content of file1",
	})
}

// TestZipFilesNestedDirectoriesIgnored tests that ZipFiles is not recursive:
// only files from the top level of the source directory are packed.
func TestZipFilesNestedDirectoriesIgnored(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)

	level4 := filepath.Join(srcDir, "level1", "level2", "level3", "level4")
	if err := os.MkdirAll(level4, 0o755); err != nil {
		t.Fatalf("failed to create nested directories: %v", err)
	}

	writeFile(t, srcDir, "root_file.txt", "Root level file")
	writeFile(t, filepath.Join(srcDir, "level1"), "level1_file.txt", "Level 1 file")
	writeFile(t, level4, "deep_file.txt", "Deep level file")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed for nested directories: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"root_file.txt": "Root level file",
	})
}

// TestZipFilesNonExistentDirectory documents current behavior: a missing source
// directory is not reported as an error, an empty ZIP archive is created.
func TestZipFilesNonExistentDirectory(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	nonExistentPath := filepath.Join(srcDir, "does_not_exist")
	err := ZipFiles(zipPath, nonExistentPath)
	if err == nil {
		t.Fatal("expected error when file path does not exist")
	}

	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist error, got %v", err)
	}
}

// TestZipFilesNonExistentZipFilePath tests error handling when the directory
// of the destination ZIP file does not exist.
func TestZipFilesNonExistentZipFilePath(t *testing.T) {
	t.Parallel()

	srcDir, _ := newDirs(t)
	writeFile(t, srcDir, "file1.txt", "content")
	zipPath := filepath.Join(t.TempDir(), "non_existent_dir", "test.zip")

	err := ZipFiles(zipPath, srcDir)
	if err == nil {
		t.Fatal("expected error when ZIP file path does not exist")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist error, got %v", err)
	}
}

// TestZipFilesEmptyDirectory tests handling of empty directory (no files to zip).
func TestZipFilesEmptyDirectory(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed for empty directory: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{})
}

// TestZipFilesSpecialCharactersInFilename tests files with special characters in names.
func TestZipFilesSpecialCharactersInFilename(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	writeFile(t, srcDir, "file-with_special.chars.txt", "Special filename content")
	writeFile(t, srcDir, "with space (1).txt", "Spaces and brackets")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed for special characters in filename: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"file-with_special.chars.txt": "Special filename content",
		"with space (1).txt":          "Spaces and brackets",
	})
}

// TestZipFilesUnicodeFilename tests files with unicode characters in names.
func TestZipFilesUnicodeFilename(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	name := "файл_文件_αρχείο.txt"
	writeFile(t, srcDir, name, "Unicode filename content")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed for unicode filename: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		name: "Unicode filename content",
	})
}

// TestZipFilesSymlink tests that a symlink to a regular file is packed
// as a regular entry with the content of its target (os.Stat follows links).
func TestZipFilesSymlink(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	targetFile := writeFile(t, srcDir, "target.txt", "Target file content")
	symlinkPath := filepath.Join(srcDir, "link_to_target.txt")

	if err := os.Symlink(targetFile, symlinkPath); err != nil {
		t.Skipf("symlink creation is not supported or not permitted: %v", err)
	}

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed for symlink: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"target.txt":         "Target file content",
		"link_to_target.txt": "Target file content",
	})
}

// TestZipFilesBrokenSymlink tests that a dangling symlink makes ZipFiles fail.
func TestZipFilesBrokenSymlink(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	symlinkPath := filepath.Join(srcDir, "broken_link.txt")

	if err := os.Symlink(filepath.Join(srcDir, "missing.txt"), symlinkPath); err != nil {
		t.Skipf("symlink creation is not supported or not permitted: %v", err)
	}

	err := ZipFiles(zipPath, srcDir)
	if err == nil {
		t.Fatal("expected error for a dangling symlink")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist error, got %v", err)
	}
}

// TestZipFilesFileWithZeroSize tests handling of zero-size files.
func TestZipFilesFileWithZeroSize(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	writeFile(t, srcDir, "empty_file.txt", "")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed for zero-size file: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"empty_file.txt": "",
	})
}

// TestZipFilesOverwritesExistingArchive tests that an existing archive is replaced.
func TestZipFilesOverwritesExistingArchive(t *testing.T) {
	t.Parallel()

	srcDir, zipPath := newDirs(t)
	writeFile(t, filepath.Dir(zipPath), filepath.Base(zipPath), "old garbage")
	writeFile(t, srcDir, "file1.txt", "new content")

	if err := ZipFiles(zipPath, srcDir); err != nil {
		t.Fatalf("ZipFiles failed: %v", err)
	}

	assertEntries(t, readZip(t, zipPath), map[string]string{
		"file1.txt": "new content",
	})
}

// TestZipFilesTableDriven tests multiple scenarios using table-driven approach.
func TestZipFilesTableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		files map[string]string // files created in the source directory
		dirs  []string          // subdirectories created in the source directory
		want  map[string]string // expected entries in the archive
	}{
		{
			name:  "regular files",
			files: map[string]string{"file1.txt": "Content of file1", "file2.txt": "Content of file2"},
			want:  map[string]string{"file1.txt": "Content of file1", "file2.txt": "Content of file2"},
		},
		{
			name:  "hidden file only",
			files: map[string]string{".gitignore": "ignored"},
			want:  map[string]string{},
		},
		{
			name: "mixed regular and hidden files",
			files: map[string]string{
				"file1.txt":  "Content of file1",
				".gitignore": "ignored",
				".env":       "SECRET=1",
			},
			want: map[string]string{"file1.txt": "Content of file1"},
		},
		{
			name:  "only subdirectories",
			dirs:  []string{"a", "b"},
			files: map[string]string{},
			want:  map[string]string{},
		},
		{
			name:  "file with dot inside name is not hidden",
			files: map[string]string{"archive.tar.gz": "data", "a.b.c": "abc"},
			want:  map[string]string{"archive.tar.gz": "data", "a.b.c": "abc"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			srcDir, zipPath := newDirs(t)

			for name, content := range tt.files {
				writeFile(t, srcDir, name, content)
			}
			for _, d := range tt.dirs {
				if err := os.Mkdir(filepath.Join(srcDir, d), 0o755); err != nil {
					t.Fatalf("failed to create directory %s: %v", d, err)
				}
			}

			if err := ZipFiles(zipPath, srcDir); err != nil {
				t.Fatalf("ZipFiles failed: %v", err)
			}

			assertEntries(t, readZip(t, zipPath), tt.want)
		})
	}
}

// --- addFileToZip ---

// zipOneFile adds filePath to an in-memory archive via addFileToZip
// and returns the opened archive.
func zipOneFile(t *testing.T, filePath string) *zip.Reader {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	if err := addFileToZip(zw, filePath); err != nil {
		t.Fatalf("addFileToZip failed: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("failed to close zip writer: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("failed to read in-memory ZIP: %v", err)
	}
	return zr
}

// TestAddFileToZipHappyPath tests successful addition of a file to ZIP writer.
func TestAddFileToZipHappyPath(t *testing.T) {
	t.Parallel()

	filePath := writeFile(t, t.TempDir(), "test.txt", "Test content for addFileToZip")

	zr := zipOneFile(t, filePath)

	assertEntries(t, entriesToMap(t, zr), map[string]string{
		"test.txt": "Test content for addFileToZip",
	})
}

// TestAddFileToZipEntryNameIsBaseName tests that the entry name has no directory part.
func TestAddFileToZipEntryNameIsBaseName(t *testing.T) {
	t.Parallel()

	filePath := writeFile(t, t.TempDir(), "test.txt", "content")

	zr := zipOneFile(t, filePath)

	if len(zr.File) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(zr.File))
	}
	if name := zr.File[0].Name; name != "test.txt" || strings.ContainsAny(name, `/\`) {
		t.Errorf("expected entry name %q without path, got %q", "test.txt", name)
	}
}

// TestAddFileToZipNonExistentFile tests error handling when source file does not exist.
func TestAddFileToZipNonExistentFile(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	defer zw.Close()

	err := addFileToZip(zw, filepath.Join(t.TempDir(), "non_existent_file.txt"))
	if err == nil {
		t.Fatal("expected error when source file does not exist")
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("expected fs.ErrNotExist error, got %v", err)
	}
}

// TestAddFileToZipValidCompressionMethod tests that Deflate compression method is applied.
func TestAddFileToZipValidCompressionMethod(t *testing.T) {
	t.Parallel()

	filePath := writeFile(t, t.TempDir(), "test.txt", "Test content")

	zr := zipOneFile(t, filePath)

	if len(zr.File) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(zr.File))
	}
	if m := zr.File[0].Method; m != zip.Deflate {
		t.Errorf("expected Deflate compression method, got %v", m)
	}
}

// TestAddFileToZipLargeFile tests integrity of a multi-megabyte file.
func TestAddFileToZipLargeFile(t *testing.T) {
	t.Parallel()

	const size = 5 << 20 // 5 MiB
	largeContent := strings.Repeat("This is test content for large file verification. ", size/50+1)[:size]
	filePath := writeFile(t, t.TempDir(), "large_file.txt", largeContent)

	zr := zipOneFile(t, filePath)

	if len(zr.File) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(zr.File))
	}
	entry := zr.File[0]
	if entry.Name != "large_file.txt" {
		t.Fatalf("unexpected entry name: %s", entry.Name)
	}
	if entry.UncompressedSize64 != size {
		t.Errorf("expected uncompressed size %d, got %d", size, entry.UncompressedSize64)
	}
	if entry.CompressedSize64 >= entry.UncompressedSize64 {
		t.Errorf("expected repetitive content to compress: compressed %d, uncompressed %d",
			entry.CompressedSize64, entry.UncompressedSize64)
	}

	got := entriesToMap(t, zr)
	if got["large_file.txt"] != largeContent {
		t.Errorf("file content mismatch. Expected length %d, got %d", len(largeContent), len(got["large_file.txt"]))
	}
}
