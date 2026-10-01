package health

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"companion/pkg/config"
	"companion/pkg/logging"
	"companion/pkg/supervisor"
)

type SystemStats struct {
	CPULoad     float64   `json:"cpu_load"`
	MemoryUsed  uint64    `json:"memory_used_mb"`
	MemoryTotal uint64    `json:"memory_total_mb"`
	MemPercent  float64   `json:"memory_percent"`
	Temperature float64   `json:"temperature_celsius"`
	Timestamp   time.Time `json:"timestamp"`
}

type HealthModule struct {
	cfg        config.HealthConfig
	logger     *logging.LogManager
	mu         sync.RWMutex
	lastStats  SystemStats
	watchdogFd *os.File
}

func NewHealthModule(cfg config.HealthConfig, logger *logging.LogManager) *HealthModule {
	return &HealthModule{
		cfg:    cfg,
		logger: logger,
	}
}

func (h *HealthModule) Name() string {
	return "health"
}

func (h *HealthModule) Start(ctx context.Context) error {
	h.logger.Log("health", logging.LevelInfo, "Health & Recovery module started")

	if h.cfg.EnableWatchdog {
		h.initWatchdog()
	}

	interval := h.cfg.PollInterval
	if interval < 1*time.Second {
		interval = 1 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.closeWatchdog()
			h.logger.Log("health", logging.LevelInfo, "Health module stopping")
			return nil
		case <-ticker.C:
			h.updateStats()
			h.pingWatchdog()
		}
	}
}

func (h *HealthModule) initWatchdog() {
	device := h.cfg.WatchdogDevice
	if device == "" {
		device = "/dev/watchdog"
	}
	f, err := os.OpenFile(device, os.O_WRONLY, 0)
	if err != nil {
		h.logger.Log("health", logging.LevelWarn, fmt.Sprintf("Failed to open watchdog %s: %v", device, err))
		return
	}
	h.watchdogFd = f
	h.logger.RecordEvent("health", "WATCHDOG_ENABLE", fmt.Sprintf("Hardware watchdog enabled on %s", device))
}

func (h *HealthModule) pingWatchdog() {
	if h.watchdogFd != nil {
		_, _ = h.watchdogFd.WriteString("\x00")
	}
}

func (h *HealthModule) closeWatchdog() {
	if h.watchdogFd != nil {
		_, _ = h.watchdogFd.WriteString("V")
		_ = h.watchdogFd.Close()
		h.watchdogFd = nil
	}
}

func (h *HealthModule) updateStats() {
	stats := SystemStats{
		Timestamp: time.Now().UTC(),
	}

	stats.CPULoad = readCPULoad()
	stats.MemoryTotal, stats.MemoryUsed, stats.MemPercent = readMemoryStats()
	stats.Temperature = readSoCTemperature()

	h.mu.Lock()
	h.lastStats = stats
	h.mu.Unlock()
}

func (h *HealthModule) GetStats() SystemStats {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastStats
}

func (h *HealthModule) TriggerReboot() error {
	h.logger.RecordEvent("health", "SYSTEM_REBOOT", "System reboot requested via API")
	return sysReboot()
}

func (h *HealthModule) TriggerShutdown() error {
	h.logger.RecordEvent("health", "SYSTEM_SHUTDOWN", "System shutdown requested via API")
	return sysShutdown()
}

func (h *HealthModule) Stop() error {
	h.closeWatchdog()
	return nil
}

func (h *HealthModule) Status() supervisor.ModuleStatus {
	return supervisor.ModuleStatus{
		Name:  h.Name(),
		State: supervisor.StateRunning,
	}
}

func readCPULoad() float64 {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0.0
	}
	fields := strings.Fields(string(data))
	if len(fields) > 0 {
		val, _ := strconv.ParseFloat(fields[0], 64)
		return val
	}
	return 0.0
}

func readMemoryStats() (totalMB uint64, usedMB uint64, percent float64) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		var memStats runtime.MemStats
		runtime.ReadMemStats(&memStats)
		total := memStats.Sys / 1024 / 1024
		used := memStats.Alloc / 1024 / 1024
		pct := 0.0
		if total > 0 {
			pct = (float64(used) / float64(total)) * 100
		}
		return total, used, pct
	}

	lines := strings.Split(string(data), "\n")
	var memTotal, memAvailable uint64
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			switch fields[0] {
			case "MemTotal:":
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				memTotal = v / 1024
			case "MemAvailable:":
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				memAvailable = v / 1024
			}
		}
	}
	if memTotal > 0 {
		memUsed := memTotal - memAvailable
		pct := (float64(memUsed) / float64(memTotal)) * 100.0
		return memTotal, memUsed, pct
	}
	return 0, 0, 0.0
}

func readSoCTemperature() float64 {
	thermalPaths := []string{
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/thermal/thermal_zone1/temp",
	}

	for _, p := range thermalPaths {
		data, err := os.ReadFile(p)
		if err == nil {
			str := strings.TrimSpace(string(data))
			val, err := strconv.ParseFloat(str, 64)
			if err == nil {
				if val > 1000 {
					val /= 1000.0
				}
				return val
			}
		}
	}
	return 0.0
}

