package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// jobFunc - адаптер функции в Job.
type jobFunc func(ctx context.Context) error

func (f jobFunc) Run(ctx context.Context) error { return f(ctx) }

func waitClosed(t *testing.T, ch <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatal("channel was not closed in time")
	}
}

func TestStartRunsJobAndClosesDone(t *testing.T) {
	t.Parallel()

	var m Manager
	var ran atomic.Int32
	done := m.StartIfNotRunning(jobFunc(func(ctx context.Context) error {
		ran.Add(1)
		return nil
	}))
	waitClosed(t, done, time.Second)
	if ran.Load() != 1 {
		t.Fatalf("job ran %d times", ran.Load())
	}
}

func TestStartIfNotRunningDoesNotStartTwice(t *testing.T) {
	t.Parallel()

	var m Manager
	var runs atomic.Int32
	release := make(chan struct{})
	job := jobFunc(func(ctx context.Context) error {
		runs.Add(1)
		<-release
		return nil
	})

	d1 := m.StartIfNotRunning(job)
	d2 := m.StartIfNotRunning(job)
	if d1 != d2 {
		t.Error("second call must return the same done channel")
	}
	close(release)
	waitClosed(t, d1, time.Second)
	if runs.Load() != 1 {
		t.Errorf("job started %d times, want 1", runs.Load())
	}
}

func TestRestartAfterFinish(t *testing.T) {
	t.Parallel()

	var m Manager
	var runs atomic.Int32
	job := jobFunc(func(ctx context.Context) error { runs.Add(1); return nil })

	d1 := m.StartIfNotRunning(job)
	waitClosed(t, d1, time.Second)

	d2 := m.StartIfNotRunning(job)
	if d1 == d2 {
		t.Fatal("a new run must get a new done channel")
	}
	waitClosed(t, d2, time.Second)
	if runs.Load() != 2 {
		t.Errorf("runs = %d, want 2", runs.Load())
	}
}

func TestRestartAfterJobError(t *testing.T) {
	t.Parallel()

	var m Manager
	failing := jobFunc(func(ctx context.Context) error { return errors.New("boom") })
	waitClosed(t, m.StartIfNotRunning(failing), time.Second)

	var ran atomic.Bool
	ok := jobFunc(func(ctx context.Context) error { ran.Store(true); return nil })
	waitClosed(t, m.StartIfNotRunning(ok), time.Second)
	if !ran.Load() {
		t.Error("manager must be able to start again after a failed run")
	}
}

func TestStopWhenNotRunning(t *testing.T) {
	t.Parallel()

	var m Manager
	if err := m.Stop(10 * time.Millisecond); err != nil {
		t.Fatalf("Stop on idle manager: %v", err)
	}
}

func TestStopCancelsContextAndWaits(t *testing.T) {
	t.Parallel()

	var m Manager
	started := make(chan struct{})
	var finished atomic.Bool
	done := m.StartIfNotRunning(jobFunc(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		time.Sleep(50 * time.Millisecond) // «дообработка» текущего файла
		finished.Store(true)
		return nil
	}))
	<-started

	if err := m.Stop(2 * time.Second); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !finished.Load() {
		t.Error("Stop must wait for the job to finish")
	}
	waitClosed(t, done, time.Second)
}

func TestStopTimeout(t *testing.T) {
	t.Parallel()

	var m Manager
	release := make(chan struct{})
	started := make(chan struct{})
	done := m.StartIfNotRunning(jobFunc(func(ctx context.Context) error {
		close(started)
		<-release // игнорирует ctx
		return nil
	}))
	<-started

	start := time.Now()
	err := m.Stop(50 * time.Millisecond)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if el := time.Since(start); el > time.Second {
		t.Errorf("Stop took %v, expected about 50ms", el)
	}
	close(release)
	waitClosed(t, done, time.Second)
}

func TestStopThenStartAgain(t *testing.T) {
	t.Parallel()

	var m Manager
	job := jobFunc(func(ctx context.Context) error { <-ctx.Done(); return nil })

	d1 := m.StartIfNotRunning(job)
	if err := m.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, d1, time.Second)

	d2 := m.StartIfNotRunning(job)
	if d1 == d2 {
		t.Fatal("expected a fresh run")
	}
	if err := m.Stop(time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentStartAndStop(t *testing.T) {
	t.Parallel()

	var m Manager
	var runs atomic.Int32
	job := jobFunc(func(ctx context.Context) error {
		runs.Add(1)
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Millisecond):
		}
		return nil
	})

	finish := make(chan struct{})
	for range 20 {
		go func() {
			for range 50 {
				m.StartIfNotRunning(job)
				_ = m.Stop(time.Second)
			}
			finish <- struct{}{}
		}()
	}
	for range 20 {
		<-finish
	}
	if err := m.Stop(2 * time.Second); err != nil {
		t.Errorf("final Stop: %v", err)
	}
	if runs.Load() == 0 {
		t.Error("job never ran")
	}
}
