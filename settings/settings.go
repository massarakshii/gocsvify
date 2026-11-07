package settings

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	ct "gocsvify/libs/configTime"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

type Settings struct {
	DirPathApp      string      `json:"appDir"`
	DirPathIn       string      `json:"in"`
	DirPathOut      string      `json:"out"`
	DirPathFailed   string      `json:"failed"`
	DirPathHistory  string      `json:"history"`
	DirPathArchives string      `json:"archives"`
	IsAbsPath       bool        `json:"isAbsPath"`
	Sleep           ct.Duration `json:"sleep"`
	IdleTimeout     ct.Duration `json:"idleTimeout"`
	LifeTime        ct.Duration `json:"lifeTime"`
	LifeTimeHistory ct.Duration `json:"lifeTimeHistory"`
	PadRows         bool        `json:"padRows"`
	Delimiter       string      `json:"delimiter"`
	WithHidden      bool        `json:"withHidden"`
	StartServer     bool        `json:"startServer"`
	ServerPort      int         `json:"serverPort"`
	AuthToken       string      `json:"authToken"`
	WriteTimeout    ct.Duration `json:"writeTimeout"`
	ReadTimeout     ct.Duration `json:"readTimeout"`
	ReadHeadTimeout ct.Duration `json:"readHeadTimeout"`
	MaxUploadSize   int64       `json:"maxUploadSize"`
	LogLevel        string      `json:"logLevel"`
	LogFile         string      `json:"logFile"`
	UseMdbLib       bool        `json:"useMdbLib"`
}

const DefaultSettingsPath = "settings.json"

func GetSettings(settingsPath string) (*Settings, error) {
	var settings Settings
	var err error

	if settingsPath == "" {
		var exist bool
		exist, err = defaultExists()
		if err != nil {
			return nil, err
		}
		if !exist {
			settings, err = setDefaultSettings()
		} else {
			settings, err = readSettings(getDefaultSettingsPath())
		}
	} else {
		settings, err = readSettings(settingsPath)
	}

	if err != nil {
		return nil, err
	}

	if err = settings.validateSettings(); err != nil {
		return nil, err
	}

	return &settings, nil
}

func (s *Settings) validateSettings() error {
	if s.DirPathIn == "" ||
		s.DirPathOut == "" ||
		s.DirPathHistory == "" ||
		s.DirPathArchives == "" ||
		s.DirPathApp == "" ||
		s.DirPathFailed == "" {

		return errors.New("not all paths are set")
	}

	if s.IdleTimeout < 0 || s.WriteTimeout < 0 || s.ReadTimeout < 0 || s.ReadHeadTimeout < 0 || s.LifeTime < 0 || s.LifeTimeHistory < 0 || s.Sleep < 0 {
		return errors.New("invalid timeout values")
	}

	if s.MaxUploadSize <= 0 {
		return errors.New("invalid maximum upload size")
	}

	if s.Delimiter == "" {
		return errors.New("delimiter is not set")
	}

	if len(s.Delimiter) > 1 {
		return errors.New("delimiter must be a single character")
	}

	return nil
}

func (s *Settings) GetPathIn(env string) (string, error) {
	return s.getPath(env, s.DirPathIn)
}

func (s *Settings) GetPathOut(env string) (string, error) {
	return s.getPath(env, s.DirPathOut)
}

func (s *Settings) GetPathHistory(env string) (string, error) {
	return s.getPath(env, s.DirPathHistory)
}

func (s *Settings) GetPathArchives(env string) (string, error) {
	return s.getPath(env, s.DirPathArchives)
}

func (s *Settings) GetPathTmp(env string) (string, error) {
	return s.getPath(env, filepath.Join(s.DirPathApp, "tmp"))
}

func (s *Settings) GetPathApp(env string) (string, error) {
	return s.getPath(env, s.DirPathApp)
}

func (s *Settings) GetPathFailed(env string) (string, error) {
	return s.getPath(env, s.DirPathFailed)
}

func (s *Settings) getPath(env string, originalPath string) (string, error) {
	var path string
	if s.IsAbsPath == true || env == "dev" {
		path = originalPath
	} else {
		execPath, err := os.Executable()
		if err != nil {
			return "", err
		}
		execDir := filepath.Dir(execPath)

		path = filepath.Join(execDir, originalPath)
	}

	return path, nil
}

func readSettings(settingsPath string) (Settings, error) {
	file, err := os.ReadFile(settingsPath)
	if err != nil {
		return Settings{}, err
	}

	settings := getDefaultSettings()
	err = json.Unmarshal(file, &settings)
	if err != nil {
		return Settings{}, err
	}

	return settings, nil
}

func setDefaultSettings() (Settings, error) {
	defaultSettings := getDefaultSettings()
	jsonData, err := json.Marshal(defaultSettings, jsontext.WithIndent("  "))
	if err != nil {
		return Settings{}, err
	}
	err = os.WriteFile(getDefaultSettingsPath(), jsonData, 0644)
	if err != nil {
		return Settings{}, err
	}

	return defaultSettings, nil
}

func getDefaultSettings() Settings {
	return Settings{
		DirPathApp:      "./gocsvify_data",
		DirPathIn:       "./gocsvify_data/in",
		DirPathOut:      "./gocsvify_data/out",
		DirPathFailed:   "./gocsvify_data/failed",
		DirPathHistory:  "./gocsvify_data/history",
		DirPathArchives: "./gocsvify_data/archives",
		IsAbsPath:       false,
		Sleep:           ct.Duration(10 * time.Second),
		IdleTimeout:     ct.Duration(60 * time.Second),
		LifeTime:        ct.Duration(60 * time.Minute),
		LifeTimeHistory: ct.Duration(60 * time.Minute),
		PadRows:         false,
		Delimiter:       ";",
		WithHidden:      false,
		StartServer:     false,
		ServerPort:      8080,
		AuthToken:       "",
		WriteTimeout:    0,
		ReadTimeout:     ct.Duration(600 * time.Second),
		ReadHeadTimeout: ct.Duration(30 * time.Second),
		MaxUploadSize:   256,
		LogLevel:        "info",
		LogFile:         "",
		UseMdbLib:       false,
	}
}

func defaultExists() (bool, error) {
	_, err := os.Stat(getDefaultSettingsPath())
	if err == nil {
		return true, nil
	}
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return false, err
}

var getDefaultSettingsPath = func() string {
	exePath, err := os.Executable()
	if err != nil {
		panic(err)
	}

	return filepath.Join(filepath.Dir(exePath), DefaultSettingsPath)
}
