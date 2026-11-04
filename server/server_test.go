package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"gocsvify/daemon"
	ct "gocsvify/libs/configTime"
	convertSettings "gocsvify/settings"
	"gocsvify/store"
)

// --- fakes ---

type fakeStore struct {
	mu      sync.Mutex
	msgs    map[string]store.Message
	getErr  error
	saveErr error
	deleted []string
}

func newFakeStore() *fakeStore { return &fakeStore{msgs: map[string]store.Message{}} }

func (f *fakeStore) GetMessage(key string) (*store.Message, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getErr != nil {
		return nil, f.getErr
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
		return f.saveErr
	}
	f.msgs[m.Key] = *m
	return nil
}
func (f *fakeStore) DeleteMessage(key string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.msgs, key)
	f.deleted = append(f.deleted, key)
	return nil
}
func (f *fakeStore) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.msgs) }
func (f *fakeStore) only(t *testing.T) store.Message {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.msgs) != 1 {
		t.Fatalf("expected exactly 1 message, got %d: %v", len(f.msgs), f.msgs)
	}
	for _, m := range f.msgs {
		return m
	}
	return store.Message{}
}

type countingJob struct{ runs atomic.Int32 }

func (j *countingJob) Run(ctx context.Context) error {
	j.runs.Add(1)
	<-ctx.Done()
	return nil
}

func convertDuration(sec int) ct.Duration { return ct.Duration(time.Duration(sec) * time.Second) }

// --- harness ---

type env struct {
	ts                          *httptest.Server
	ms                          *fakeStore
	job                         *countingJob
	dm                          *daemon.Manager
	in, out, tmp, archives, app string
}

func setup(t *testing.T, mutate func(*convertSettings.Settings)) *env {
	t.Helper()
	root := t.TempDir()
	e := &env{
		ms: newFakeStore(), job: &countingJob{}, dm: &daemon.Manager{},
		in: filepath.Join(root, "in"), out: filepath.Join(root, "out"),
		archives: filepath.Join(root, "archives"), app: filepath.Join(root, "app"),
	}
	e.tmp = filepath.Join(e.app, "tmp")
	for _, d := range []string{e.in, e.out, e.archives, e.tmp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := convertSettings.Settings{
		DirPathApp: e.app, DirPathIn: e.in, DirPathOut: e.out, DirPathArchives: e.archives,
		IsAbsPath: true, ServerPort: 0, MaxUploadSize: 1, Delimiter: ";",
		ReadTimeout: convertDuration(30), ReadHeadTimeout: convertDuration(30),
	}
	if mutate != nil {
		mutate(&s)
	}
	srv, errCh, err := StartServer(s, "prod", e.ms, e.job, e.dm)
	if err != nil {
		t.Fatalf("StartServer: %v", err)
	}
	e.ts = httptest.NewServer(srv.Handler)
	t.Cleanup(func() {
		e.ts.Close()
		_ = srv.Close()
		<-errCh
		_ = e.dm.Stop(2 * time.Second)
	})
	return e
}

func uploadBody(t *testing.T, field, filename string, content []byte) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="`+field+`"; filename="`+filename+`"`)
	h.Set("Content-Type", "application/octet-stream")
	w, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write(content)
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func (e *env) upload(t *testing.T, filename string, content []byte) (*http.Response, Response) {
	t.Helper()
	body, ct := uploadBody(t, "file", filename, content)
	resp, err := http.Post(e.ts.URL+"/upload", ct, body)
	if err != nil {
		t.Fatalf("POST /upload: %v", err)
	}
	return resp, decode(t, resp)
}

func decode(t *testing.T, resp *http.Response) Response {
	t.Helper()
	defer resp.Body.Close()
	var r Response
	data, _ := io.ReadAll(resp.Body)
	if len(data) > 0 && strings.HasPrefix(resp.Header.Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(data, &r); err != nil {
			t.Fatalf("bad JSON %q: %v", data, err)
		}
	}
	return r
}

func (e *env) get(t *testing.T, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(e.ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

// --- StartServer ---

func TestStartServerConfiguresTimeouts(t *testing.T) {
	t.Parallel()

	s := convertSettings.Settings{
		DirPathApp: t.TempDir(), DirPathIn: "in", DirPathOut: "out", DirPathArchives: "arch",
		IsAbsPath: true, ServerPort: 0,
		ReadTimeout: convertDuration(11), ReadHeadTimeout: convertDuration(7), WriteTimeout: convertDuration(3),
	}
	srv, errCh, err := StartServer(s, "prod", newFakeStore(), &countingJob{}, &daemon.Manager{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close(); <-errCh }()

	if srv.ReadTimeout != 11*time.Second || srv.ReadHeaderTimeout != 7*time.Second || srv.WriteTimeout != 3*time.Second {
		t.Errorf("timeouts: read=%v head=%v write=%v", srv.ReadTimeout, srv.ReadHeaderTimeout, srv.WriteTimeout)
	}
}

// Ошибка запуска (порт занят) приходит через канал, а не через os.Exit.
func TestStartServerReportsListenError(t *testing.T) {
	t.Parallel()

	// Занимаем порт на всех интерфейсах, как это делает сам сервер (":port").
	// 127.0.0.1 здесь не подходит: на macOS/BSD wildcard-bind поверх него проходит.
	first, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, portStr, err := net.SplitHostPort(first.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	p, _ := strconv.Atoi(portStr)

	s := convertSettings.Settings{DirPathApp: t.TempDir(), DirPathIn: "in", DirPathOut: "out", DirPathArchives: "arch", IsAbsPath: true, ServerPort: p}
	srv, errCh, err := StartServer(s, "prod", newFakeStore(), &countingJob{}, &daemon.Manager{})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	select {
	case lerr, ok := <-errCh:
		if !ok || lerr == nil {
			t.Fatalf("expected a listen error, got %v (open=%v)", lerr, ok)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no error reported for an occupied port")
	}
}

func TestStartServerChannelClosesAfterShutdown(t *testing.T) {
	t.Parallel()

	s := convertSettings.Settings{DirPathApp: t.TempDir(), DirPathIn: "in", DirPathOut: "out", DirPathArchives: "arch", IsAbsPath: true}
	srv, errCh, err := StartServer(s, "prod", newFakeStore(), &countingJob{}, &daemon.Manager{})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	select {
	case e, ok := <-errCh:
		if ok && e != nil {
			t.Errorf("graceful shutdown must not be reported as an error: %v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel not closed after shutdown")
	}
}

// --- /upload ---

func TestUploadHappyPath(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	content := []byte("PK-fake-xlsx-content")
	resp, r := e.upload(t, "Отчёт.xlsx", content)
	if resp.StatusCode != http.StatusAccepted || !r.Success {
		t.Fatalf("status %d, resp %+v", resp.StatusCode, r)
	}
	if r.Data == nil || r.Data.Filename != "Отчёт.xlsx" || r.Data.FileKey == "" {
		t.Fatalf("data = %+v", r.Data)
	}

	m := e.ms.only(t)
	if m.Key != r.Data.FileKey || m.Status != store.StatusNew || m.Filename != "Отчёт.xlsx" {
		t.Errorf("message = %+v", m)
	}
	got, err := os.ReadFile(filepath.Join(e.in, m.Key+".xlsx"))
	if err != nil || !bytes.Equal(got, content) {
		t.Errorf("file in 'in': %q, %v", got, err)
	}
	if names := dirNames(t, e.tmp); len(names) != 0 {
		t.Errorf("tmp must be empty, got %v", names)
	}

	// Демон запущен.
	deadline := time.Now().Add(time.Second)
	for e.job.runs.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if e.job.runs.Load() != 1 {
		t.Errorf("job runs = %d, want 1", e.job.runs.Load())
	}
}

func TestUploadAllowedExtensionsCaseInsensitive(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	for _, name := range []string{"a.xls", "b.XLSX", "c.xlsm", "d.accdb", "e.MDB"} {
		if resp, r := e.upload(t, name, []byte("x")); resp.StatusCode != http.StatusAccepted {
			t.Errorf("%s: status %d (%+v)", name, resp.StatusCode, r)
		}
	}
	if e.ms.count() != 5 {
		t.Errorf("saved %d messages, want 5", e.ms.count())
	}
}

func TestUploadRejectsBadNamesAndExtensions(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	for _, name := range []string{"a.txt", "a.exe", "noext", "a.xlsx.exe", ".xlsx2", `back\slash.xlsx`, "..", "."} {
		resp, r := e.upload(t, name, []byte("x"))
		if resp.StatusCode != http.StatusBadRequest || r.Success {
			t.Errorf("%q: status %d, resp %+v; want 400", name, resp.StatusCode, r)
		}
	}
	if e.ms.count() != 0 || len(dirNames(t, e.in)) != 0 {
		t.Error("nothing must be stored for rejected uploads")
	}
}

// Путь в имени файла отбрасывается: файл не может оказаться вне директории in.
func TestUploadPathTraversalInFilename(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	resp, r := e.upload(t, "../../evil.xlsx", []byte("x"))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d (%+v)", resp.StatusCode, r)
	}
	if r.Data.Filename != "evil.xlsx" {
		t.Errorf("filename = %q, want base name", r.Data.Filename)
	}
	names := dirNames(t, e.in)
	if len(names) != 1 || !strings.HasSuffix(names[0], ".xlsx") || strings.Contains(names[0], "evil") {
		t.Errorf("in dir = %v (file name must be the generated key)", names)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(e.in), "evil.xlsx")); err == nil {
		t.Error("file escaped the in directory")
	}
}

func TestUploadMissingFileField(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	body, ct := uploadBody(t, "other", "a.xlsx", []byte("x"))
	resp, err := http.Post(e.ts.URL+"/upload", ct, body)
	if err != nil {
		t.Fatal(err)
	}
	if r := decode(t, resp); resp.StatusCode != http.StatusBadRequest || r.Success {
		t.Errorf("status %d, %+v", resp.StatusCode, r)
	}
}

func TestUploadNotMultipart(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	resp, err := http.Post(e.ts.URL+"/upload", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status %d, want 400", resp.StatusCode)
	}
	resp.Body.Close()
}

func TestUploadMethodNotAllowed(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	if resp := e.get(t, "/upload"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /upload: %d, want 405", resp.StatusCode)
	}
	for _, p := range []string{"/check", "/download"} {
		resp, err := http.Post(e.ts.URL+p, "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusMethodNotAllowed {
			t.Errorf("POST %s: %d, want 405", p, resp.StatusCode)
		}
		resp.Body.Close()
	}
}

// Известный Content-Length больше лимита: отказ сразу, тело не читается.
func TestUploadTooLargeByContentLength(t *testing.T) {
	t.Parallel()
	e := setup(t, nil) // лимит 1 МБ

	big := bytes.Repeat([]byte("a"), 2<<20)
	resp, r := e.upload(t, "big.xlsx", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge || r.Success {
		t.Fatalf("status %d, %+v; want 413", resp.StatusCode, r)
	}
	if e.ms.count() != 0 || len(dirNames(t, e.in)) != 0 || len(dirNames(t, e.tmp)) != 0 {
		t.Error("nothing must be stored for an oversized upload")
	}
}

// chunked (ContentLength = -1): лимит срабатывает через MaxBytesReader, ответ 413 с текстом про МБ.
func TestUploadTooLargeChunked(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	body, ct := uploadBody(t, "file", "big.xlsx", bytes.Repeat([]byte("a"), 2<<20))
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/upload", io.NopCloser(body)) // длина неизвестна
	req.Header.Set("Content-Type", ct)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	r := decode(t, resp)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, %+v; want 413", resp.StatusCode, r)
	}
	if !strings.Contains(r.Message, "1 МБ") {
		t.Errorf("message = %q, want the limit in MB", r.Message)
	}
	if e.ms.count() != 0 || len(dirNames(t, e.in)) != 0 {
		t.Error("nothing must be stored")
	}
}

func TestUploadJustUnderLimit(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	resp, r := e.upload(t, "ok.xlsx", bytes.Repeat([]byte("a"), (1<<20)-1024))
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status %d, %+v", resp.StatusCode, r)
	}
}

func TestUploadSaveMessageFails(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	e.ms.saveErr = errors.New("db down")

	resp, r := e.upload(t, "a.xlsx", []byte("x"))
	if resp.StatusCode != http.StatusUnprocessableEntity || r.Success {
		t.Fatalf("status %d, %+v", resp.StatusCode, r)
	}
	if len(dirNames(t, e.in)) != 0 {
		t.Error("no file expected in 'in'")
	}
}

// tmp недоступен: сообщение, созданное до этого, удаляется, клиент получает ошибку.
func TestUploadTmpDirMissingRollsBackMessage(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	if err := os.RemoveAll(e.tmp); err != nil {
		t.Fatal(err)
	}

	resp, r := e.upload(t, "a.xlsx", []byte("x"))
	if resp.StatusCode != http.StatusUnprocessableEntity || r.Success {
		t.Fatalf("status %d, %+v", resp.StatusCode, r)
	}
	if e.ms.count() != 0 || len(e.ms.deleted) != 1 {
		t.Errorf("message must be rolled back: count=%d deleted=%v", e.ms.count(), e.ms.deleted)
	}
}

func TestUploadInDirMissingRollsBack(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	if err := os.RemoveAll(e.in); err != nil {
		t.Fatal(err)
	}

	resp, r := e.upload(t, "a.xlsx", []byte("x"))
	if resp.StatusCode != http.StatusUnprocessableEntity || r.Success {
		t.Fatalf("status %d, %+v", resp.StatusCode, r)
	}
	if e.ms.count() != 0 {
		t.Error("message must be rolled back")
	}
	if names := dirNames(t, e.tmp); len(names) != 0 {
		t.Errorf("tmp file must be removed, got %v", names)
	}
}

func TestUploadConcurrent(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	var wg sync.WaitGroup
	keys := sync.Map{}
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, r := e.upload(t, "f"+string(rune('a'+i))+".xlsx", []byte{byte(i)})
			if resp.StatusCode != http.StatusAccepted {
				t.Errorf("status %d", resp.StatusCode)
				return
			}
			if _, dup := keys.LoadOrStore(r.Data.FileKey, true); dup {
				t.Errorf("duplicate key %s", r.Data.FileKey)
			}
		}()
	}
	wg.Wait()
	if len(dirNames(t, e.in)) != 20 || e.ms.count() != 20 {
		t.Errorf("in=%d messages=%d, want 20/20", len(dirNames(t, e.in)), e.ms.count())
	}
}

// --- /check ---

func TestCheck(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	e.ms.msgs["k1"] = store.Message{Key: "k1", Filename: "a.xlsx", Status: store.StatusProcessing, Message: "Converting file"}

	resp := e.get(t, "/check?key=k1")
	r := decode(t, resp)
	if resp.StatusCode != http.StatusOK || !r.Success || r.Data == nil ||
		r.Data.Status != store.StatusProcessing || r.Data.Filename != "a.xlsx" || r.Data.FileKey != "k1" || r.Message != "Converting file" {
		t.Errorf("status %d, %+v / %+v", resp.StatusCode, r, r.Data)
	}
}

func TestCheckErrors(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	if resp := e.get(t, "/check"); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("no key: %d, want 400", resp.StatusCode)
	}
	if resp := e.get(t, "/check?key="); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("empty key: %d, want 400", resp.StatusCode)
	}
	if resp := e.get(t, "/check?key=ghost"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown key: %d, want 404", resp.StatusCode)
	}
	e.ms.getErr = errors.New("db down")
	if resp := e.get(t, "/check?key=k"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("store error: %d, want 500", resp.StatusCode)
	}
}

// --- /download ---

func seedDone(t *testing.T, e *env, key, filename string, sheets map[string]string) {
	t.Helper()
	e.ms.msgs[key] = store.Message{Key: key, Filename: filename, Status: store.StatusDone}
	dir := filepath.Join(e.out, key)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range sheets {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestDownloadParamValidation(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	for path, want := range map[string]int{
		"/download":                              http.StatusBadRequest,
		"/download?key=":                         http.StatusBadRequest,
		"/download?key=k&sheetNum=abc":           http.StatusBadRequest,
		"/download?key=k&sheetNum=-1":            http.StatusBadRequest,
		"/download?key=k&sheetNum=1.5":           http.StatusBadRequest,
		"/download?key=k&sheetNum=..%2F0":        http.StatusBadRequest,
		"/download?key=ghost":                    http.StatusNotFound,
		"/download?key=ghost&sheetNum=0":         http.StatusNotFound,
		"/download?key=..%2F..%2Fetc&sheetNum=0": http.StatusNotFound,
	} {
		if resp := e.get(t, path); resp.StatusCode != want {
			t.Errorf("%s: %d, want %d", path, resp.StatusCode, want)
		}
	}
}

func TestDownloadStoreError(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	e.ms.getErr = errors.New("db down")
	if resp := e.get(t, "/download?key=k"); resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status %d, want 500", resp.StatusCode)
	}
}

func TestDownloadNotReadyStatuses(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)

	cases := map[string]string{
		store.StatusNew:        "Файл еще не обработан",
		store.StatusProcessing: "Файл еще не обработан",
		store.StatusError:      "При обработке файла произошла ошибка",
	}
	for status, wantMsg := range cases {
		e.ms.msgs["k-"+status] = store.Message{Key: "k-" + status, Filename: "a.xlsx", Status: status}
		for _, q := range []string{"", "&sheetNum=0"} {
			resp := e.get(t, "/download?key=k-"+status+q)
			r := decode(t, resp)
			if resp.StatusCode != http.StatusUnprocessableEntity || r.Success || r.Message != wantMsg {
				t.Errorf("status=%s q=%q: %d %+v", status, q, resp.StatusCode, r)
			}
		}
	}
}

func TestDownloadSheetCSV(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	seedDone(t, e, "k", "Отчёт март.xlsx", map[string]string{"0.csv": "a;b\n1;2\n", "1.csv": "x\n"})

	resp := e.get(t, "/download?key=k&sheetNum=1")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "x\n" {
		t.Fatalf("status %d, body %q", resp.StatusCode, body)
	}
	_, params, err := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if err != nil || params["filename"] != "Отчёт март_1.csv" {
		t.Errorf("Content-Disposition = %q (%v)", resp.Header.Get("Content-Disposition"), err)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestDownloadMissingSheet(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	seedDone(t, e, "k", "a.xlsx", map[string]string{"0.csv": "1\n"})

	resp := e.get(t, "/download?key=k&sheetNum=7")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status %d, want 404", resp.StatusCode)
	}
}

func readZipBody(t *testing.T, body []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("not a zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestDownloadZip(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	seedDone(t, e, "k", "Отчёт.xlsx", map[string]string{"0.csv": "a\n", "1.csv": "b\n"})

	resp := e.get(t, "/download?key=k")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	got := readZipBody(t, body)
	if len(got) != 2 || got["0.csv"] != "a\n" || got["1.csv"] != "b\n" {
		t.Errorf("zip contents = %v", got)
	}
	_, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if params["filename"] != "Отчёт.zip" {
		t.Errorf("filename = %q", params["filename"])
	}
	// Архив закеширован, во временной директории ничего не осталось.
	if _, err := os.Stat(filepath.Join(e.archives, "k.zip")); err != nil {
		t.Errorf("archive was not cached: %v", err)
	}
	if names := dirNames(t, e.tmp); len(names) != 0 {
		t.Errorf("tmp must be empty, got %v", names)
	}
}

func TestDownloadZipIsCached(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	seedDone(t, e, "k", "a.xlsx", map[string]string{"0.csv": "orig\n"})

	first := e.get(t, "/download?key=k")
	first.Body.Close()

	// Меняем исходные данные: повторный запрос отдаёт уже собранный архив.
	_ = os.WriteFile(filepath.Join(e.out, "k", "0.csv"), []byte("changed\n"), 0o644)

	resp := e.get(t, "/download?key=k")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("second download: %d", resp.StatusCode)
	}
	if got := readZipBody(t, body); got["0.csv"] != "orig\n" {
		t.Errorf("cached archive expected, got %v", got)
	}
}

func TestDownloadZipConcurrent(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	seedDone(t, e, "k", "a.xlsx", map[string]string{"0.csv": "data\n"})

	var wg sync.WaitGroup
	for range 15 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp := e.get(t, "/download?key=k")
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("status %d", resp.StatusCode)
				return
			}
			if got := readZipBody(t, body); got["0.csv"] != "data\n" {
				t.Errorf("zip contents = %v", got)
			}
		}()
	}
	wg.Wait()
	if names := dirNames(t, e.tmp); len(names) != 0 {
		t.Errorf("tmp leftovers: %v", names)
	}
}

// Статус done, но результата на диске нет (например, удалён очисткой): 500, без битого архива в кеше.
func TestDownloadZipOutputDirMissing(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	e.ms.msgs["k"] = store.Message{Key: "k", Filename: "a.xlsx", Status: store.StatusDone}

	resp := e.get(t, "/download?key=k")
	r := decode(t, resp)
	if resp.StatusCode != http.StatusInternalServerError || r.Success {
		t.Fatalf("status %d, %+v", resp.StatusCode, r)
	}
	if names := dirNames(t, e.archives); len(names) != 0 {
		t.Errorf("broken archive left in cache: %v", names)
	}
	if names := dirNames(t, e.tmp); len(names) != 0 {
		t.Errorf("tmp leftovers: %v", names)
	}
}

func TestDownloadFilenameWithoutExtension(t *testing.T) {
	t.Parallel()
	e := setup(t, nil)
	seedDone(t, e, "k", "noext", map[string]string{"0.csv": "a\n"})

	resp := e.get(t, "/download?key=k&sheetNum=0")
	resp.Body.Close()
	_, params, _ := mime.ParseMediaType(resp.Header.Get("Content-Disposition"))
	if params["filename"] != "noext_0.csv" {
		t.Errorf("filename = %q", params["filename"])
	}
}

func TestSendJSON(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	sendJson(rec, http.StatusTeapot, Response{Success: false, Message: "я чайник", Data: &FileResponse{FileKey: "k"}})
	if rec.Code != http.StatusTeapot {
		t.Errorf("code %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json; charset=UTF-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	var r Response
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil || r.Message != "я чайник" || r.Data.FileKey != "k" {
		t.Errorf("body %q (%v)", rec.Body.String(), err)
	}

	rec = httptest.NewRecorder()
	sendJson(rec, 200, Response{Success: true})
	if strings.Contains(rec.Body.String(), `"data"`) {
		t.Errorf("nil data must be omitted: %s", rec.Body.String())
	}
}
