package shard

import (
	"crypto/sha256"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// createTestConfig creates a minimal config for testing
func createTestConfig() config.Config {
	cfg := config.DefaultConfig()
	cfg.WALConfig.Disabled = true                       // Disable WAL for faster tests
	cfg.StorageConfig.MaxSegmentSize = 10 * 1024 * 1024 // 10MB segments
	return *cfg
}

// createTestEvent creates a test event with given ID and content
func createTestEvent(idNum int, content string) *types.Event {
	// Create deterministic pubkey based on idNum
	pubkeyHash := sha256.Sum256([]byte(fmt.Sprintf("test-pubkey-%d", idNum)))

	event := &types.Event{
		Pubkey:    pubkeyHash,
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      1,
		Content:   content,
		Tags:      [][]string{},
		Sig:       [64]byte{},
	}

	// Generate deterministic event ID
	idHash := sha256.Sum256([]byte(fmt.Sprintf("test-event-%d-%s", idNum, content)))
	event.ID = idHash

	return event
}

// cleanupTestDir removes test directory
func cleanupTestDir(t *testing.T, dir string) {
	t.Helper()
	if err := os.RemoveAll(dir); err != nil {
		t.Logf("Failed to cleanup test dir %s: %v", dir, err)
	}
}
