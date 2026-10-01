package npu

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os/exec"
	"sync"
	"time"

	"companion/pkg/config"
	"companion/pkg/logging"
	"companion/pkg/supervisor"
)

type NPUModule struct {
	cfg       config.NPUDetectionConfig
	logger    *logging.LogManager
	cmd       *exec.Cmd
	mu        sync.RWMutex
	startTime time.Time
}

func NewNPUModule(cfg config.NPUDetectionConfig, logger *logging.LogManager) *NPUModule {
	return &NPUModule{
		cfg:    cfg,
		logger: logger,
	}
}

func (n *NPUModule) Name() string {
	return "npu_detection"
}

func (n *NPUModule) Start(ctx context.Context) error {
	if !n.cfg.Enabled {
		n.logger.Log("npu_detection", logging.LevelInfo, "NPU Detection module disabled in configuration")
		<-ctx.Done()
		return nil
	}

	pythonBin := n.cfg.PythonBin
	if pythonBin == "" {
		pythonBin = "python"
	}

	n.logger.Log("npu_detection", logging.LevelInfo, fmt.Sprintf("Launching NPU Detection pipeline: %s %s", pythonBin, n.cfg.ScriptPath))

	cmd := exec.CommandContext(ctx, pythonBin, n.cfg.ScriptPath)
	if n.cfg.WorkingDir != "" {
		cmd.Dir = n.cfg.WorkingDir
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to open stdout pipe: %w", err)
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to open stderr pipe: %w", err)
	}

	n.mu.Lock()
	n.cmd = cmd
	n.startTime = time.Now()
	n.mu.Unlock()

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start Python NPU process: %w", err)
	}

	go n.pipeReader(stdout, logging.LevelInfo)
	go n.pipeReader(stderr, logging.LevelWarn)

	err = cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("python NPU process exited with error: %w", err)
	}

	return nil
}

func (n *NPUModule) pipeReader(r io.Reader, level logging.LogLevel) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		text := scanner.Text()
		if text != "" {
			n.logger.Log("npu_detection", level, text)
		}
	}
}

func (n *NPUModule) Stop() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	if n.cmd != nil && n.cmd.Process != nil {
		n.logger.Log("npu_detection", logging.LevelInfo, "Stopping NPU Detection process...")
		_ = n.cmd.Process.Kill()
	}
	return nil
}

func (n *NPUModule) Status() supervisor.ModuleStatus {
	n.mu.RLock()
	defer n.mu.RUnlock()

	state := supervisor.StateStopped
	uptime := "0s"
	if n.cmd != nil && n.cmd.Process != nil {
		state = supervisor.StateRunning
		uptime = time.Since(n.startTime).Truncate(time.Second).String()
	}

	return supervisor.ModuleStatus{
		Name:      n.Name(),
		State:     state,
		Uptime:    uptime,
		StartTime: n.startTime,
	}
}

