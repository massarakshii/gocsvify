package converters

import (
	"context"
	"fmt"
	"gocsvify/converters/accdb"
	"gocsvify/converters/xls"
	"gocsvify/converters/xlsx"
	"gocsvify/libs/dirs"
	"gocsvify/libs/str"
	convertSettings "gocsvify/settings"
	"gocsvify/store"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Job struct {
	Settings convertSettings.Settings
	Env      string
	ms       store.MessageStore
}

func NewJob(env string, settings convertSettings.Settings, ms store.MessageStore) *Job {
	return &Job{
		Settings: settings,
		Env:      env,
		ms:       ms,
	}
}

func (j *Job) Run(ctx context.Context) error {
	in, err := j.Settings.GetPathIn(j.Env)
	if err != nil {
		slog.Error("Failed to get input directory path:", slog.Group("details", "error", err))

		return err
	}
	out, err := j.Settings.GetPathOut(j.Env)
	if err != nil {
		slog.Error("Failed to get output directory path:", slog.Group("details", "error", err))
		return err
	}
	history, err := j.Settings.GetPathHistory(j.Env)
	if err != nil {
		slog.Error("Failed to get history directory path:", slog.Group("details", "error", err))
		return err
	}
	tmp, err := j.Settings.GetPathTmp(j.Env)
	if err != nil {
		slog.Error("Failed to get temporary directory path:", slog.Group("details", "error", err))
		return err
	}
	archives, err := j.Settings.GetPathArchives(j.Env)
	if err != nil {
		slog.Error("Failed to get archives directory path:", slog.Group("details", "error", err))
		return err
	}
	failed, err := j.Settings.GetPathFailed(j.Env)
	if err != nil {
		slog.Error("Failed to get failed directory path:", slog.Group("details", "error", err))
		return err
	}

	err = dirs.CreateDirs([]string{in, out, history, tmp, archives, failed})
	if err != nil {
		slog.Error("Failed to create directories:", slog.Group("details", "error", err))
		return err
	}

	startTime := time.Now()
	for {
		slog.Debug("Waiting for files...")
		if hasFiles, err := j.hasFiles(in); err == nil && hasFiles {
			j.start(ctx, in, out, history, tmp, failed)
			startTime = time.Now()
		} else {
			j.clearOld(out, time.Duration(j.Settings.LifeTime))
			j.clearOld(archives, time.Duration(j.Settings.LifeTime))
			j.clearOld(tmp, time.Duration(j.Settings.LifeTime))
			j.clearOld(history, time.Duration(j.Settings.LifeTimeHistory))
		}

		idleTime := time.Since(startTime)
		timeout := time.Duration(j.Settings.IdleTimeout)
		if timeout != 0 && idleTime > timeout {
			slog.Info("Idle timeout exceeded. Stopping the daemon.")
			break
		}

		timer := time.NewTimer(time.Duration(j.Settings.Sleep))
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
	return nil
}

func (j *Job) clearOld(dirPath string, lifeTime time.Duration) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		slog.Error("Failed to read directory:", slog.Group("details", "error", err))
		return
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}

		path := filepath.Join(dirPath, entry.Name())
		dirInfo, err := os.Stat(path)
		if err != nil {
			continue
		}

		if dirInfo.ModTime().Before(time.Now().Add(-lifeTime)) {
			message, err := j.ms.GetMessage(entry.Name())
			if err != nil {
				slog.Info("Failed to find the message:", slog.Group("details", "error", err))
			}
			if message != nil {
				err = j.ms.DeleteMessage(message.Key)
				if err != nil {
					slog.Error("Failed to delete the message:", slog.Group("details", "error", err))
				}
			}
			if dirInfo.IsDir() {
				err = os.RemoveAll(path)
			} else {
				err = os.Remove(path)
			}

			if err != nil {
				slog.Error("Failed to remove file/directory:", slog.Group("details", "error", err))
			}
		}
	}
}

// hasFiles сообщает, есть ли в директории файлы для обработки: подпапки и скрытые файлы
// (.DS_Store, .gitkeep и т.п.) не считаются, иначе демон никогда бы не засыпал
// и очистка старых файлов не запускалась бы.
func (j *Job) hasFiles(dirPath string) (bool, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if !entry.IsDir() && !strings.HasPrefix(entry.Name(), ".") {
			return true, nil
		}
	}
	return false, nil
}

func (j *Job) start(ctx context.Context, in string, out string, history string, tmp string, failed string) {
	slog.Debug("start")

	entries, err := os.ReadDir(in)
	if err != nil {
		slog.Error("Failed to read directory:", slog.Group("details", "error", err))
		return
	}

	startTime := time.Now()
	numWorkers := runtime.NumCPU()
	slog.Debug("cnt cpu: " + strconv.Itoa(numWorkers))
	tasks := make(chan ConvertTask, 100)
	results := make(chan ConvertResult, 100)

	var wg sync.WaitGroup

	for range numWorkers {
		wg.Add(1)
		go j.convert(ctx, tasks, results, &wg, out, history, tmp)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	go func() {
		defer close(tasks)

		for _, entry := range entries {
			if entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
				continue
			}

			fileName := entry.Name()
			fileP := filepath.Join(in, fileName)
			failedP := filepath.Join(failed, fileName)
			fileExt := filepath.Ext(fileName)
			fileKey := strings.TrimSuffix(fileName, fileExt)

			message, err := j.ms.GetMessage(fileKey)
			if err != nil {
				slog.Error("Failed to find the message:", slog.Group("details", "error", err))
				errRename := os.Rename(fileP, failedP)
				if errRename != nil {
					slog.Error("Failed to move the file to the failed folder:", slog.Group("details", "error", errRename))
				}
				continue
			}
			if message == nil {
				message = store.NewMessageWithKey(fileName, fileKey)

				err = j.ms.SaveMessage(message)
				if err != nil {
					errRename := os.Rename(fileP, failedP)
					if errRename != nil {
						slog.Error("Failed to move the file to the failed folder:", slog.Group("details", "error", errRename))
					}
					slog.Error("failed to save message:", "error", err)
					continue
				}
			}
			if message.Status != store.StatusNew {
				message.UpdateStatus(store.StatusNew, "Reprocessing")
				message.Filename = fileName
				errSave := j.ms.SaveMessage(message)
				if errSave != nil {
					slog.Error("failed to save message:", "error", errSave)
				}
			}
			task := ConvertTask{
				filepath: fileP,
				fileName: fileName,
				message:  message,
			}
			select {
			case <-ctx.Done():
				return
			case tasks <- task:
			}
		}
	}()

	for res := range results {
		if res.Error != nil {
			err := os.Rename(res.Filepath, filepath.Join(failed, res.FileName))
			if err != nil {
				slog.Info("Failed to move the file to the failed folder:", slog.Group("details", "error", err))
			}
			slog.Error("File processing failed:", slog.Group(
				"details",
				"file", res.FileName,
				"time", res.Time,
				"error", res.Error,
			))
			continue
		}
		slog.Debug("File processed:", slog.Group(
			"details",
			"file", res.FileName,
			"time", res.Time,
			"error", res.Error,
		))
	}

	slog.Debug("sync:", "time", time.Since(startTime))
}

type ConvertTask struct {
	filepath string
	fileName string
	message  *store.Message
}

type ConvertResult struct {
	FileName string
	Filepath string
	Time     float64
	Error    error
}

func (j *Job) convert(ctx context.Context, tasks <-chan ConvertTask, results chan<- ConvertResult, wg *sync.WaitGroup, dirPathOut string, DirPathHistory string, tmpDir string) {
	defer wg.Done()

	for task := range tasks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		start := time.Now()
		task.message.UpdateStatus(store.StatusProcessing, "Converting file")
		err := j.ms.SaveMessage(task.message)
		if err != nil {
			slog.Error("Failed to save the message:", "error", err)
		}

		logMsg, err := j.convertSafe(ctx, task, dirPathOut, DirPathHistory, tmpDir)

		if err != nil {
			slog.Error(logMsg, "error", err)

			if ctx.Err() != nil {
				task.message.UpdateStatus(store.StatusNew, "Task canceled, status reset to new")
				errMsg := j.ms.SaveMessage(task.message)
				if errMsg != nil {
					slog.Error("Failed to save the message", "error", errMsg)
				}
			} else {
				task.message.UpdateStatus(store.StatusError, "An error occurred while converting the file")
				errMsg := j.ms.SaveMessage(task.message)
				if errMsg != nil {
					slog.Error("Failed to save the message", "error", errMsg)
				}
			}
		} else {
			task.message.UpdateStatus(store.StatusDone, "File converted successfully")
			errMsg := j.ms.SaveMessage(task.message)
			if errMsg != nil {
				slog.Error("Failed to save the message:", "error", errMsg)
			}
		}

		select {
		case <-ctx.Done():
			return
		case results <- ConvertResult{
			FileName: task.fileName,
			Filepath: task.filepath,
			Time:     time.Since(start).Seconds(),
			Error:    err,
		}:
		}
	}
}

func (j *Job) convertSafe(ctx context.Context, task ConvertTask, dirPathOut string, DirPathHistory string, tmpDir string) (logMsg string, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Conversion error:", "error", r)
			err = fmt.Errorf("panic: %v", r)
			logMsg = "Panic while converting the file"
		}
	}()

	return j.convertOne(ctx, task, dirPathOut, DirPathHistory, tmpDir)
}

func (j *Job) convertOne(ctx context.Context, task ConvertTask, dirPathOut string, DirPathHistory string, tmpDir string) (logMsg string, err error) {
	taskCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	fileExt := filepath.Ext(task.fileName)
	fileKey := strings.TrimSuffix(task.fileName, fileExt)
	uuid, err := str.GenerateUUID()
	if err != nil {
		uuid = fileKey
	}
	tmpPath := filepath.Join(tmpDir, uuid)
	csvPath := filepath.Join(dirPathOut, fileKey)

	err = dirs.CreateDirs([]string{tmpPath})
	if err != nil {
		return "Failed to create the directory", err
	}
	defer os.RemoveAll(tmpPath)

	ext := filepath.Ext(task.filepath)

	var converterSync Converter
	switch strings.ToLower(ext) {
	case ".xlsx", ".xlsm":
		converterSync = xlsx.NewConverter(taskCtx, task.filepath, &j.Settings)
	case ".xls":
		converterSync = xls.NewConverter(taskCtx, task.filepath, &j.Settings)
	case ".accdb", ".mdb":
		converterSync = accdb.NewConverter(taskCtx, task.filepath, &j.Settings)
	default:
		return "Unsupported file extension", fmt.Errorf("unsupported file extension: %s", ext)
	}

	err = converterSync.Convert(tmpPath)
	if err != nil {
		return "An error occurred while converting the file", err
	}

	if err = os.RemoveAll(csvPath); err != nil {
		return "Failed to remove the previous result", err
	}

	err = os.Rename(tmpPath, csvPath)
	if err != nil {
		return "Failed to save the resulting file", err
	}

	historyPath := filepath.Join(DirPathHistory, task.fileName)
	err = os.Rename(task.filepath, historyPath)
	if err != nil {
		return "Failed to move the file to history", err
	}

	return "File converted successfully", nil
}
