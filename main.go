package main

import (
	"context"
	"flag"
	"fmt"
	xlsconverter "gocsvify/converters"
	"gocsvify/daemon"
	"gocsvify/libs/dirs"
	"gocsvify/server"
	convertSettings "gocsvify/settings"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"go.etcd.io/bbolt"
)

var daemonManager daemon.Manager

func main() {
	err := run()
	if err != nil {
		log.Fatalf("Service failed to start: %v", err)
	}
}

func run() error {
	fmt.Println("File conversion service started. Waiting for files...")
	env := flag.String("env", "prod", "Run mode (dev or prod)")
	settingsPath := flag.String("settings_path", "", "Path to the settings file")
	flag.Parse()

	settings, err := convertSettings.GetSettings(*settingsPath)
	if err != nil {
		return fmt.Errorf("failed to get settings: %v", err)
	}

	var logLevel slog.Level
	switch settings.LogLevel {
	case "info":
		logLevel = slog.LevelInfo
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	var logger *slog.Logger
	if settings.LogFile != "" {
		file, err := os.OpenFile(settings.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			return fmt.Errorf("failed to open log file: %v", err)
		}
		defer file.Close()
		logger = slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: logLevel}))
	} else {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	}
	slog.SetDefault(logger)

	slog.Info("Starting application")

	appDir, err := settings.GetPathApp(*env)
	if err != nil {
		return fmt.Errorf("failed to read settings: %v", err)
	}
	err = dirs.CreateDirs([]string{appDir})
	if err != nil {
		return fmt.Errorf("failed to create directories: %v", err)
	}
	db, err := bbolt.Open(filepath.Join(appDir, "app.db"), 0600, &bbolt.Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("failed to open database: %v", err)
	}
	defer db.Close()

	ms := xlsconverter.NewMessageStoreBolt(db)
	job := xlsconverter.NewJob(*env, *settings, ms)
	doneChan := daemonManager.StartIfNotRunning(job)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	stopTimeout := 30 * time.Second
	if settings.StartServer {
		serverInstance, chanErr, err := server.StartServer(*settings, *env, ms, job, &daemonManager)
		if err != nil {
			slog.Error("Failed to start server", "error", err)
			errStop := daemonManager.Stop(stopTimeout)
			if errStop != nil {
				slog.Error("Failed to stop daemon", "error", errStop)
			}
			return err
		}

		select {
		case err := <-chanErr:
			slog.Error("Server error", "error", err)
			errStop := daemonManager.Stop(stopTimeout)
			if errStop != nil {
				slog.Error("Failed to stop daemon", "error", errStop)
			}
			return err
		case <-sigChan:
			slog.Info("Stop signal received. Shutting down")
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Очищаем ресурсы сервера и закрываем сетевой порт
		if err := serverInstance.Shutdown(shutdownCtx); err != nil {
			slog.Error("Failed to shut down HTTP server gracefully", "error", err)
		} else {
			slog.Info("HTTP server shut down gracefully.")
		}

		errStop := daemonManager.Stop(stopTimeout)
		if errStop != nil {
			slog.Error("Failed to stop daemon", "error", errStop)
		}
	} else {
		slog.Info("Starting daemon in standalone mode")
		slog.Debug("HTTP server is disabled in the settings. The daemon runs in standalone mode.")
		slog.Debug("Press Ctrl+C to stop...")

		select {
		case <-sigChan:
			slog.Info("Stop signal received. Shutting down")
			errStop := daemonManager.Stop(stopTimeout)
			if errStop != nil {
				slog.Error("Failed to stop daemon", "error", errStop)
			}
		case <-doneChan:
			slog.Info("Daemon finished. Shutting down")
		}
	}

	return nil
}
