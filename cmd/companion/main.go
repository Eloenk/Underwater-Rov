package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"companion/pkg/api"
	"companion/pkg/config"
	"companion/pkg/health"
	"companion/pkg/logging"
	"companion/pkg/mavlink"
	"companion/pkg/supervisor"
	"companion/pkg/video"
)

func main() {
	configPathFlag := flag.String("config", "companion.yaml", "Path to YAML configuration file")
	flag.Parse()

	configPath := *configPathFlag
	if !filepath.IsAbs(configPath) {
		if _, err := os.Stat(configPath); os.IsNotExist(err) {
			alt := filepath.Join("..", "..", configPath)
			if _, err := os.Stat(alt); err == nil {
				configPath = alt
			}
		}
	}

	fmt.Printf("====================================================\n")
	fmt.Printf(" ROV Companion Computer Service (Go Native Daemon)  \n")
	fmt.Printf(" Optimized for Orange Pi 4 Pro & BlueOS Replacement \n")
	fmt.Printf("====================================================\n")
	fmt.Printf("Loading config from: %s\n", configPath)

	cfg, err := config.LoadConfig(configPath)
	if err != nil {
		fmt.Printf("Error loading configuration: %v\n", err)
		os.Exit(1)
	}

	logger := logging.NewLogManager(cfg.Logging.BufferCapacity)
	if cfg.Logging.EventDBPath != "" {
		logger.SetEventDBPath(cfg.Logging.EventDBPath)
	}
	logger.RecordEvent("system", "BOOT", "ROV Companion Computer Service starting up")

	sup := supervisor.NewSupervisor(cfg.Supervisor, logger)

	healthMod := health.NewHealthModule(cfg.Health, logger)
	videoMod := video.NewVideoModule(cfg.Video, logger)
	mavlinkMod := mavlink.NewMAVLinkModule(cfg.MAVLink, logger)

	sup.RegisterModule(healthMod)
	sup.RegisterModule(videoMod)
	sup.RegisterModule(mavlinkMod)

	apiServer := api.NewServer(cfg.Server, sup, logger, healthMod, videoMod, mavlinkMod)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sup.StartAll()

	go func() {
		if err := apiServer.Start(ctx); err != nil {
			logger.Log("system", logging.LevelError, fmt.Sprintf("API server shutdown error: %v", err))
		}
	}()

	fmt.Printf("Service initialized successfully.\n")
	fmt.Printf("Web Dashboard available at: http://0.0.0.0:%d\n", cfg.Server.Port)
	fmt.Printf("Video Stream available at:  http://0.0.0.0:%d/stream\n", cfg.Video.StreamPort)

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	sig := <-sigChan
	logger.RecordEvent("system", "SHUTDOWN_SIGNAL", fmt.Sprintf("Received signal %v, stopping companion service...", sig))

	cancel()
	_ = apiServer.Stop()
	sup.StopAll()

	time.Sleep(500 * time.Millisecond)
	fmt.Println("ROV Companion Computer Service stopped cleanly.")
}
