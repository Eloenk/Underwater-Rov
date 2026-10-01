package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Server     ServerConfig     `yaml:"server"`
	Supervisor SupervisorConfig `yaml:"supervisor"`
	Video      VideoConfig      `yaml:"video"`
	MAVLink    MAVLinkConfig    `yaml:"mavlink"`
	Logging    LoggingConfig    `yaml:"logging"`
	Health     HealthConfig     `yaml:"health"`
	NPUDetection NPUDetectionConfig `yaml:"npu_detection"`
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type SupervisorConfig struct {
	RestartBackoffInitial time.Duration `yaml:"-"`
	RestartBackoffInitialRaw string `yaml:"restart_backoff_initial"`
	RestartBackoffMax     time.Duration `yaml:"-"`
	RestartBackoffMaxRaw string     `yaml:"restart_backoff_max"`
	MaxCrashCount         int           `yaml:"max_crash_count"`
}

type VideoConfig struct {
	DefaultDevice string `yaml:"default_device"`
	WatchDir      string `yaml:"watch_dir"`
	DefaultFormat string `yaml:"default_format"`
	MaxWidth      int    `yaml:"max_width"`
	MaxHeight     int    `yaml:"max_height"`
	MaxFPS        int    `yaml:"max_fps"`
	StreamPort    int    `yaml:"stream_port"`
}

type MAVLinkConfig struct {
	BaudRates        []int    `yaml:"baud_rates"`
	HeartbeatTimeout time.Duration `yaml:"-"`
	HeartbeatTimeoutRaw string `yaml:"heartbeat_timeout"`
	UDPEndpoints     []string `yaml:"udp_endpoints"`
}

type LoggingConfig struct {
	BufferCapacity int    `yaml:"buffer_capacity"`
	EventDBPath    string `yaml:"event_db_path"`
}

type HealthConfig struct {
	PollInterval time.Duration `yaml:"-"`
	PollIntervalRaw string     `yaml:"poll_interval"`
	WatchdogDevice string      `yaml:"watchdog_device"`
	EnableWatchdog bool        `yaml:"enable_watchdog"`
}

type NPUDetectionConfig struct {
	Enabled    bool   `yaml:"enabled"`
	PythonBin  string `yaml:"python_bin"`
	ScriptPath string `yaml:"script_path"`
	WorkingDir string `yaml:"working_dir"`
}

func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", path, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}

	if cfg.Supervisor.RestartBackoffInitialRaw != "" {
		d, err := time.ParseDuration(cfg.Supervisor.RestartBackoffInitialRaw)
		if err == nil {
			cfg.Supervisor.RestartBackoffInitial = d
		} else {
			cfg.Supervisor.RestartBackoffInitial = 1 * time.Second
		}
	} else {
		cfg.Supervisor.RestartBackoffInitial = 1 * time.Second
	}

	if cfg.Supervisor.RestartBackoffMaxRaw != "" {
		d, err := time.ParseDuration(cfg.Supervisor.RestartBackoffMaxRaw)
		if err == nil {
			cfg.Supervisor.RestartBackoffMax = d
		} else {
			cfg.Supervisor.RestartBackoffMax = 30 * time.Second
		}
	} else {
		cfg.Supervisor.RestartBackoffMax = 30 * time.Second
	}

	if cfg.MAVLink.HeartbeatTimeoutRaw != "" {
		d, err := time.ParseDuration(cfg.MAVLink.HeartbeatTimeoutRaw)
		if err == nil {
			cfg.MAVLink.HeartbeatTimeout = d
		} else {
			cfg.MAVLink.HeartbeatTimeout = 3 * time.Second
		}
	} else {
		cfg.MAVLink.HeartbeatTimeout = 3 * time.Second
	}

	if cfg.Health.PollIntervalRaw != "" {
		d, err := time.ParseDuration(cfg.Health.PollIntervalRaw)
		if err == nil {
			cfg.Health.PollInterval = d
		} else {
			cfg.Health.PollInterval = 2 * time.Second
		}
	} else {
		cfg.Health.PollInterval = 2 * time.Second
	}

	return &cfg, nil
}
