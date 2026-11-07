package server

import (
	"crypto/sha256"
	"crypto/subtle"
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
	slog.Debug("Starting server...")

	inPath, err := settings.GetPathIn(env)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get input directory path: %v", err)
	}
	tmpPath, err := settings.GetPathTmp(env)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get tmp directory path: %v", err)
	}
	outPath, err := settings.GetPathOut(env)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get output directory path: %v", err)
	}
	archivesPath, err := settings.GetPathArchives(env)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get archives directory path: %v", err)
	}

	mux := http.NewServeMux()

	mux.HandleFunc("POST /upload", func(w http.ResponseWriter, r *http.Request) {
		limit := settings.MaxUploadSize << 20

		if r.ContentLength > limit {
			sendJson(w, http.StatusRequestEntityTooLarge, Response{
				Success: false,
				Message: "File size exceeds the allowed limit",
			})
			return
		}

		r.Body = http.MaxBytesReader(w, r.Body, settings.MaxUploadSize<<20)
		err := r.ParseMultipartForm(32 << 20)
		if err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				sendJson(w, http.StatusRequestEntityTooLarge, Response{
					Success: false,
					Message: fmt.Sprintf("File is too large, maximum is %d MB", settings.MaxUploadSize),
				})
				return
			}
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Failed to parse the form",
			})
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Failed to get the file",
			})
			return
		}
		defer file.Close()

		fileName := filepath.Base(header.Filename)
		if fileName == "" || fileName == "." || fileName == ".." || strings.Contains(fileName, "/") || strings.Contains(fileName, "\\") {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Invalid file name",
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
				Message: "Invalid file extension",
			})
			return
		}
		message, err := store.NewMessage(fileName)
		if err != nil {
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Failed to create the message",
			})
			return
		}
		err = ms.SaveMessage(message)
		if err != nil {
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Failed to save the message",
			})
			return
		}
		tmpFilePath := filepath.Join(tmpPath, message.Key+fileExt)
		inFilePath := filepath.Join(inPath, message.Key+fileExt)

		out, err := os.Create(tmpFilePath)
		if err != nil {
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Failed to delete the message:", slog.Group("details", "error", errDelete))
			}

			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Failed to create the file",
			})
			return
		}

		_, err = io.Copy(out, file)
		if err != nil {
			errClose := out.Close()
			if errClose != nil {
				slog.Error("Failed to close the file:", slog.Group("details", "error", errClose))
			}
			errRemove := os.Remove(tmpFilePath)
			if errRemove != nil {
				slog.Error("Failed to remove the temporary file:", slog.Group("details", "error", errRemove))
			}
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Failed to delete the message:", slog.Group("details", "error", errDelete))
			}
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Failed to write the file",
			})
			return
		}
		errClose := out.Close()
		if errClose != nil {
			errRemove := os.Remove(tmpFilePath)
			if errRemove != nil {
				slog.Error("Failed to remove the temporary file:", slog.Group("details", "error", errRemove))
			}
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Failed to delete the message:", slog.Group("details", "error", errDelete))
			}
			slog.Error("Failed to close the file:", slog.Group("details", "error", errClose))
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Failed to close the file",
			})
			return
		}

		err = os.Rename(tmpFilePath, inFilePath)
		if err != nil {
			errRemove := os.RemoveAll(tmpFilePath)
			if errRemove != nil {
				slog.Error("Failed to remove the temporary file:", slog.Group("details", "error", errRemove))
			}
			errDelete := ms.DeleteMessage(message.Key)
			if errDelete != nil {
				slog.Error("Failed to delete the message:", slog.Group("details", "error", errDelete))
			}
			slog.Error("Failed to rename the file:", slog.Group("details", "error", err))

			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "Failed to save the file",
			})
			return
		}
		daemonManager.StartIfNotRunning(job)

		sendJson(w, http.StatusAccepted, Response{
			Success: true,
			Message: "File uploaded successfully, waiting for processing",
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
				Message: "Parameter key is missing",
			})
			return
		}
		message, err := ms.GetMessage(key)
		if err != nil {
			slog.Error("Failed to find the message:", slog.Group("details", "error", err))

			sendJson(w, http.StatusInternalServerError, Response{
				Success: false,
				Message: "Failed to find the message",
			})
			return
		}
		if message == nil {
			sendJson(w, http.StatusNotFound, Response{
				Success: false,
				Message: "Message not found",
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
				Message: "Parameter key is missing",
			})
			return
		}

		if sheet != "" && !str.OnlyNum(sheet) {
			sendJson(w, http.StatusBadRequest, Response{
				Success: false,
				Message: "Parameter sheetNum is not a number",
			})
			return
		}

		message, err := ms.GetMessage(key)
		if err != nil {
			slog.Error("Failed to find the message:", slog.Group("details", "error", err))
			sendJson(w, http.StatusInternalServerError, Response{
				Success: false,
				Message: "Failed to find the message",
			})
			return
		}
		if message == nil {
			sendJson(w, http.StatusNotFound, Response{
				Success: false,
				Message: "Message not found",
			})
			return
		}

		switch message.Status {
		case store.StatusNew, store.StatusProcessing:
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "File has not been processed yet",
			})
			return
		case store.StatusError:
			sendJson(w, http.StatusUnprocessableEntity, Response{
				Success: false,
				Message: "An error occurred while processing the file",
			})
			return
		}

		filesPath := filepath.Join(outPath, key)
		if sheet == "" {
			zipFileExt := ".zip"
			tmpUuid, errGen := str.GenerateUUID()
			if errGen != nil {
				slog.Error("Failed to generate UUID:", slog.Group("details", "error", errGen))
				tmpUuid = key
			}
			fileArchiveTmpPath := filepath.Join(tmpPath, tmpUuid+zipFileExt)
			fileArchivePath := filepath.Join(archivesPath, key+zipFileExt)

			if _, statErr := os.Stat(fileArchivePath); statErr != nil {
				zipErr := zipper.ZipFiles(fileArchiveTmpPath, filesPath)

				if zipErr != nil {
					sendJson(w, http.StatusInternalServerError, Response{
						Success: false,
						Message: "Failed to create the archive",
					})
					return
				}

				renameErr := os.Rename(fileArchiveTmpPath, fileArchivePath)
				if renameErr != nil {
					removeErr := os.Remove(fileArchiveTmpPath)
					if removeErr != nil {
						slog.Error("Failed to remove the temporary file:", slog.Group("details", "error", removeErr))
					}
					sendJson(w, http.StatusInternalServerError, Response{
						Success: false,
						Message: "Failed to prepare the archive",
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

	token := resolveAuthToken(settings)
	if token == "" {
		slog.Warn("Authorization is disabled: set GOCSVIFY_AUTH_TOKEN or authToken in the settings")
	}
	handler := AuthMiddleware(mux, token)

	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", settings.ServerPort),
		Handler:           handler,
		ReadTimeout:       time.Duration(settings.ReadTimeout),
		ReadHeaderTimeout: time.Duration(settings.ReadHeadTimeout),
		WriteTimeout:      time.Duration(settings.WriteTimeout),
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Debug("Server started on http://localhost:" + strconv.Itoa(settings.ServerPort))
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("Failed to run the server:", slog.Group("details", "error", err))
			errCh <- err
		}
		close(errCh)
	}()

	return server, errCh, nil
}

func resolveAuthToken(settings convertSettings.Settings) string {
	if token := strings.TrimSpace(os.Getenv("GOCSVIFY_AUTH_TOKEN")); token != "" {
		return token
	}
	return strings.TrimSpace(settings.AuthToken)
}

func AuthMiddleware(h http.Handler, token string) http.Handler {
	if token == "" {
		return h
	}
	want := sha256.Sum256([]byte(token))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		scheme, got, found := strings.Cut(strings.TrimSpace(r.Header.Get("Authorization")), " ")
		if found && strings.EqualFold(scheme, "Bearer") {
			have := sha256.Sum256([]byte(strings.TrimSpace(got)))
			if subtle.ConstantTimeCompare(have[:], want[:]) == 1 {
				h.ServeHTTP(w, r)
				return
			}
		}

		w.Header().Set("WWW-Authenticate", `Bearer realm="gocsvify"`)
		sendJson(w, http.StatusUnauthorized, Response{
			Success: false,
			Message: "Unauthorized",
		})
	})
}

func sendJson(w http.ResponseWriter, status int, r Response) {
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(r)
}
