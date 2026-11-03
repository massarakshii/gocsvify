package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type Manager struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	isRunning bool
	doneChan  chan struct{}
}

func (dm *Manager) StartIfNotRunning(job Job) <-chan struct{} {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	if dm.isRunning {
		return dm.doneChan
	}

	ctx, cancel := context.WithCancel(context.Background())
	doneChan := make(chan struct{})

	dm.cancel = cancel
	dm.doneChan = doneChan
	dm.isRunning = true

	go func() {
		defer close(doneChan)
		defer cancel()
		err := job.Run(ctx)
		if err != nil {
			slog.Error("Критическая ошибка при выполнении задачи, остановка воркера:", slog.Group("details", "error", err))
		}

		dm.mu.Lock()
		if dm.doneChan == doneChan { // не трогаем состояние более нового запуска
			dm.isRunning = false
		}
		dm.mu.Unlock()
	}()

	return doneChan
}

func (dm *Manager) Stop(timeout time.Duration) error {
	dm.mu.Lock()
	running, cancel, done := dm.isRunning, dm.cancel, dm.doneChan
	dm.mu.Unlock()

	if !running {
		return nil
	}
	cancel()

	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("демон не остановился за %s", timeout)
	}
}
