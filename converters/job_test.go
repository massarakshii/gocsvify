package converters

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	convertSettings "xls_converter/settings"
	"xls_converter/store"
)

// --- fake MessageStore ---

type fakeStore struct {
	mu       sync.Mutex
	msgs     map[string]store.Message
	getErr   func(key string) error
	saveErr  func(m *store.Message) error
	deleted  []string
	statuses map[string][]string // история статусов по ключу
}

func newFakeStore() *fakeStore {
	return &fakeStore{msgs: map[string]store.Message{}, statuses: map[string][]string{}}
}

func (f *fakeStore) GetMessage(key string) (*store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		if err := f.getErr(key); err != nil {
			return nil, err
		}
	}
	m, ok := f.msgs[key]
	if !ok {
		return nil, nil
	}
	return &m, nil
}

func (f *fakeStore) SaveMessage(m *store.Message) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.saveErr != nil {
		if err := f.saveErr(m); err != nil {
			return err
		}
	}
	f.msgs[m.Key] = *m
	f.statuses[m.Key] = append(f.statuses[m.Key], m.Status)
	return nil
}

func (f *fakeStore) DeleteMessage(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.msgs, key)
	f.deleted = append(f.deleted, key)
	return nil
}

func (f *fakeStore) get(key string) (store.Message, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.msgs[key]
	return m, ok
}

// --- helpers ---

type dirsSet struct{ root, in, out, history, tmp, archives, failed string }

func newJob(t *testing.T, ms store.MessageStore, mutate func(*convertSettings.Settings)) (*Job, dirsSet) {
	t.Helper()
	root := t.TempDir()
	d := dirsSet{
		root:     root,
		in:       filepath.Join(root, "in"),
		out:      filepath.Join(root, "out"),
		history:  filepath.Join(root, "history"),
		tmp:      filepath.Join(root, "app", "tmp"),
		archives: filepath.Join(root, "archives"),
		failed:   filepath.Join(root, "failed"),
	}
	s := convertSettings.Settings{
		DirPathApp:      filepath.Join(root, "app"),
		DirPathIn:       d.in,
		DirPathOut:      d.out,
		DirPathHistory:  d.history,
		DirPathArchives: d.archives,
		DirPathFailed:   d.failed,
		IsAbsPath:       true,
		Sleep:           5_000_000,   // 5ms
		IdleTimeout:     150_000_000, // 150ms: Run завершается сам
		LifeTime:        3_600_000_000_000,
		LifeTimeHistory: 3_600_000_000_000,
		Delimiter:       ";",
		MaxUploadSize:   10,
	}
	if mutate != nil {
		mutate(&s)
	}
	return NewJob("prod", s, ms), d
}

func minimalXlsx(t *testing.T, value string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("xl/worksheets/sheet1.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`<?xml version="1.0"?><worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
		`<dimension ref="A1"/><sheetData><row><c r="A1"><v>` + value + `</v></c></row></sheetData></worksheet>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func put(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func exists(p string) bool { _, err := os.Stat(p); return err == nil }

func runJob(t *testing.T, j *Job) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- j.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("Run did not stop by idle timeout")
	}
}

// --- Run: основные сценарии ---

func TestRunConvertsFile(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	put(t, d.in, "key1.xlsx", minimalXlsx(t, "42"))

	runJob(t, j)

	if got, err := os.ReadFile(filepath.Join(d.out, "key1", "0.csv")); err != nil || string(got) != "42\n" {
		t.Fatalf("csv = %q, %v", got, err)
	}
	if !exists(filepath.Join(d.history, "key1.xlsx")) {
		t.Error("source must be moved to history")
	}
	if exists(filepath.Join(d.in, "key1.xlsx")) {
		t.Error("source must be removed from in")
	}
	m, ok := ms.get("key1")
	if !ok || m.Status != store.StatusDone {
		t.Fatalf("message = %+v (found=%v), want done", m, ok)
	}
	if m.Filename != "key1.xlsx" {
		t.Errorf("filename = %q", m.Filename)
	}
	// Статусы шли new -> processing -> done.
	st := ms.statuses["key1"]
	if len(st) < 3 || st[0] != store.StatusNew || st[len(st)-2] != store.StatusProcessing || st[len(st)-1] != store.StatusDone {
		t.Errorf("status history = %v", st)
	}
	if entries, _ := os.ReadDir(d.tmp); len(entries) != 0 {
		t.Errorf("tmp must be cleaned, found %d entries", len(entries))
	}
}

func TestRunConvertsManyFilesInParallel(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	const n = 30
	for i := range n {
		put(t, d.in, "k"+string(rune('a'+i%26))+string(rune('a'+i/26))+".xlsx", minimalXlsx(t, "1"))
	}

	runJob(t, j)

	entries, _ := os.ReadDir(d.out)
	if len(entries) != n {
		t.Errorf("converted %d files, want %d", len(entries), n)
	}
	for k, m := range ms.msgs {
		if m.Status != store.StatusDone {
			t.Errorf("%s: status %s", k, m.Status)
		}
	}
}

func TestRunBrokenFileGoesToFailed(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	put(t, d.in, "bad.xlsx", []byte("this is not a zip"))
	put(t, d.in, "good.xlsx", minimalXlsx(t, "1"))

	runJob(t, j)

	if m, _ := ms.get("bad"); m.Status != store.StatusError {
		t.Errorf("bad: status %q, want error", m.Status)
	}
	if !exists(filepath.Join(d.failed, "bad.xlsx")) {
		t.Error("bad file must be moved to failed")
	}
	if exists(filepath.Join(d.out, "bad")) {
		t.Error("no output expected for a broken file")
	}
	// Один сломанный файл не мешает остальным.
	if m, _ := ms.get("good"); m.Status != store.StatusDone {
		t.Errorf("good: status %q, want done", m.Status)
	}
}

func TestRunBrokenXlsAndAccdb(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	put(t, d.in, "a.xls", bytes.Repeat([]byte("x"), 2000))
	put(t, d.in, "b.accdb", bytes.Repeat([]byte("x"), 2000))
	put(t, d.in, "c.mdb", []byte{})

	runJob(t, j)

	for _, name := range []string{"a.xls", "b.accdb", "c.mdb"} {
		if !exists(filepath.Join(d.failed, name)) {
			t.Errorf("%s must be in failed", name)
		}
	}
	for _, k := range []string{"a", "b", "c"} {
		if m, _ := ms.get(k); m.Status != store.StatusError {
			t.Errorf("%s: status %q, want error", k, m.Status)
		}
	}
}

func TestRunUnsupportedExtension(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	put(t, d.in, "note.txt", []byte("hi"))

	runJob(t, j)

	if m, _ := ms.get("note"); m.Status != store.StatusError {
		t.Errorf("status %q, want error", m.Status)
	}
	if !exists(filepath.Join(d.failed, "note.txt")) {
		t.Error("file must be moved to failed")
	}
}

func TestRunUppercaseExtension(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	put(t, d.in, "BOOK.XLSX", minimalXlsx(t, "5"))

	runJob(t, j)

	if m, _ := ms.get("BOOK"); m.Status != store.StatusDone {
		t.Errorf("status %q, want done", m.Status)
	}
}

func TestRunSkipsHiddenFilesAndDirs(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	put(t, d.in, ".hidden.xlsx", minimalXlsx(t, "1"))
	if err := os.MkdirAll(filepath.Join(d.in, "subdir"), 0o755); err != nil {
		t.Fatal(err)
	}

	runJob(t, j)

	if len(ms.msgs) != 0 {
		t.Errorf("nothing must be processed, got %v", ms.msgs)
	}
	if !exists(filepath.Join(d.in, ".hidden.xlsx")) || !exists(filepath.Join(d.in, "subdir")) {
		t.Error("hidden files and directories must be left in place")
	}
}

// Автономный режим: ключ - имя файла без расширения, исходное имя сохраняется в сообщении.
func TestRunAutonomousKeyIsFilenameWithoutExtension(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, nil)
	put(t, d.in, "Отчёт март.xlsx", minimalXlsx(t, "1"))

	runJob(t, j)

	m, ok := ms.get("Отчёт март")
	if !ok || m.Filename != "Отчёт март.xlsx" || m.Status != store.StatusDone {
		t.Fatalf("message = %+v (found=%v)", m, ok)
	}
	if !exists(filepath.Join(d.out, "Отчёт март", "0.csv")) {
		t.Error("output must be in out/<name without extension>")
	}
}

// Повторная загрузка файла с тем же ключом: статус сбрасывается и результат заменяется.
func TestRunReprocessesExistingMessageAndReplacesOutput(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	ms.msgs["k"] = store.Message{Key: "k", Filename: "old.xlsx", Status: store.StatusDone, Message: "old"}
	j, d := newJob(t, ms, nil)
	put(t, filepath.Join(d.out, "k"), "stale.csv", []byte("stale"))
	put(t, d.in, "k.xlsx", minimalXlsx(t, "9"))

	runJob(t, j)

	if exists(filepath.Join(d.out, "k", "stale.csv")) {
		t.Error("previous result must be replaced")
	}
	if got, _ := os.ReadFile(filepath.Join(d.out, "k", "0.csv")); string(got) != "9\n" {
		t.Errorf("csv = %q", got)
	}
	m, _ := ms.get("k")
	if m.Status != store.StatusDone || m.Filename != "k.xlsx" {
		t.Errorf("message = %+v", m)
	}
}

// Файл, оставшийся в in после «processing» (сбой при прошлом запуске), обрабатывается заново.
func TestRunPicksUpStaleProcessingMessage(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	ms.msgs["k"] = store.Message{Key: "k", Filename: "k.xlsx", Status: store.StatusProcessing}
	j, d := newJob(t, ms, nil)
	put(t, d.in, "k.xlsx", minimalXlsx(t, "3"))

	runJob(t, j)

	if m, _ := ms.get("k"); m.Status != store.StatusDone {
		t.Errorf("status %q, want done", m.Status)
	}
}

// --- ошибки хранилища ---

func TestRunGetMessageErrorMovesFileToFailed(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	ms.getErr = func(key string) error { return errors.New("db down") }
	j, d := newJob(t, ms, nil)
	put(t, d.in, "k.xlsx", minimalXlsx(t, "1"))

	runJob(t, j)

	if !exists(filepath.Join(d.failed, "k.xlsx")) {
		t.Error("file must be moved to failed when the store is unavailable")
	}
	if exists(filepath.Join(d.out, "k")) {
		t.Error("must not convert without a message")
	}
}

func TestRunSaveNewMessageErrorMovesFileToFailed(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	ms.saveErr = func(m *store.Message) error { return errors.New("disk full") }
	j, d := newJob(t, ms, nil)
	put(t, d.in, "k.xlsx", minimalXlsx(t, "1"))

	runJob(t, j)

	if !exists(filepath.Join(d.failed, "k.xlsx")) {
		t.Error("file must be moved to failed when the message cannot be saved")
	}
}

// Сбой сохранения статуса не должен ломать саму конвертацию.
func TestRunStatusSaveFailureDoesNotBreakConversion(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	calls := 0
	ms.saveErr = func(m *store.Message) error {
		calls++
		if calls > 1 { // первое сохранение (new) проходит, остальные - нет
			return errors.New("flaky")
		}
		return nil
	}
	j, d := newJob(t, ms, nil)
	put(t, d.in, "k.xlsx", minimalXlsx(t, "7"))

	runJob(t, j)

	if got, _ := os.ReadFile(filepath.Join(d.out, "k", "0.csv")); string(got) != "7\n" {
		t.Errorf("csv = %q", got)
	}
}

// --- остановка ---

func TestRunStopsOnContextCancel(t *testing.T) {
	t.Parallel()

	j, _ := newJob(t, newFakeStore(), func(s *convertSettings.Settings) {
		s.IdleTimeout = 0 // сам не остановится
		s.Sleep = 10_000_000
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- j.Run(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run returned %v on cancel, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after cancel")
	}
}

func TestRunWithCanceledContextLeavesFilesNew(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	j, d := newJob(t, ms, func(s *convertSettings.Settings) { s.IdleTimeout = 0 })
	put(t, d.in, "k.xlsx", minimalXlsx(t, "1"))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() { _ = j.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
	}

	// Файл не потерян: либо остался в in, либо (если успел) конвертирован.
	if !exists(filepath.Join(d.in, "k.xlsx")) && !exists(filepath.Join(d.history, "k.xlsx")) {
		t.Error("source file was lost")
	}
	if m, ok := ms.get("k"); ok && m.Status == store.StatusProcessing {
		t.Errorf("status must not stay processing after cancel: %+v", m)
	}
}

func TestRunStopsByIdleTimeout(t *testing.T) {
	t.Parallel()

	j, _ := newJob(t, newFakeStore(), nil)
	start := time.Now()
	runJob(t, j)
	if el := time.Since(start); el < 100*time.Millisecond || el > 5*time.Second {
		t.Errorf("stopped after %v, want about 150ms", el)
	}
}

func TestRunCreatesDirs(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	runJob(t, j)
	for _, p := range []string{d.in, d.out, d.history, d.tmp, d.archives, d.failed} {
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			t.Errorf("%s was not created (%v)", p, err)
		}
	}
}

func TestRunFailsWhenDirCannotBeCreated(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	// На месте in лежит файл.
	put(t, d.root, "in", []byte("x"))
	if err := j.Run(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

// --- очистка ---

func TestRunClearsOldEntries(t *testing.T) {
	t.Parallel()

	ms := newFakeStore()
	ms.msgs["oldkey"] = store.Message{Key: "oldkey", Filename: "oldkey.xlsx", Status: store.StatusDone}
	ms.msgs["newkey"] = store.Message{Key: "newkey", Filename: "newkey.xlsx", Status: store.StatusDone}
	j, d := newJob(t, ms, func(s *convertSettings.Settings) {
		s.LifeTime = 1_000_000_000_000 // 1000с
		s.LifeTimeHistory = 1_000_000_000_000
	})

	old := time.Now().Add(-2 * time.Hour)
	// out: старая и свежая директории результата.
	put(t, filepath.Join(d.out, "oldkey"), "0.csv", []byte("1"))
	put(t, filepath.Join(d.out, "newkey"), "0.csv", []byte("1"))
	// archives, history, tmp: по старому файлу.
	oldArchive := put(t, d.archives, "oldkey.zip", []byte("z"))
	oldHistory := put(t, d.history, "oldkey.xlsx", []byte("h"))
	freshHistory := put(t, d.history, "fresh.xlsx", []byte("h"))
	oldTmp := put(t, d.tmp, "leftover", []byte("t"))
	hiddenOld := put(t, d.history, ".keep", []byte("k"))
	for _, p := range []string{filepath.Join(d.out, "oldkey"), oldArchive, oldHistory, oldTmp, hiddenOld} {
		if err := os.Chtimes(p, old, old); err != nil {
			t.Fatal(err)
		}
	}

	runJob(t, j)

	for _, p := range []string{filepath.Join(d.out, "oldkey"), oldArchive, oldHistory, oldTmp} {
		if exists(p) {
			t.Errorf("%s must be removed", p)
		}
	}
	for _, p := range []string{filepath.Join(d.out, "newkey"), freshHistory, hiddenOld} {
		if !exists(p) {
			t.Errorf("%s must be kept", p)
		}
	}
	if _, ok := ms.get("oldkey"); ok {
		t.Error("message of an expired entry must be deleted")
	}
	if _, ok := ms.get("newkey"); !ok {
		t.Error("message of a fresh entry must stay")
	}
}

func TestClearOldMissingDirDoesNotPanic(t *testing.T) {
	t.Parallel()
	j, d := newJob(t, newFakeStore(), nil)
	j.clearOld(filepath.Join(d.root, "nope"), time.Hour)
}

func TestClearOldZeroLifetimeRemovesEverythingNonHidden(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	a := put(t, d.out, "a", []byte("1"))
	h := put(t, d.out, ".h", []byte("1"))
	time.Sleep(5 * time.Millisecond)
	j.clearOld(d.out, 0)
	if exists(a) {
		t.Error("a must be removed with zero lifetime")
	}
	if !exists(h) {
		t.Error("hidden file must stay")
	}
}

// --- hasFiles ---

func TestHasFiles(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	dir := t.TempDir()
	if ok, err := j.hasFiles(dir); err != nil || ok {
		t.Errorf("empty: %v, %v", ok, err)
	}
	put(t, dir, ".DS_Store", nil)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, err := j.hasFiles(dir); err != nil || ok {
		t.Errorf("only hidden file and subdir: %v, %v; want false", ok, err)
	}
	put(t, dir, "f.xlsx", nil)
	if ok, err := j.hasFiles(dir); err != nil || !ok {
		t.Errorf("with a file: %v, %v; want true", ok, err)
	}
	if _, err := j.hasFiles(filepath.Join(d.root, "missing")); err == nil {
		t.Error("missing dir: expected error")
	}
}

// --- convertOne / convertSafe ---

func TestConvertOneSuccessAndPaths(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	for _, p := range []string{d.in, d.out, d.history, d.tmp} {
		_ = os.MkdirAll(p, 0o755)
	}
	src := put(t, d.in, "k.xlsx", minimalXlsx(t, "8"))

	msg, err := j.convertOne(context.Background(), ConvertTask{filepath: src, fileName: "k.xlsx", message: store.NewMessageWithKey("k.xlsx", "k")}, d.out, d.history, d.tmp)
	if err != nil {
		t.Fatalf("convertOne: %v (%s)", err, msg)
	}
	if !exists(filepath.Join(d.out, "k", "0.csv")) || !exists(filepath.Join(d.history, "k.xlsx")) {
		t.Error("expected output and history entries")
	}
}

func TestConvertOneHistoryMoveFails(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	for _, p := range []string{d.in, d.out, d.tmp} {
		_ = os.MkdirAll(p, 0o755)
	}
	src := put(t, d.in, "k.xlsx", minimalXlsx(t, "8"))

	// history не существует -> Rename исходника не удаётся.
	_, err := j.convertOne(context.Background(), ConvertTask{filepath: src, fileName: "k.xlsx"}, d.out, filepath.Join(d.root, "no-history"), d.tmp)
	if err == nil {
		t.Fatal("expected error when history dir is missing")
	}
}

func TestConvertOneConversionErrorLeavesNoOutput(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	for _, p := range []string{d.in, d.out, d.history, d.tmp} {
		_ = os.MkdirAll(p, 0o755)
	}
	// Предыдущий результат не должен затираться, если новая конвертация упала.
	put(t, filepath.Join(d.out, "k"), "0.csv", []byte("previous"))
	src := put(t, d.in, "k.xlsx", []byte("garbage"))

	if _, err := j.convertOne(context.Background(), ConvertTask{filepath: src, fileName: "k.xlsx"}, d.out, d.history, d.tmp); err == nil {
		t.Fatal("expected error")
	}
	if got, _ := os.ReadFile(filepath.Join(d.out, "k", "0.csv")); string(got) != "previous" {
		t.Errorf("previous result was damaged: %q", got)
	}
	if entries, _ := os.ReadDir(d.tmp); len(entries) != 0 {
		t.Errorf("tmp must be cleaned, found %d", len(entries))
	}
}

func TestConvertSafeRecoversFromPanic(t *testing.T) {
	t.Parallel()

	j, d := newJob(t, newFakeStore(), nil)
	//nolint:staticcheck // nil-контекст специально вызывает панику внутри context.WithTimeout
	msg, err := j.convertSafe(nil, ConvertTask{filepath: "x.xlsx", fileName: "x.xlsx"}, d.out, d.history, d.tmp)
	if err == nil {
		t.Fatal("expected an error from the recovered panic")
	}
	if msg == "" {
		t.Error("log message must be set")
	}
}
