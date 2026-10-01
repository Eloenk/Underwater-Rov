package supervisor

import (
	"context"
	"fmt"
	"sync"
	"time"

	"companion/pkg/config"
	"companion/pkg/logging"
)

type State string

const (
	StateStopped      State = "STOPPED"
	StateStarting     State = "STARTING"
	StateRunning      State = "RUNNING"
	StateCrashed      State = "CRASHED"
	StateCrashLooping State = "CRASH_LOOPING"
)

type ModuleStatus struct {
	Name         string    `json:"name"`
	State        State     `json:"state"`
	Uptime       string    `json:"uptime"`
	RestartCount int       `json:"restart_count"`
	LastError    string    `json:"last_error"`
	StartTime    time.Time `json:"start_time"`
}

type Module interface {
	Name() string
	Start(ctx context.Context) error
	Stop() error
	Status() ModuleStatus
}

type managedModule struct {
	module       Module
	cancel       context.CancelFunc
	state        State
	restartCount int
	lastError    error
	startTime    time.Time
	mu           sync.RWMutex
}

type Supervisor struct {
	mu      sync.RWMutex
	modules map[string]*managedModule
	cfg     config.SupervisorConfig
	logger  *logging.LogManager
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

func NewSupervisor(cfg config.SupervisorConfig, logger *logging.LogManager) *Supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Supervisor{
		modules: make(map[string]*managedModule),
		cfg:     cfg,
		logger:  logger,
		ctx:     ctx,
		cancel:  cancel,
	}
}

func (s *Supervisor) RegisterModule(mod Module) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.modules[mod.Name()] = &managedModule{
		module: mod,
		state:  StateStopped,
	}
	s.logger.Log("supervisor", logging.LevelInfo, fmt.Sprintf("Registered module: %s", mod.Name()))
}

func (s *Supervisor) StartAll() {
	s.mu.RLock()
	names := make([]string, 0, len(s.modules))
	for name := range s.modules {
		names = append(names, name)
	}
	s.mu.RUnlock()

	for _, name := range names {
		s.StartModule(name)
	}
}

func (s *Supervisor) StartModule(name string) error {
	s.mu.RLock()
	mm, exists := s.modules[name]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("module %s not found", name)
	}

	mm.mu.Lock()
	if mm.state == StateRunning || mm.state == StateStarting {
		mm.mu.Unlock()
		return fmt.Errorf("module %s is already running/starting", name)
	}

	ctx, cancel := context.WithCancel(s.ctx)
	mm.cancel = cancel
	mm.state = StateStarting
	mm.startTime = time.Now()
	mm.mu.Unlock()

	s.logger.RecordEvent("supervisor", "MODULE_START", fmt.Sprintf("Starting module %s", name))

	s.wg.Add(1)
	go s.runModuleLoop(name, mm, ctx)

	return nil
}

func (s *Supervisor) runModuleLoop(name string, mm *managedModule, ctx context.Context) {
	defer s.wg.Done()

	backoff := s.cfg.RestartBackoffInitial
	if backoff <= 0 {
		backoff = 1 * time.Second
	}
	maxBackoff := s.cfg.RestartBackoffMax
	if maxBackoff <= 0 {
		maxBackoff = 30 * time.Second
	}

	for {
		select {
		case <-ctx.Done():
			mm.mu.Lock()
			mm.state = StateStopped
			mm.mu.Unlock()
			s.logger.Log("supervisor", logging.LevelInfo, fmt.Sprintf("Module %s stopped gracefully", name))
			return
		default:
		}

		mm.mu.Lock()
		mm.state = StateRunning
		mm.mu.Unlock()

		err := mm.module.Start(ctx)

		select {
		case <-ctx.Done():
			mm.mu.Lock()
			mm.state = StateStopped
			mm.mu.Unlock()
			return
		default:
		}

		mm.mu.Lock()
		mm.restartCount++
		mm.lastError = err
		mm.state = StateCrashed
		count := mm.restartCount
		mm.mu.Unlock()

		errMsg := "unknown error"
		if err != nil {
			errMsg = err.Error()
		}

		s.logger.RecordEvent("supervisor", "MODULE_CRASH", fmt.Sprintf("Module %s crashed (count=%d): %s", name, count, errMsg))

		if s.cfg.MaxCrashCount > 0 && count >= s.cfg.MaxCrashCount {
			mm.mu.Lock()
			mm.state = StateCrashLooping
			mm.mu.Unlock()
			s.logger.RecordEvent("supervisor", "CRASH_LOOP", fmt.Sprintf("Module %s entered crash loop state after %d failures", name, count))
			return
		}

		s.logger.Log("supervisor", logging.LevelWarn, fmt.Sprintf("Module %s restarting in %s (backoff)", name, backoff))

		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

func (s *Supervisor) StopModule(name string) error {
	s.mu.RLock()
	mm, exists := s.modules[name]
	s.mu.RUnlock()

	if !exists {
		return fmt.Errorf("module %s not found", name)
	}

	mm.mu.Lock()
	if mm.cancel != nil {
		mm.cancel()
	}
	_ = mm.module.Stop()
	mm.state = StateStopped
	mm.mu.Unlock()

	s.logger.RecordEvent("supervisor", "MODULE_STOP", fmt.Sprintf("Stopped module %s", name))
	return nil
}

func (s *Supervisor) RestartModule(name string) error {
	_ = s.StopModule(name)
	time.Sleep(200 * time.Millisecond)
	return s.StartModule(name)
}

func (s *Supervisor) StopAll() {
	s.cancel()
	s.mu.RLock()
	for _, mm := range s.modules {
		mm.mu.Lock()
		if mm.cancel != nil {
			mm.cancel()
		}
		_ = mm.module.Stop()
		mm.state = StateStopped
		mm.mu.Unlock()
	}
	s.mu.RUnlock()
	s.wg.Wait()
}

func (s *Supervisor) GetStatus() map[string]ModuleStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()

	statusMap := make(map[string]ModuleStatus)
	for name, mm := range s.modules {
		mm.mu.RLock()
		uptimeStr := "0s"
		if mm.state == StateRunning && !mm.startTime.IsZero() {
			uptimeStr = time.Since(mm.startTime).Truncate(time.Second).String()
		}
		errStr := ""
		if mm.lastError != nil {
			errStr = mm.lastError.Error()
		}
		statusMap[name] = ModuleStatus{
			Name:         name,
			State:        mm.state,
			Uptime:       uptimeStr,
			RestartCount: mm.restartCount,
			LastError:    errStr,
			StartTime:    mm.startTime,
		}
		mm.mu.RUnlock()
	}
	return statusMap
}

