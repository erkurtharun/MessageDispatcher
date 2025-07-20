// Package scheduler provides a lightweight periodic task runner with fixed-rate scheduling and backlog handling.
// It ensures tasks run every fixed interval (e.g., 2 minutes) from the last attempt time, queuing if tasks overrun to prevent drift.
// On Stop, it waits for the current task to finish but discards any queued tasks.
// For testability, time operations are abstracted via a Clock interface.
package scheduler

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"MessageDispatcher/internal/app/sender"
	"MessageDispatcher/internal/infrastructure/cache"
	"go.uber.org/zap"
)

type Config struct {
	Interval      time.Duration
	QueueCapacity int // Capacity of the task queue (default 100 if <=0)
}

// Clock abstracts time operations for testability.
type Clock interface {
	Now() time.Time
	NewTimer(d time.Duration) Timer
	Until(t time.Time) time.Duration
	Add(t time.Time, d time.Duration) time.Time
	Sub(t1, t2 time.Time) time.Duration
	Before(t1, t2 time.Time) bool
	Sleep(d time.Duration)
}

// Timer abstracts time.Timer for mocking.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
	Reset(d time.Duration)
}

// realClock implements Clock using standard time package.
type realClock struct{}

func (realClock) Now() time.Time                             { return time.Now() }
func (realClock) NewTimer(d time.Duration) Timer             { return &realTimer{t: time.NewTimer(d)} }
func (realClock) Until(t time.Time) time.Duration            { return time.Until(t) }
func (realClock) Add(t time.Time, d time.Duration) time.Time { return t.Add(d) }
func (realClock) Sub(t1, t2 time.Time) time.Duration         { return t1.Sub(t2) }
func (realClock) Before(t1, t2 time.Time) bool               { return t1.Before(t2) }
func (realClock) Sleep(d time.Duration)                      { time.Sleep(d) }

// realTimer wraps time.Timer.
type realTimer struct {
	t *time.Timer
}

func (rt *realTimer) C() <-chan time.Time   { return rt.t.C }
func (rt *realTimer) Stop() bool            { return rt.t.Stop() }
func (rt *realTimer) Reset(d time.Duration) { rt.t.Reset(d) }

type Scheduler struct {
	running   atomic.Bool
	service   *sender.Service
	logger    *zap.Logger
	interval  time.Duration
	taskQueue chan struct{}
	wg        sync.WaitGroup
	cancel    context.CancelFunc
	cache     *cache.RedisCache
	clock     Clock // Added for testability
}

// NewScheduler creates a new scheduler with configurable interval and queue capacity.
func NewScheduler(service *sender.Service, cfg Config, logger *zap.Logger, cache *cache.RedisCache) *Scheduler {
	queueCap := cfg.QueueCapacity
	if queueCap <= 0 {
		queueCap = 100
	}

	return &Scheduler{
		service:   service,
		interval:  cfg.Interval,
		logger:    logger,
		taskQueue: make(chan struct{}, queueCap),
		cache:     cache,
		clock:     realClock{},
	}
}

func (s *Scheduler) loadPersistentLastAttemptTime(ctx context.Context) (time.Time, bool) {
	key := "scheduler_last_attempt_time"
	lastAttemptStr, err := s.cache.Get(ctx, key)
	if err != nil {
		s.logger.Warn("Failed to get persistent last attempt time from Redis", zap.Error(err))
		return time.Time{}, false
	}
	if lastAttemptStr == "" {
		s.logger.Info("No persistent last attempt time found in Redis")
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, lastAttemptStr)
	if err != nil {
		s.logger.Warn("Invalid persistent last attempt time format in Redis", zap.Error(err), zap.String("value", lastAttemptStr))
		return time.Time{}, false
	}
	s.logger.Info("Loaded persistent last attempt time from Redis", zap.Time("lastAttemptTime", parsed))
	return parsed, true
}

func (s *Scheduler) savePersistentLastAttemptTime(ctx context.Context, t time.Time) {
	key := "scheduler_last_attempt_time"
	if err := s.cache.Set(ctx, key, t.Format(time.RFC3339), 0); err != nil {
		s.logger.Warn("Failed to save persistent last attempt time to Redis", zap.Error(err), zap.Time("lastAttemptTime", t))
	} else {
		s.logger.Info("Saved persistent last attempt time to Redis", zap.Time("lastAttemptTime", t))
	}
}

// Start If reset is true (e.g., from API), triggers immediate task without loading persistent time.
// If reset is false (e.g., app restart), loads persistent last attempt time to resume without drift.
func (s *Scheduler) Start(parentCtx context.Context, reset bool) {
	if s.running.Load() {
		return
	}
	s.running.Store(true)
	ctx, cancel := context.WithCancel(parentCtx)
	s.cancel = cancel

	now := s.clock.Now()

	var lastAttemptTime time.Time
	var loaded bool
	if !reset {
		lastAttemptTime, loaded = s.loadPersistentLastAttemptTime(ctx)
		if !loaded {
			s.logger.Info("No persistent last attempt time, starting fresh")
		}
	}

	s.logger.Info("Starting scheduler", zap.Bool("reset", reset), zap.Bool("loaded_from_persistent", loaded), zap.Time("lastAttemptTime", lastAttemptTime))

	s.wg.Add(1)
	go s.worker(ctx)

	immediate := reset || !loaded
	if immediate {
		s.taskQueue <- struct{}{}
		s.logger.Info("Immediate first task scheduled", zap.Time("scheduledAt", now))
	} else {
		// Queue missed tasks on resume
		elapsed := s.clock.Sub(now, lastAttemptTime)
		missed := int(math.Floor(float64(elapsed) / float64(s.interval)))
		if missed > 0 {
			s.logger.Info("Queuing missed tasks on resume", zap.Int("missed_count", missed))
			for i := 0; i < missed; i++ {
				select {
				case s.taskQueue <- struct{}{}:
					s.logger.Debug("Missed task queued")
				default:
					s.logger.Warn("Task queue full, dropping missed task signal")
				}
			}
		}
	}

	s.wg.Add(1)
	go s.timing(ctx, lastAttemptTime, loaded)
}

func (s *Scheduler) worker(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Panic recovered in worker goroutine", zap.Any("panic", r))
		}
		s.wg.Done()
	}()
	for {
		select {
		case <-ctx.Done():
			for len(s.taskQueue) > 0 {
				<-s.taskQueue
			}
			s.logger.Debug("Worker stopped via context cancel")
			return
		case _, ok := <-s.taskQueue:
			if !ok {
				s.logger.Warn("Task queue closed unexpectedly")
				return
			}
			attemptTime := s.clock.Now()
			start := s.clock.Now()
			if err := s.service.SendUnsentMessages(ctx); err != nil {
				s.logger.Warn("SendUnsentMessages failed", zap.Error(err))
			}
			// Save last attempt time regardless of success
			s.savePersistentLastAttemptTime(ctx, attemptTime)
			duration := s.clock.Sub(s.clock.Now(), start)
			s.logger.Debug("Task completed", zap.Duration("duration", duration))
		}
	}
}

func (s *Scheduler) timing(ctx context.Context, lastAttemptTime time.Time, loaded bool) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Error("Panic recovered in timing goroutine", zap.Any("panic", r))
		}
		s.wg.Done()
	}()

	now := s.clock.Now()
	var nextRun time.Time
	if loaded {
		elapsed := s.clock.Sub(now, lastAttemptTime)
		n := math.Ceil(float64(elapsed) / float64(s.interval))
		nextRun = s.clock.Add(lastAttemptTime, time.Duration(n)*s.interval)
	} else {
		nextRun = s.clock.Add(now, s.interval)
	}

	timer := s.clock.NewTimer(s.clock.Until(nextRun))
	defer timer.Stop()

	var totalJitter time.Duration

	for {
		select {
		case <-ctx.Done():
			s.logger.Debug("Timing goroutine stopped via context cancel")
			return
		case tickTime := <-timer.C():
			expected := nextRun
			jitter := s.clock.Sub(tickTime, expected)
			totalJitter += jitter
			s.logger.Debug("Timer tick", zap.Time("expected", expected), zap.Time("actual", tickTime), zap.Duration("jitter", jitter), zap.Duration("totalJitter", totalJitter))

			s.taskQueue <- struct{}{}
			s.logger.Debug("Task scheduled (blocking if queue full)")

			now := s.clock.Now()
			nextRun = s.clock.Add(nextRun, s.interval)
			if s.clock.Before(nextRun, now) {
				nextRun = s.clock.Add(now, s.interval)
				s.logger.Debug("Backlog detected, advancing nextRun relatively", zap.Time("new_nextRun", nextRun))
			}

			adjustedWait := s.clock.Until(nextRun) - (totalJitter / 2)
			if adjustedWait < 0 {
				adjustedWait = 0
			}

			if !timer.Stop() {
				select {
				case <-timer.C():
				default:
				}
			}
			timer.Reset(adjustedWait)
		}
	}
}

func (s *Scheduler) Stop() {
	if !s.running.Load() {
		return
	}
	s.running.Store(false)
	if s.cancel != nil {
		s.cancel()
	}
	s.wg.Wait()
}

func (s *Scheduler) IsRunning() bool {
	return s.running.Load()
}
