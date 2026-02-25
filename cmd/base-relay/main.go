package main

import (
	"context"
	"flag"
	"fmt"
	"log"

	"github.com/fiatjaf/eventstore"
	"github.com/fiatjaf/relayer/v2"
	"github.com/kelseyhightower/envconfig"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip11"
)

type Relay struct {
	storage *NostrEventStorage
}

func (r *Relay) Name() string {
	return "BasicRelay"
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

func (r *Relay) AcceptEvent(ctx context.Context, evt *nostr.Event) (
	bool, string,
) {
	// block events that are too large
	// jsonb, _ := json.Marshal(evt)
	// if len(jsonb) > 10000 {
	// 	return false, "event is too large"
	// }

	return true, ""
}

func (r *Relay) GetNIP11InformationDocument() nip11.RelayInformationDocument {
	return nip11.RelayInformationDocument{
		Name:        r.Name(),
		Description: "Test is a test relay",
		PubKey:      "This is not a pubkey and it may cause your client crash",
		Contact:     "This is not a pubkey and it may cause your client crash",
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
		Software: "Test Relay",
		Version:  "999.999.999",
	}
}

func main() {
	flags := &CommandLineFlags{}
	flag.StringVar(&flags.DataDir, "dataDir", "eventData", "Data directory for event store")
	flag.IntVar(&flags.Port, "port", 7447, "Port for the relay server")
	flag.Parse()

	r := Relay{}
	if err := envconfig.Process("", &r); err != nil {
		log.Fatalf("failed to read from env: %v", err)
		return
	}

	eventStore, err := initStore(flags.DataDir)
	if err != nil {
		log.Fatalf("failed to create eventStore %v\n", err)
	}
	r.storage = eventStore

	server, err := relayer.NewServer(&r)
	if err != nil {
		log.Fatalf("failed to create server: %v", err)
	}
	defer func() {
		r.storage.Close()
	}()
	if err := server.Start("0.0.0.0", flags.Port); err != nil {
		log.Fatalf("server terminated: %v", err)
	}
}
