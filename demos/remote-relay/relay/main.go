package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/relayer/v2"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

type Relay struct {
	storage *NostrEventStorage
}

func (r *Relay) Name() string {
	return "RemoteRelay"
}

func (r *Relay) Storage(ctx context.Context) eventstore.Store {
	return r.storage
}

func (r *Relay) Init() error {
	err := envconfig.Process("", r)
	if err != nil {
		return fmt.Errorf("couldn't process envconfig: %w", err)
	}

	return nil
}

func (r *Relay) AcceptEvent(ctx context.Context, evt *nostr.Event) (bool, string) {
	return true, ""
}

func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:        r.Name(),
		Description: "Relay backed by remote Nostr Event Store over gRPC",
		Software:    "remote-relay",
		Version:     "1.0.0",
		SupportedNIPs: []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10,
			11, 12, 13, 14, 15, 16, 17, 18, 19, 20,
			21, 22, 23, 24, 25, 26, 27, 28, 29, 30,
			31, 32, 33, 34, 35, 36, 37, 38, 39, 40,
			41, 42, 43, 44, 45, 46, 47, 48, 49, 50,
			51, 52, 53, 54, 55, 56, 57, 58, 59, 60,
			61, 62, 63, 64, 65, 66, 67, 68, 69, 70,
			71, 72, 73, 74, 75, 76, 77, 78, 79, 80,
			81, 82, 83, 84, 85, 86, 87, 88, 89, 90,
			91, 92, 93, 94, 95, 96, 97, 98, 99, 100},
	}
}

func main() {
	flags := &CommandLineFlags{}
	flag.StringVar(&flags.ConfigPath, "config", "./config.yaml", "Path to configuration file (YAML format)")
	flag.IntVar(&flags.Port, "port", 7447, "Port for the relay server")
	flag.Parse()

	ctx := context.Background()

	cfg, err := loadConfig(ctx, flags.ConfigPath)
	if err != nil {
		log.Fatalf("failed to load configuration: %v", err)
	}

	r := Relay{}
	if err := envconfig.Process("", &r); err != nil {
		log.Fatalf("failed to read from env: %v", err)
		return
	}

	storage, err := initStore(cfg)
	if err != nil {
		log.Fatalf("failed to create remote storage adapter: %v", err)
	}
	r.storage = storage

	server, err := relayer.NewServer(&r)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	defer r.storage.Close()

	log.Printf("Starting remote relay on %s:%d (event-store=%s)", "0.0.0.0", flags.Port, cfg.RemoteConfig.GRPCListenAddr)
	if err := server.Start("0.0.0.0", flags.Port); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}

func loadConfig(ctx context.Context, configPath string) (*config.Config, error) {
	if _, err := os.Stat(configPath); err != nil {
		if os.IsNotExist(err) {
			log.Printf("Configuration file not found at %s, using default configuration", configPath)
			cfg := config.DefaultConfig()
			cfg.RemoteConfig.GRPCListenAddr = "localhost:50051"
			cfg.RemoteConfig.APIKey = ""
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

	log.Printf("Configuration loaded from %s", configPath)
	return cfg, nil
}
