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
		slog.Error("Ошибка при получении пути к директории входа:", slog.Group("details", "error", err))

		return err
	}
	out, err := j.Settings.GetPathOut(j.Env)
	if err != nil {
		slog.Error("Ошибка при получении пути к директории выхода:", slog.Group("details", "error", err))
		return err
	}
	history, err := j.Settings.GetPathHistory(j.Env)
	if err != nil {
		slog.Error("Ошибка при получении пути к директории истории:", slog.Group("details", "error", err))
		return err
	}
	tmp, err := j.Settings.GetPathTmp(j.Env)
	if err != nil {
		slog.Error("Ошибка при получении пути к директории временных файлов:", slog.Group("details", "error", err))
		return err
	}
	archives, err := j.Settings.GetPathArchives(j.Env)
	if err != nil {
		slog.Error("Ошибка при получении пути к директории архивов:", slog.Group("details", "error", err))
		return err
	}
	failed, err := j.Settings.GetPathFailed(j.Env)
	if err != nil {
		slog.Error("Ошибка при получении пути к директории архивов:", slog.Group("details", "error", err))
		return err
	}

	err = dirs.CreateDirs([]string{in, out, history, tmp, archives, failed})
	if err != nil {
		slog.Error("Ошибка при создании директорий:", slog.Group("details", "error", err))
		return err
	}

	startTime := time.Now()
	for {
		slog.Debug("ожидание файлов...")
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
			slog.Info("Превышено время ожидания. Останавливаем демона.")
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
		slog.Error("Ошибка чтения директории:", slog.Group("details", "error", err))
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
				slog.Info("Ошибка при поиске сообщения:", slog.Group("details", "error", err))
			}
			if message != nil {
				err = j.ms.DeleteMessage(message.Key)
				if err != nil {
					slog.Error("Ошибка удаления сообщения:", slog.Group("details", "error", err))
				}
			}
			if dirInfo.IsDir() {
				err = os.RemoveAll(path)
			} else {
				err = os.Remove(path)
			}

			if err != nil {
				slog.Error("Ошибка удаления файла/директории:", slog.Group("details", "error", err))
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
		slog.Error("Ошибка чтения директории:", slog.Group("details", "error", err))
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
				slog.Error("Ошибка при поиске сообщения:", slog.Group("details", "error", err))
				errRename := os.Rename(fileP, failedP)
				if errRename != nil {
					slog.Error("Ошибка при перемещении файла в папку failed:", slog.Group("details", "error", errRename))
				}
				continue
			}
			if message == nil {
				message = store.NewMessageWithKey(fileName, fileKey)

				err = j.ms.SaveMessage(message)
				if err != nil {
					errRename := os.Rename(fileP, failedP)
					if errRename != nil {
						slog.Error("Ошибка при перемещении файла в папку failed:", slog.Group("details", "error", errRename))
					}
					slog.Error("failed to save message:", "error", err)
					continue
				}
			}
			if message.Status != store.StatusNew {
				message.UpdateStatus(store.StatusNew, "Повторная обработка")
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
				slog.Info("Ошибка при перемещении файла в папку с ошибками:", slog.Group("details", "error", err))
			}
			slog.Error("Обработка файла завершилась ошибкой:", slog.Group(
				"details",
				"file", res.FileName,
				"time", res.Time,
				"error", res.Error,
			))
			continue
		}
		slog.Debug("Обработан файл:", slog.Group(
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
			slog.Error("ошибка сохранения сообщения:", "error", err)
		}

		logMsg, err := j.convertSafe(ctx, task, dirPathOut, DirPathHistory, tmpDir)

		if err != nil {
			slog.Error(logMsg, "error", err)

			if ctx.Err() != nil {
				task.message.UpdateStatus(store.StatusNew, "Задача отменена, возвращаем статус new")
				errMsg := j.ms.SaveMessage(task.message)
				if errMsg != nil {
					slog.Error("Ошибка при сохранении сообщения", "error", errMsg)
				}
			} else {
				task.message.UpdateStatus(store.StatusError, "Произошла ошибка при конвертации файла")
				errMsg := j.ms.SaveMessage(task.message)
				if errMsg != nil {
					slog.Error("Ошибка при сохранении сообщения", "error", errMsg)
				}
			}
		} else {
			task.message.UpdateStatus(store.StatusDone, "Файл успешно конвертирован")
			errMsg := j.ms.SaveMessage(task.message)
			if errMsg != nil {
				slog.Error("ошибка сохранения сообщения:", "error", errMsg)
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
			slog.Error("ошибка конвертации:", "error", r)
			err = fmt.Errorf("panic: %v", r)
			logMsg = "Паника при конвертации файла"
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
		return "Произошла ошибка при создании директории", err
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
		return "Неподдерживаемое расширение файла", fmt.Errorf("unsupported file extension: %s", ext)
	}

	err = converterSync.Convert(tmpPath)
	if err != nil {
		return "Произошла ошибка при конвертации файла", err
	}

	if err = os.RemoveAll(csvPath); err != nil {
		return "Не удалось удалить предыдущий результат", err
	}

	err = os.Rename(tmpPath, csvPath)
	if err != nil {
		return "Произошла ошибка при сохранении итогового файла", err
	}

	historyPath := filepath.Join(DirPathHistory, task.fileName)
	err = os.Rename(task.filepath, historyPath)
	if err != nil {
		return "Произошла ошибка при перемещении файла в историю", err
	}

	return "Успешная конвертация файла", nil
}
