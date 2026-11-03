package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	xlsconverter "xls_converter/converters"
	"xls_converter/daemon"
	"xls_converter/libs/dirs"
	"xls_converter/server"
	convertSettings "xls_converter/settings"

	"go.etcd.io/bbolt"
)

var daemonManager daemon.Manager

func main() {
	err := run()
	if err != nil {
		log.Fatalf("Ошибка при запуске сервиса: %v", err)
	}
}

func run() error {
	fmt.Println("Сервис конвертации файлов запущен. Ожидаем файлы...")
	env := flag.String("env", "prod", "Режим запуска (dev или prod)")
	settingsPath := flag.String("settings_path", "", "Файл настроек")
	flag.Parse()

	settings, err := convertSettings.GetSettings(*settingsPath)
	if err != nil {
		return fmt.Errorf("ошибка при получении настроек: %v", err)
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
			return fmt.Errorf("ошибка при открытии файла для логирования: %v", err)
		}
		defer file.Close()
		logger = slog.New(slog.NewJSONHandler(file, &slog.HandlerOptions{Level: logLevel}))
	} else {
		logger = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: logLevel}))
	}
	slog.SetDefault(logger)

	slog.Info("Запуск приложения")

	appDir, err := settings.GetPathApp(*env)
	if err != nil {
		return fmt.Errorf("ошибка при чтении настроек: %v", err)
	}
	err = dirs.CreateDirs([]string{appDir})
	if err != nil {
		return fmt.Errorf("ошибка при создании директорий: %v", err)
	}
	db, err := bbolt.Open(filepath.Join(appDir, "app.db"), 0600, &bbolt.Options{
		Timeout: 2 * time.Second,
	})
	if err != nil {
		return fmt.Errorf("ошибка при чтении бд: %v", err)
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
			slog.Error("Ошибка при запуске сервера", "error", err)
			errStop := daemonManager.Stop(stopTimeout)
			if errStop != nil {
				slog.Error("Ошибка при остановке демона", "error", errStop)
			}
			return err
		}

		select {
		case err := <-chanErr:
			slog.Error("Ошибка при работе сервера", "error", err)
			errStop := daemonManager.Stop(stopTimeout)
			if errStop != nil {
				slog.Error("Ошибка при остановке демона", "error", errStop)
			}
			return err
		case <-sigChan:
			slog.Info("Получен сигнал остановки. Завершение работы скрипта")
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		// Очищаем ресурсы сервера и закрываем сетевой порт
		if err := serverInstance.Shutdown(shutdownCtx); err != nil {
			slog.Error("Ошибка при плавной остановке HTTP-сервера", "error", err)
		} else {
			slog.Info("HTTP-сервер успешно и плавно остановлен.")
		}

		errStop := daemonManager.Stop(stopTimeout)
		if errStop != nil {
			slog.Error("Ошибка при остановке демона", "error", errStop)
		}
	} else {
		slog.Info("Запуск демона в автономном режиме")
		slog.Debug("HTTP-сервер отключен в настройках. Демон работает в автономном режиме.")
		slog.Debug("Для остановки нажмите Ctrl+C...")

		select {
		case <-sigChan:
			slog.Info("Получен сигнал остановки. Завершение работы скрипта")
			errStop := daemonManager.Stop(stopTimeout)
			if errStop != nil {
				slog.Error("Ошибка при остановке демона", "error", errStop)
			}
		case <-doneChan:
			slog.Info("Демон завершил работу. Завершение работы скрипта")
		}
	}

	return nil
}
