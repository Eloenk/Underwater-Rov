package health

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
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
	CPULoad            float64   `json:"cpu_load"`
	MemoryUsed         uint64    `json:"memory_used_mb"`
	MemoryTotal        uint64    `json:"memory_total_mb"`
	MemPercent         float64   `json:"memory_percent"`
	Temperature        float64   `json:"temperature"`
	TemperatureCelsius float64   `json:"temperature_celsius"`
	Timestamp          time.Time `json:"timestamp"`
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

	h.updateStats()

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
	tVal := readSoCTemperature()
	stats.Temperature = tVal
	stats.TemperatureCelsius = tVal

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

var (
	cpuMu     sync.Mutex
	prevTotal uint64
	prevIdle  uint64
)

func readCPULoad() float64 {
	data, err := os.ReadFile("/proc/stat")
	if err != nil {
		return readCPULoadAvg()
	}

	lines := strings.Split(string(data), "\n")
	if len(lines) == 0 {
		return readCPULoadAvg()
	}

	fields := strings.Fields(lines[0])
	if len(fields) < 5 || fields[0] != "cpu" {
		return readCPULoadAvg()
	}

	var total, idle uint64
	for i := 1; i < len(fields); i++ {
		val, _ := strconv.ParseUint(fields[i], 10, 64)
		total += val
		if i == 4 || i == 5 {
			idle += val
		}
	}

	cpuMu.Lock()
	defer cpuMu.Unlock()

	if prevTotal == 0 {
		prevTotal = total
		prevIdle = idle
		return readCPULoadAvg()
	}

	diffTotal := total - prevTotal
	diffIdle := idle - prevIdle
	prevTotal = total
	prevIdle = idle

	if diffTotal == 0 {
		return 0.0
	}

	pct := (float64(diffTotal-diffIdle) / float64(diffTotal)) * 100.0
	return pct
}

func readCPULoadAvg() float64 {
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
	var memTotal, memAvailable, memFree, buffers, cached uint64
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
			case "MemFree:":
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				memFree = v / 1024
			case "Buffers:":
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				buffers = v / 1024
			case "Cached:":
				v, _ := strconv.ParseUint(fields[1], 10, 64)
				cached = v / 1024
			}
		}
	}

	if memTotal > 0 {
		var memUsed uint64
		if memAvailable > 0 {
			memUsed = memTotal - memAvailable
		} else {
			memUsed = memTotal - (memFree + buffers + cached)
		}
		pct := (float64(memUsed) / float64(memTotal)) * 100.0
		return memTotal, memUsed, pct
	}

	return 0, 0, 0.0
}

func readSoCTemperature() float64 {
	thermalPaths := []string{
		"/etc/armbianmonitor/data/cpu_temp",
		"/sys/class/thermal/thermal_zone0/temp",
		"/sys/class/thermal/thermal_zone1/temp",
		"/sys/class/thermal/thermal_zone2/temp",
		"/sys/class/hwmon/hwmon0/temp1_input",
		"/sys/class/hwmon/hwmon1/temp1_input",
		"/sys/class/hwmon/hwmon2/temp1_input",
	}

	matches, _ := filepath.Glob("/sys/class/thermal/thermal_zone*/temp")
	thermalPaths = append(thermalPaths, matches...)

	hwMatches, _ := filepath.Glob("/sys/class/hwmon/hwmon*/temp*_input")
	thermalPaths = append(thermalPaths, hwMatches...)

	for _, p := range thermalPaths {
		data, err := os.ReadFile(p)
		if err == nil {
			str := strings.TrimSpace(string(data))
			val, err := strconv.ParseFloat(str, 64)
			if err == nil {
				if val > 1000 {
					val /= 1000.0
				}
				if val >= 1.0 && val <= 125.0 {
					return val
				}
			}
		}
	}
	return 0.0
}
