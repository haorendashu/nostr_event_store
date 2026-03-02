// Package remote provides gRPC server infrastructure for distributed EventStore.
package remote

import (
	"context"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"

	"google.golang.org/grpc"

	pb "github.com/haorendashu/nostr_event_store/protos"
	"github.com/haorendashu/nostr_event_store/src/recovery"
)

// ListenerConfig holds configuration for the EventStoreListener.
type ListenerConfig struct {
	GRPCListenAddr string
	APIKey         string
	Logger         *log.Logger
}

// EventStoreListener implements eventstore.Listener interface and manages the gRPC server lifecycle.
type EventStoreListener struct {
	config       *ListenerConfig
	server       *grpc.Server
	lis          net.Listener
	store        interface{} // Will hold eventstore.EventStore at runtime (avoiding import cycle)
	addr         string
	mu           sync.RWMutex
	isRunning    bool
	shutdownCh   chan struct{}
	shutdownOnce sync.Once
	serverOnce   sync.Once
}

// NewListener creates a new EventStoreListener that implements eventstore.Listener.
func NewListener(config *ListenerConfig) *EventStoreListener {
	if config.Logger == nil {
		config.Logger = log.New(nil, "", 0)
	}
	addr := config.GRPCListenAddr
	if addr == "" {
		addr = ":50051"
	}
	return &EventStoreListener{
		config:     config,
		addr:       addr,
		shutdownCh: make(chan struct{}),
	}
}

// OnOpened is called when the EventStore is successfully opened.
// Starts the gRPC server in a background goroutine.
func (l *EventStoreListener) OnOpened(ctx context.Context) {
	l.mu.Lock()
	if l.isRunning {
		l.mu.Unlock()
		return
	}
	l.mu.Unlock()

	// Start server in background
	go func() {
		if err := l.start(ctx); err != nil {
			l.config.Logger.Printf("Failed to start gRPC listener: %v", err)
		}
	}()
}

// OnClosed is called when the EventStore is closed.
// Gracefully stops the gRPC server.
func (l *EventStoreListener) OnClosed(ctx context.Context) {
	l.stop()
}

// OnRecoveryStarted is called when crash recovery begins.
func (l *EventStoreListener) OnRecoveryStarted(ctx context.Context) {
	l.config.Logger.Printf("Recovery started")
}

// OnRecoveryCompleted is called when crash recovery finishes.
func (l *EventStoreListener) OnRecoveryCompleted(ctx context.Context, stats *recovery.RecoveryState) {
	if stats != nil {
		l.config.Logger.Printf("Recovery completed: %+v", stats)
	} else {
		l.config.Logger.Printf("Recovery completed")
	}
}

// OnCompactionStarted is called when compaction begins.
func (l *EventStoreListener) OnCompactionStarted(ctx context.Context, segmentID uint32) {
	l.config.Logger.Printf("Compaction started for segment %d", segmentID)
}

// OnCompactionCompleted is called when compaction finishes.
func (l *EventStoreListener) OnCompactionCompleted(ctx context.Context, segmentID uint32) {
	l.config.Logger.Printf("Compaction completed for segment %d", segmentID)
}

// SetEventStore sets the EventStore instance for this listener.
// This should be called after the listener is created but before OnOpened.
func (l *EventStoreListener) SetEventStore(store interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.store = store
}

// OnError is called when an error occurs.
func (l *EventStoreListener) OnError(ctx context.Context, err error) {
	l.config.Logger.Printf("Error: %v", err)
}

// start starts the gRPC server and listens for connections.
func (l *EventStoreListener) start(ctx context.Context) error {
	l.mu.Lock()
	if l.isRunning {
		l.mu.Unlock()
		return fmt.Errorf("listener already running")
	}
	if l.store == nil {
		l.mu.Unlock()
		return fmt.Errorf("store not set")
	}
	store := l.store
	l.mu.Unlock()

	// Parse listen address
	addr := l.addr
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}

	// Create listener socket
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		l.config.Logger.Printf("Failed to listen on %s: %v", addr, err)
		return err
	}

	l.mu.Lock()
	l.lis = lis
	l.mu.Unlock()

	l.config.Logger.Printf("gRPC server listening on %s", addr)

	// Create gRPC server with options
	grpcServer := grpc.NewServer(
		grpc.MaxConcurrentStreams(1000),
	)

	// Register EventStore service
	eventStoreServer := NewServer(store, l.config.APIKey)
	pb.RegisterEventStoreServer(grpcServer, eventStoreServer)

	l.mu.Lock()
	l.server = grpcServer
	l.isRunning = true
	l.mu.Unlock()

	l.config.Logger.Printf("EventStore gRPC server started (auth enabled: %v)", l.config.APIKey != "")

	// Start serving (blocking)
	if err := grpcServer.Serve(lis); err != nil && err != grpc.ErrServerStopped {
		l.config.Logger.Printf("gRPC serve error: %v", err)
		l.mu.Lock()
		l.isRunning = false
		l.mu.Unlock()
	}

	return nil
}

// stop gracefully shuts down the gRPC server.
func (l *EventStoreListener) stop() {
	l.shutdownOnce.Do(func() {
		l.mu.Lock()
		if !l.isRunning || l.server == nil {
			l.mu.Unlock()
			return
		}
		server := l.server
		l.isRunning = false
		l.mu.Unlock()

		l.config.Logger.Printf("Stopping gRPC server...")

		// GracefulStop blocks until all RPCs complete or timeout
		server.GracefulStop()

		l.config.Logger.Printf("gRPC server stopped")

		// Close listener if still open
		l.mu.Lock()
		if l.lis != nil {
			_ = l.lis.Close()
		}
		l.mu.Unlock()

		close(l.shutdownCh)
	})
}
