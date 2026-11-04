package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"gocsvify/daemon"
	"gocsvify/libs/str"
	convertSettings "gocsvify/settings"
	"gocsvify/store"
	"gocsvify/zipper"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Response struct {
	Success bool          `json:"success"`
	Message string        `json:"message"`
	Data    *FileResponse `json:"data,omitempty"`
}

type FileResponse struct {
	Filename string `json:"fileName"`
	FileKey  string `json:"fileKey"`
	Status   string `json:"status"`
}

func StartServer(settings convertSettings.Settings, env string, ms store.MessageStore, job daemon.Job, daemonManager *daemon.Manager) (*http.Server, <-chan error, error) {
	slog.Debug("Запуск сервера...")

	inPath, err := settings.GetPathIn(env)
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка при получении пути к директории входа: %v", err)
	}
	tmpPath, err := settings.GetPathTmp(env)
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка при получении пути к tmp директории: %v", err)
	}
	outPath, err := settings.GetPathOut(env)
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка при получении пути к директории выхода: %v", err)
	}
	archivesPath, err := settings.GetPathArchives(env)
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка при получении пути к директории архивов: %v", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /upload", func(w http.ResponseWriter, r *http.Request) {
		limit := settings.MaxUploadSize << 20

		if r.ContentLength > limit {
			sendJson(w, http.StatusRequestEntityTooLarge, Response{
				Success: false,
				Message: "Размер файла превышает допустимый",
			})
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, settings.MaxUploadSize<<20)
		err := r.ParseMultipartForm(32 << 20)
		if err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				sendJson(w, http.StatusRequestEntityTooLarge, Response{
					Success: false,
					Message: fmt.Sprintf("Файл слишком большой, максимум %d МБ", settings.MaxUploadSize),
				})
				return
			}
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Ошибка при разборе формы",
			})
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Ошибка при получении файла",
			})
			return
		}
		defer file.Close()

		fileName := filepath.Base(header.Filename)
		if fileName == "" || fileName == "." || fileName == ".." || strings.Contains(fileName, "/") || strings.Contains(fileName, "\\") {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Неверное имя файла",
			})
			return
		}

		allowedExt := map[string]bool{
			".xls":   true,
			".xlsx":  true,
			".xlsm":  true,
			".accdb": true,
			".mdb":   true,
		}
		fileExt := filepath.Ext(fileName)
		if !allowedExt[strings.ToLower(fileExt)] {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Неверное расширение файла",
			})
			return
		}
		message, err := store.NewMessage(fileName)
		if err != nil {
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Ошибка при создании сообщения",
			})
			return
		}
		err = ms.SaveMessage(message)
		if err != nil {
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Ошибка при сохранении сообщения",
			})
			return
		}
		tmpFilePath := filepath.Join(tmpPath, message.Key+fileExt)
		inFilePath := filepath.Join(inPath, message.Key+fileExt)

		out, err := os.Create(tmpFilePath)
		if err != nil {
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Ошибка при удалении сообщения:", slog.Group("details", "error", errDelete))
			}

			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Ошибка при создании файла",
			})
			return
		}

		_, err = io.Copy(out, file)
		if err != nil {
			errClose := out.Close()
			if errClose != nil {
				slog.Error("Ошибка при закрытии файла:", slog.Group("details", "error", errClose))
			}
			errRemove := os.Remove(tmpFilePath)
			if errRemove != nil {
				slog.Error("Ошибка при удалении временного файла:", slog.Group("details", "error", errRemove))
			}
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Ошибка при удалении сообщения:", slog.Group("details", "error", errDelete))
			}
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Ошибка при записи файла",
			})
			return
		}
		errClose := out.Close()
		if errClose != nil {
			errRemove := os.Remove(tmpFilePath)
			if errRemove != nil {
				slog.Error("Ошибка при удалении временного файла:", slog.Group("details", "error", errRemove))
			}
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Ошибка при удалении сообщения:", slog.Group("details", "error", errDelete))
			}
			slog.Error("Ошибка при закрытии файла:", slog.Group("details", "error", errClose))
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Ошибка при закрытии файла",
			})
			return
		}

		err = os.Rename(tmpFilePath, inFilePath)
		if err != nil {
			errRemove := os.RemoveAll(tmpFilePath)
			if errRemove != nil {
				slog.Error("Ошибка при удалении временного файла:", slog.Group("details", "error", errRemove))
			}
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Ошибка при удалении сообщения:", slog.Group("details", "error", errDelete))
			}
			slog.Error("Ошибка при переименовании файла:", slog.Group("details", "error", err))

			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Ошибка при сохранении файла",
			})
			return
		}
		daemonManager.StartIfNotRunning(job)

		sendJson(w, http.StatusAccepted, Response{
			Success: true,
			Message: "Файл успешно загружен, ожидается обработка",
			Data: &FileResponse{
				Filename: fileName,
				FileKey:  message.Key,
			},
		})
		return
	})

	mux.HandleFunc("GET /check", func(w http.ResponseWriter, r *http.Request) {
		key := r.FormValue("key")
		if key == "" {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Параметр key не указан",
			})
			return
		}
		message, err := ms.GetMessage(key)
		if err != nil {
			slog.Error("Ошибка при поиске сообщения:", slog.Group("details", "error", err))

			sendJson(w, http.StatusInternalServerError, Response{
				Success: false,
				Message: "Ошибка при поиске сообщения",
			})
			return
		}
		if message == nil {
			sendJson(w, http.StatusNotFound, Response{
				Success: false,
				Message: "Сообщение не найдено",
			})
			return
		}

		sendJson(w, http.StatusOK, Response{
			Success: true,
			Message: message.Message,
			Data: &FileResponse{
				Filename: message.Filename,
				FileKey:  message.Key,
				Status:   message.Status,
			},
		})
		return
	})

	mux.HandleFunc("GET /download", func(w http.ResponseWriter, r *http.Request) {
		key := r.FormValue("key")
		sheet := r.FormValue("sheetNum")
		if key == "" {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Параметр key не указан",
			})
			return
		}

		if sheet != "" && !str.OnlyNum(sheet) {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Параметр sheet не является числом",
			})
			return
		}

		message, err := ms.GetMessage(key)
		if err != nil {
			slog.Error("Ошибка при поиске сообщения:", slog.Group("details", "error", err))
			sendJson(w, http.StatusInternalServerError, Response{
				Success: false,
				Message: "Ошибка при поиске сообщения",
			})
			return
		}
		if message == nil {
			sendJson(w, http.StatusNotFound, Response{
				Success: false,
				Message: "Сообщение не найдено",
			})
			return
		}

		switch message.Status {
		case store.StatusNew, store.StatusProcessing:
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Файл еще не обработан",
			})
			return
		case store.StatusError:
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "При обработке файла произошла ошибка",
			})
			return
		}

		filesPath := filepath.Join(outPath, key)
		if sheet == "" {
			zipFileExt := ".zip"
			tmpUuid, errGen := str.GenerateUUID()
			if errGen != nil {
				slog.Error("Ошибка при генерации UUID:", slog.Group("details", "error", errGen))
				tmpUuid = key
			}
			fileArchiveTmpPath := filepath.Join(tmpPath, tmpUuid+zipFileExt)
			fileArchivePath := filepath.Join(archivesPath, key+zipFileExt)

			if _, statErr := os.Stat(fileArchivePath); statErr != nil {
				zipErr := zipper.ZipFiles(fileArchiveTmpPath, filesPath)

				if zipErr != nil {
					sendJson(w, http.StatusInternalServerError, Response{
						Success: false,
						Message: "Ошибка при создании архива",
					})
					return
				}

				renameErr := os.Rename(fileArchiveTmpPath, fileArchivePath)
				if renameErr != nil {
					removeErr := os.Remove(fileArchiveTmpPath)
					if removeErr != nil {
						slog.Error("Ошибка при удалении временного файла:", slog.Group("details", "error", removeErr))
					}
					sendJson(w, http.StatusInternalServerError, Response{
						Success: false,
						Message: "Ошибка при подготовке архива",
					})
					return
				}
			}

			originalFileExt := filepath.Ext(message.Filename)
			originalFileName := strings.TrimSuffix(message.Filename, originalFileExt)
			downloadName := originalFileName + zipFileExt
			disposition := mime.FormatMediaType("attachment", map[string]string{"filename": downloadName})

			w.Header().Set("Content-Description", "File Transfer")
			w.Header().Set("Content-Transfer-Encoding", "binary")
			w.Header().Set("Content-Disposition", disposition)
			w.Header().Set("Content-Type", "application/octet-stream")

			http.ServeFile(w, r, fileArchivePath)

			return
		}

		fileExt := ".csv"
		filePath := filepath.Join(filesPath, sheet+fileExt)

		originalFileExt := filepath.Ext(message.Filename)
		originalFileName := strings.TrimSuffix(message.Filename, originalFileExt)
		downloadName := originalFileName + "_" + sheet + fileExt
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": downloadName})

		w.Header().Set("Content-Description", "File Transfer")
		w.Header().Set("Content-Transfer-Encoding", "binary")
		w.Header().Set("Content-Disposition", disposition)
		w.Header().Set("Content-Type", "application/octet-stream")

		http.ServeFile(w, r, filePath)
	})

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", settings.ServerPort),
		Handler:           mux,
		ReadTimeout:       time.Duration(settings.ReadTimeout),
		ReadHeaderTimeout: time.Duration(settings.ReadHeadTimeout),
		WriteTimeout:      time.Duration(settings.WriteTimeout),
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Debug("Сервер запущен на http://localhost:" + strconv.Itoa(settings.ServerPort))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Ошибка запуска сервера:", slog.Group("details", "error", err))
			errCh <- err
		}
		close(errCh)
	}()

	return server, errCh, nil
}

func sendJson(w http.ResponseWriter, status int, r Response) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(r)
}
