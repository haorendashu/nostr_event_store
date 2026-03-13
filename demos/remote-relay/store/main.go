package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/remote"
)

func main() {
	configPath := flag.String("config", "./config.yaml", "Path to configuration file (YAML format)")
	flag.Parse()

	ctx := context.Background()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	if cfg.RemoteConfig.Mode == "" || cfg.RemoteConfig.Mode == "local" {
		cfg.RemoteConfig.Mode = "remote"
	}

	if err := ensureDataDirs(cfg); err != nil {
		log.Fatalf("failed to ensure data directories: %v", err)
	}

	listener := remote.NewListener(&remote.ListenerConfig{
		GRPCListenAddr: cfg.RemoteConfig.GRPCListenAddr,
		APIKey:         cfg.RemoteConfig.APIKey,
		Logger:         log.New(os.Stdout, "[STORE] ", log.LstdFlags),
	})

	store := eventstore.New(&eventstore.Options{
		Config:   cfg,
		Listener: listener,
	})
	listener.SetEventStore(store)

	baseDir := cfg.StorageConfig.DataDir
	if err := store.Open(ctx, baseDir, true); err != nil {
		log.Fatalf("failed to open EventStore: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := store.Close(closeCtx); err != nil {
			log.Printf("failed to close EventStore: %v", err)
		}
	}()

	fmt.Printf("[STORE] remote EventStore started\n")
	fmt.Printf("[STORE] gRPC listening on %s\n", cfg.RemoteConfig.GRPCListenAddr)
	fmt.Printf("[STORE] data dir: %s\n", cfg.StorageConfig.DataDir)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("[STORE] shutdown signal received")
}

func loadConfig(configPath string) (*config.Config, error) {
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			log.Printf("configuration file not found at %s, using default config", configPath)
			cfg := config.DefaultConfig()
			cfg.RemoteConfig.Mode = "remote"
			cfg.RemoteConfig.GRPCListenAddr = "localhost:50051"
			return cfg, nil
		}
		return nil, fmt.Errorf("failed to stat config file: %w", err)
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	cfg, err := config.LoadYAML(data)
	if err != nil {
		return nil, fmt.Errorf("failed to parse YAML config: %w", err)
	}

	return cfg, nil
}

func ensureDataDirs(cfg *config.Config) error {
	dirs := []string{
		cfg.StorageConfig.DataDir,
		cfg.IndexConfig.IndexDir,
		cfg.WALConfig.WALDir,
	}

	for _, dir := range dirs {
		if dir == "" {
			continue
		}
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}

	return nil
}
