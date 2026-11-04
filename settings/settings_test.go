package settings

// Тесты требуют Go 1.25+ с GOEXPERIMENT=jsonv2 (или Go 1.26+): пакет использует encoding/json/v2.
// Тесты, подменяющие путь к settings.json по умолчанию (useTempDefaultPath),
// не параллельные: путь - глобальная переменная.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	ct "gocsvify/libs/configTime"
)

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestDefaultSettingsAreValid(t *testing.T) {
	t.Parallel()

	d := getDefaultSettings()
	if err := d.validateSettings(); err != nil {
		t.Fatalf("default settings must be valid: %v", err)
	}
	if d.MaxUploadSize != 256 || d.Delimiter != ";" || d.ServerPort != 8080 {
		t.Errorf("unexpected defaults: %+v", d)
	}
	if time.Duration(d.ReadTimeout) != 600*time.Second || time.Duration(d.ReadHeadTimeout) != 30*time.Second {
		t.Errorf("unexpected server timeouts: read=%v head=%v", d.ReadTimeout, d.ReadHeadTimeout)
	}
}

func TestValidateSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mutate  func(*Settings)
		wantErr bool
	}{
		{"defaults", func(*Settings) {}, false},
		{"empty in", func(s *Settings) { s.DirPathIn = "" }, true},
		{"empty out", func(s *Settings) { s.DirPathOut = "" }, true},
		{"empty history", func(s *Settings) { s.DirPathHistory = "" }, true},
		{"empty archives", func(s *Settings) { s.DirPathArchives = "" }, true},
		{"empty app", func(s *Settings) { s.DirPathApp = "" }, true},
		{"empty failed", func(s *Settings) { s.DirPathFailed = "" }, true},
		{"negative idle", func(s *Settings) { s.IdleTimeout = -1 }, true},
		{"negative write", func(s *Settings) { s.WriteTimeout = -1 }, true},
		{"negative read", func(s *Settings) { s.ReadTimeout = -1 }, true},
		{"negative read head", func(s *Settings) { s.ReadHeadTimeout = -1 }, true},
		{"negative lifetime", func(s *Settings) { s.LifeTime = -1 }, true},
		{"negative lifetime history", func(s *Settings) { s.LifeTimeHistory = -1 }, true},
		{"negative sleep", func(s *Settings) { s.Sleep = -1 }, true},
		{"zero timeouts are fine", func(s *Settings) { s.WriteTimeout, s.ReadTimeout, s.Sleep = 0, 0, 0 }, false},
		{"zero max upload", func(s *Settings) { s.MaxUploadSize = 0 }, true},
		{"negative max upload", func(s *Settings) { s.MaxUploadSize = -5 }, true},
		{"empty delimiter", func(s *Settings) { s.Delimiter = "" }, true},
		{"two-char delimiter", func(s *Settings) { s.Delimiter = ";;" }, true},
		{"comma delimiter", func(s *Settings) { s.Delimiter = "," }, false},
		{"tab delimiter", func(s *Settings) { s.Delimiter = "\t" }, false},
		// Известное ограничение: проверка по байтам, многобайтовый символ отклоняется.
		{"multibyte delimiter is rejected (len in bytes)", func(s *Settings) { s.Delimiter = "§" }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := getDefaultSettings()
			tt.mutate(&s)
			err := s.validateSettings()
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateSettings() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestReadSettingsMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := readSettings(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected error for a missing file")
	}
}

func TestReadSettingsDirectory(t *testing.T) {
	t.Parallel()
	if _, err := readSettings(t.TempDir()); err == nil {
		t.Fatal("expected error when path is a directory")
	}
}

func TestReadSettingsInvalidJSON(t *testing.T) {
	t.Parallel()
	for name, content := range map[string]string{
		"empty":       ``,
		"truncated":   `{"in": `,
		"array":       `[1,2,3]`,
		"bad bool":    `{"isAbsPath": "yes"}`,
		"bad port":    `{"serverPort": "abc"}`,
		"bad dur str": `{"sleep": "10 parsecs"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := writeFile(t, t.TempDir(), "s.json", content)
			if _, err := readSettings(p); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// Файл со старым набором ключей не должен «обнулять» то, чего в нём нет:
// недостающие поля берутся из значений по умолчанию.
func TestReadSettingsKeepsDefaultsForMissingKeys(t *testing.T) {
	t.Parallel()

	p := writeFile(t, t.TempDir(), "s.json", `{"in": "custom/in", "serverPort": 9090}`)
	got, err := readSettings(p)
	if err != nil {
		t.Fatalf("readSettings: %v", err)
	}
	def := getDefaultSettings()
	if got.DirPathIn != "custom/in" || got.ServerPort != 9090 {
		t.Errorf("explicit values lost: %+v", got)
	}
	if got.MaxUploadSize != def.MaxUploadSize || got.Delimiter != def.Delimiter ||
		got.ReadTimeout != def.ReadTimeout || got.DirPathApp != def.DirPathApp {
		t.Errorf("defaults not preserved: %+v", got)
	}
	if err := got.validateSettings(); err != nil {
		t.Errorf("old-style config must validate thanks to defaults: %v", err)
	}
}

func TestReadSettingsDurations(t *testing.T) {
	t.Parallel()

	p := writeFile(t, t.TempDir(), "s.json",
		`{"sleep": 5, "idleTimeout": "90s", "lifeTime": "2h", "readTimeout": "1m30s", "writeTimeout": 0}`)
	got, err := readSettings(p)
	if err != nil {
		t.Fatalf("readSettings: %v", err)
	}
	checks := map[string][2]time.Duration{
		"sleep": {time.Duration(got.Sleep), 5 * time.Second},
		"idle":  {time.Duration(got.IdleTimeout), 90 * time.Second},
		"life":  {time.Duration(got.LifeTime), 2 * time.Hour},
		"read":  {time.Duration(got.ReadTimeout), 90 * time.Second},
		"write": {time.Duration(got.WriteTimeout), 0},
	}
	for name, c := range checks {
		if c[0] != c[1] {
			t.Errorf("%s = %v, want %v", name, c[0], c[1])
		}
	}
}

func TestSetDefaultSettingsWritesReadableFile(t *testing.T) {
	useTempDefaultPath(t)

	got, err := setDefaultSettings()
	if err != nil {
		t.Fatalf("setDefaultSettings: %v", err)
	}
	if got != getDefaultSettings() {
		t.Errorf("returned settings differ from defaults: %+v", got)
	}

	data, err := os.ReadFile(getDefaultSettingsPath())
	if err != nil {
		t.Fatalf("settings file not created: %v", err)
	}
	// Длительности пишутся строками, а не наносекундами.
	for _, want := range []string{`"readTimeout": "10m0s"`, `"lifeTimeHistory"`, `"maxUploadSize": 256`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("settings file lacks %s:\n%s", want, data)
		}
	}

	back, err := readSettings(getDefaultSettingsPath())
	if err != nil {
		t.Fatalf("readSettings of generated file: %v", err)
	}
	if back != got {
		t.Errorf("round trip mismatch:\n got: %+v\nwant: %+v", back, got)
	}
}

func TestSetDefaultSettingsWriteFails(t *testing.T) {
	dir := useTempDefaultPath(t)
	// На месте settings.json лежит каталог - запись должна упасть.
	if err := os.Mkdir(filepath.Join(dir, "settings.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := setDefaultSettings(); err == nil {
		t.Fatal("expected error")
	}
}

// useTempDefaultPath подменяет путь к settings.json по умолчанию на файл во временной
// директории и возвращает эту директорию. Путь - глобальная переменная, поэтому тест,
// вызвавший хелпер, не должен быть параллельным.
func useTempDefaultPath(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old := getDefaultSettingsPath
	getDefaultSettingsPath = func() string { return filepath.Join(dir, DefaultSettingsPath) }
	t.Cleanup(func() { getDefaultSettingsPath = old })
	return dir
}

func TestDefaultExists(t *testing.T) {
	dir := useTempDefaultPath(t)

	if ok, err := defaultExists(); err != nil || ok {
		t.Fatalf("defaultExists() = %v, %v; want false, nil", ok, err)
	}
	writeFile(t, dir, "settings.json", `{}`)
	if ok, err := defaultExists(); err != nil || !ok {
		t.Fatalf("defaultExists() = %v, %v; want true, nil", ok, err)
	}
}

func TestGetSettingsExplicitPath(t *testing.T) {
	useTempDefaultPath(t)

	p := writeFile(t, t.TempDir(), "custom.json", `{"serverPort": 1234, "delimiter": ","}`)
	s, err := GetSettings(p)
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if s.ServerPort != 1234 || s.Delimiter != "," {
		t.Errorf("unexpected settings: %+v", s)
	}
	if _, err := os.Stat(getDefaultSettingsPath()); err == nil {
		t.Error("default settings file must not be created when an explicit path is given")
	}
}

func TestGetSettingsExplicitPathErrors(t *testing.T) {
	useTempDefaultPath(t)

	dir := t.TempDir()
	if _, err := GetSettings(filepath.Join(dir, "missing.json")); err == nil {
		t.Error("missing file: expected error")
	}
	if _, err := GetSettings(writeFile(t, dir, "bad.json", `{`)); err == nil {
		t.Error("broken JSON: expected error")
	}
	if _, err := GetSettings(writeFile(t, dir, "inv.json", `{"maxUploadSize": 0}`)); err == nil {
		t.Error("invalid values: expected validation error")
	}
	if _, err := GetSettings(writeFile(t, dir, "inv2.json", `{"in": ""}`)); err == nil {
		t.Error("empty path: expected validation error")
	}
}

func TestGetSettingsCreatesDefaultFile(t *testing.T) {
	useTempDefaultPath(t)

	s, err := GetSettings("")
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if *s != getDefaultSettings() {
		t.Errorf("unexpected settings: %+v", s)
	}
	if _, err := os.Stat(getDefaultSettingsPath()); err != nil {
		t.Errorf("default file must be created: %v", err)
	}

	// Повторный вызов читает файл, а не перезаписывает.
	if err := os.WriteFile(getDefaultSettingsPath(), []byte(`{"serverPort": 7777}`), 0o644); err != nil {
		t.Fatal(err)
	}
	s2, err := GetSettings("")
	if err != nil {
		t.Fatalf("GetSettings (2nd): %v", err)
	}
	if s2.ServerPort != 7777 {
		t.Errorf("edited file must be respected, got port %d", s2.ServerPort)
	}
	if s2.Delimiter != ";" {
		t.Errorf("missing keys must fall back to defaults, got delimiter %q", s2.Delimiter)
	}
}

func TestGetSettingsInvalidDefaultFile(t *testing.T) {
	dir := useTempDefaultPath(t)
	writeFile(t, dir, "settings.json", `{broken`)
	if _, err := GetSettings(""); err == nil {
		t.Fatal("expected error")
	}
}

func TestGetPaths(t *testing.T) {
	t.Parallel()

	s := getDefaultSettings()
	s.DirPathApp, s.DirPathIn, s.DirPathOut = "app", "in", "out"
	s.DirPathFailed, s.DirPathHistory, s.DirPathArchives = "failed", "history", "archives"

	type getter struct {
		name string
		fn   func(string) (string, error)
		rel  string
	}
	getters := []getter{
		{"app", s.GetPathApp, "app"},
		{"in", s.GetPathIn, "in"},
		{"out", s.GetPathOut, "out"},
		{"failed", s.GetPathFailed, "failed"},
		{"history", s.GetPathHistory, "history"},
		{"archives", s.GetPathArchives, "archives"},
		{"tmp", s.GetPathTmp, filepath.Join("app", "tmp")},
	}

	exe, err := os.Executable()
	if err != nil {
		t.Skipf("os.Executable: %v", err)
	}
	execDir := filepath.Dir(exe)

	for _, g := range getters {
		// dev: путь как есть.
		if got, err := g.fn("dev"); err != nil || got != g.rel {
			t.Errorf("%s/dev = %q, %v; want %q", g.name, got, err, g.rel)
		}
		// prod: относительно каталога исполняемого файла.
		if got, err := g.fn("prod"); err != nil || got != filepath.Join(execDir, g.rel) {
			t.Errorf("%s/prod = %q, %v; want %q", g.name, got, err, filepath.Join(execDir, g.rel))
		}
	}

	// IsAbsPath: путь как есть в любом режиме.
	s.IsAbsPath = true
	for _, g := range getters {
		if got, _ := g.fn("prod"); got != g.rel {
			t.Errorf("%s/prod/isAbs = %q, want %q", g.name, got, g.rel)
		}
	}
}

var _ = ct.Duration(0)
