package wal

import (
	"context"
	"strings"
	"testing"

	"nostr_event_store/src/storage"
	"nostr_event_store/src/types"
)

// mockReplayer implements the Replayer interface for testing.
type mockReplayer struct {
	inserts      []*types.Event
	updates      []types.RecordLocation
	indexUpdates []struct{ key, value []byte }
	checkpoints  []Checkpoint
	errors       []error
}

func (m *mockReplayer) OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error {
	m.inserts = append(m.inserts, event)
	return nil
}

func (m *mockReplayer) OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
	m.updates = append(m.updates, location)
	return nil
}

func (m *mockReplayer) OnIndexUpdate(ctx context.Context, key []byte, value []byte) error {
	m.indexUpdates = append(m.indexUpdates, struct{ key, value []byte }{key, value})
	return nil
}

func (m *mockReplayer) OnCheckpoint(ctx context.Context, checkpoint Checkpoint) error {
	m.checkpoints = append(m.checkpoints, checkpoint)
	return nil
}

// TestReplayInsert tests replaying OpTypeInsert entries.
func TestReplayInsert(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create and write events to WAL
	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(ctx, cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}

	// Serialize and write test events
	serializer := storage.NewTLVSerializer(4096)
	testEvents := []*types.Event{
		{
			ID:        [32]byte{1},
			Pubkey:    [32]byte{2},
			CreatedAt: 1655000000,
			Kind:      1,
			Tags:      [][]string{{"p", "test"}},
			Content:   "Event 1",
			Sig:       [64]byte{3},
		},
		{
			ID:        [32]byte{4},
			Pubkey:    [32]byte{5},
			CreatedAt: 1655000001,
			Kind:      1,
			Tags:      [][]string{{"e", "event1"}},
			Content:   "Event 2",
			Sig:       [64]byte{6},
		},
	}

	for _, event := range testEvents {
		record, err := serializer.Serialize(event)
		if err != nil {
			t.Fatalf("serialize failed: %v", err)
		}

		entry := &Entry{
			Type:                OpTypeInsert,
			EventDataOrMetadata: record.Data,
		}

		if _, err := writer.Write(ctx, entry); err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}

	writer.Close()

	// Replay WAL
	replayer := &mockReplayer{}
	opts := ReplayOptions{
		StartLSN:    0,
		StopOnError: true,
		Serializer:  serializer,
	}

	stats, err := ReplayFromReader(ctx, tmpDir, replayer, opts)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	// Verify stats
	if stats.EntriesProcessed != 2 {
		t.Errorf("expected 2 entries processed, got %d", stats.EntriesProcessed)
	}
	if stats.InsertsReplayed != 2 {
		t.Errorf("expected 2 inserts replayed, got %d", stats.InsertsReplayed)
	}

	// Verify replayed events
	if len(replayer.inserts) != 2 {
		t.Fatalf("expected 2 inserts, got %d", len(replayer.inserts))
	}

	for i, event := range replayer.inserts {
		if event.Content != testEvents[i].Content {
			t.Errorf("event %d content mismatch: got %s, want %s", i, event.Content, testEvents[i].Content)
		}
		if event.Kind != testEvents[i].Kind {
			t.Errorf("event %d kind mismatch: got %d, want %d", i, event.Kind, testEvents[i].Kind)
		}
	}
}

// TestReplayCheckpoint tests replaying OpTypeCheckpoint entries.
func TestReplayCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create WAL with checkpoint
	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(ctx, cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}

	// Write some entries and checkpoint
	serializer := storage.NewTLVSerializer(4096)
	event := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{2},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags:      [][]string{},
		Content:   "Test",
		Sig:       [64]byte{3},
	}

	record, _ := serializer.Serialize(event)
	entry := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: record.Data,
	}
	writer.Write(ctx, entry)

	// Create checkpoint
	checkpointLSN, err := writer.CreateCheckpoint(ctx)
	if err != nil {
		t.Fatalf("create checkpoint failed: %v", err)
	}

	writer.Close()

	// Replay WAL
	replayer := &mockReplayer{}
	opts := ReplayOptions{
		StartLSN:    0,
		StopOnError: true,
		Serializer:  serializer,
	}

	stats, err := ReplayFromReader(ctx, tmpDir, replayer, opts)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	// Verify checkpoint was replayed
	if stats.CheckpointsReplayed != 1 {
		t.Errorf("expected 1 checkpoint replayed, got %d", stats.CheckpointsReplayed)
	}

	if len(replayer.checkpoints) != 1 {
		t.Fatalf("expected 1 checkpoint, got %d", len(replayer.checkpoints))
	}

	if replayer.checkpoints[0].LSN != checkpointLSN {
		t.Errorf("checkpoint LSN mismatch: got %d, want %d", replayer.checkpoints[0].LSN, checkpointLSN)
	}
}

// TestReplayLargeEvent tests replaying large multi-page events.
func TestReplayLargeEvent(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create WAL
	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(ctx, cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}

	// Create large event
	serializer := storage.NewTLVSerializer(4096)
	largeEvent := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{2},
		CreatedAt: 1655000000,
		Kind:      30023,
		Tags:      [][]string{{"d", "article"}},
		Content:   strings.Repeat("Large content paragraph. ", 500), // ~12KB
		Sig:       [64]byte{3},
	}

	record, err := serializer.Serialize(largeEvent)
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	t.Logf("Large record: length=%d, continued=%v", record.Length, record.Flags.IsContinued())

	entry := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: record.Data,
	}

	if _, err := writer.Write(ctx, entry); err != nil {
		t.Fatalf("write failed: %v", err)
	}

	writer.Close()

	// Replay WAL
	replayer := &mockReplayer{}
	opts := ReplayOptions{
		StartLSN:    0,
		StopOnError: true,
		Serializer:  serializer,
	}

	stats, err := ReplayFromReader(ctx, tmpDir, replayer, opts)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	// Verify large event was replayed correctly
	if stats.InsertsReplayed != 1 {
		t.Errorf("expected 1 insert replayed, got %d", stats.InsertsReplayed)
	}

	if len(replayer.inserts) != 1 {
		t.Fatalf("expected 1 insert, got %d", len(replayer.inserts))
	}

	replayedEvent := replayer.inserts[0]
	if replayedEvent.Content != largeEvent.Content {
		t.Errorf("content length mismatch: got %d, want %d", len(replayedEvent.Content), len(largeEvent.Content))
	}
	if replayedEvent.Kind != largeEvent.Kind {
		t.Errorf("kind mismatch: got %d, want %d", replayedEvent.Kind, largeEvent.Kind)
	}
}

// TestReplayStartLSN tests replaying from a specific LSN.
func TestReplayStartLSN(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create WAL with multiple entries
	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(ctx, cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}

	serializer := storage.NewTLVSerializer(4096)

	// Write 5 events
	for i := 0; i < 5; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i)},
			Pubkey:    [32]byte{byte(i + 1)},
			CreatedAt: uint64(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{},
			Content:   "Event",
			Sig:       [64]byte{byte(i + 2)},
		}

		record, _ := serializer.Serialize(event)
		entry := &Entry{
			Type:                OpTypeInsert,
			EventDataOrMetadata: record.Data,
		}
		writer.Write(ctx, entry)
	}

	writer.Close()

	// Replay from LSN 3 (should skip first 2 entries)
	replayer := &mockReplayer{}
	opts := ReplayOptions{
		StartLSN:    3,
		StopOnError: true,
		Serializer:  serializer,
	}

	stats, err := ReplayFromReader(ctx, tmpDir, replayer, opts)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	// Should process and replay 3 entries (LSN 3, 4, 5)
	// Reader starts at LSN 3, so it doesn't read LSN 1, 2
	if stats.EntriesProcessed != 3 {
		t.Errorf("expected 3 entries processed, got %d", stats.EntriesProcessed)
	}
	if stats.InsertsReplayed != 3 {
		t.Errorf("expected 3 inserts replayed, got %d", stats.InsertsReplayed)
	}
}

// TestReplayErrorHandling tests error handling during replay.
func TestReplayErrorHandling(t *testing.T) {
	tmpDir := t.TempDir()
	ctx := context.Background()

	// Create WAL with some entries
	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(ctx, cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}

	serializer := storage.NewTLVSerializer(4096)

	// Write valid event
	event := &types.Event{
		ID:        [32]byte{1},
		Pubkey:    [32]byte{2},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags:      [][]string{},
		Content:   "Valid",
		Sig:       [64]byte{3},
	}

	record, _ := serializer.Serialize(event)
	entry := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: record.Data,
	}
	writer.Write(ctx, entry)

	// Write invalid entry (corrupted data)
	invalidEntry := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: []byte{1, 2, 3}, // Too short to be valid
	}
	writer.Write(ctx, invalidEntry)

	// Write another valid event
	writer.Write(ctx, entry)

	writer.Close()

	// Replay with StopOnError = false (continue on errors)
	replayer := &mockReplayer{}
	opts := ReplayOptions{
		StartLSN:    0,
		StopOnError: false,
		Serializer:  serializer,
	}

	stats, err := ReplayFromReader(ctx, tmpDir, replayer, opts)
	if err != nil {
		t.Fatalf("replay failed: %v", err)
	}

	// Should process 3 entries, replay 2 valid ones, and collect 1 error
	if stats.EntriesProcessed != 3 {
		t.Errorf("expected 3 entries processed, got %d", stats.EntriesProcessed)
	}
	if stats.InsertsReplayed != 2 {
		t.Errorf("expected 2 inserts replayed, got %d", stats.InsertsReplayed)
	}
	if len(stats.Errors) != 1 {
		t.Errorf("expected 1 error, got %d", len(stats.Errors))
	}
}
