// Package wal implements tests for WAL functionality with multi-page record support.
package wal

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/haorendashu/nostr_event_store/src/storage"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestFileWriterBasic tests basic WAL writing.
func TestFileWriterBasic(t *testing.T) {
	tmpDir := t.TempDir()

	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(context.Background(), cfg); err != nil {
		t.Fatalf("open failed: %v", err)
	}
	defer writer.Close()

	// Write an entry
	entry := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: []byte("test event data"),
	}

	lsn, err := writer.Write(context.Background(), entry)
	if err != nil {
		t.Fatalf("write failed: %v", err)
	}

	t.Logf("Written entry with LSN %d", lsn)

	if lsn != 1 {
		t.Errorf("expected LSN 1, got %d", lsn)
	}

	if writer.LastLSN() != 1 {
		t.Fatalf("LastLSN should be 1, got %d", writer.LastLSN())
	}
}

// TestFileWriterMultipleEntries tests writing multiple entries.
func TestFileWriterMultipleEntries(t *testing.T) {
	tmpDir := t.TempDir()

	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(context.Background(), cfg); err != nil {
		t.Fatalf("open failed: %v", err)
	}
	defer writer.Close()

	// Write 3 entries
	for i := 0; i < 3; i++ {
		entry := &Entry{
			Type:                OpTypeInsert,
			EventDataOrMetadata: []byte("event " + string(rune('0'+i))),
		}

		lsn, err := writer.Write(context.Background(), entry)
		if err != nil {
			t.Fatalf("write %d failed: %v", i, err)
		}

		if lsn != uint64(i+1) {
			t.Errorf("entry %d: expected LSN %d, got %d", i, i+1, lsn)
		}
	}

	if writer.LastLSN() != 3 {
		t.Errorf("expected LastLSN 3, got %d", writer.LastLSN())
	}
}

// TestFileReaderBasic tests basic WAL reading.
func TestFileReaderBasic(t *testing.T) {
	tmpDir := t.TempDir()

	// Write entries
	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(context.Background(), cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}

	testData := []string{"event 1", "event 2", "event 3"}
	for _, data := range testData {
		entry := &Entry{
			Type:                OpTypeInsert,
			EventDataOrMetadata: []byte(data),
		}
		_, err := writer.Write(context.Background(), entry)
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}

	writer.Close()

	// Read entries back
	reader := NewFileReader()
	if err := reader.Open(context.Background(), tmpDir, 0); err != nil {
		t.Fatalf("open reader failed: %v", err)
	}
	defer reader.Close()

	count := 0
	for {
		entry, err := reader.Read(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("read failed: %v", err)
		}

		if count >= len(testData) {
			t.Fatalf("read more entries than written")
		}

		if string(entry.EventDataOrMetadata) != testData[count] {
			t.Errorf("entry %d: got %s, want %s", count, entry.EventDataOrMetadata, testData[count])
		}

		count++
	}

	if count != len(testData) {
		t.Errorf("expected %d entries, got %d", len(testData), count)
	}
}

// TestWALWithLargeRecord tests WAL with multi-page record data.
func TestWALWithLargeRecord(t *testing.T) {
	tmpDir := t.TempDir()

	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(context.Background(), cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}
	defer writer.Close()

	// Create a large record (12KB, similar to multi-page storage record)
	largeData := make([]byte, 12*1024)
	for i := range largeData {
		largeData[i] = byte('A' + (i % 26))
	}

	entry := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: largeData,
	}

	lsn, err := writer.Write(context.Background(), entry)
	if err != nil {
		t.Fatalf("write large record failed: %v", err)
	}

	t.Logf("Wrote large record (%d bytes) with LSN %d", len(largeData), lsn)

	writer.Close()

	// Read it back
	reader := NewFileReader()
	if err := reader.Open(context.Background(), tmpDir, 0); err != nil {
		t.Fatalf("open reader failed: %v", err)
	}
	defer reader.Close()

	readEntry, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if len(readEntry.EventDataOrMetadata) != len(largeData) {
		t.Errorf("data length mismatch: got %d, want %d", len(readEntry.EventDataOrMetadata), len(largeData))
	}

	if readEntry.LSN != lsn {
		t.Errorf("LSN mismatch: got %d, want %d", readEntry.LSN, lsn)
	}
}

// TestWALIntegrationWithStorage tests WAL integration with storage serializer.
func TestWALIntegrationWithStorage(t *testing.T) {
	tmpDir := t.TempDir()

	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(context.Background(), cfg); err != nil {
		t.Fatalf("open writer failed: %v", err)
	}
	defer writer.Close()

	// Create and serialize a small event
	serializer := storage.NewTLVSerializer(4096)

	smallEvent := &types.Event{
		ID:        [32]byte{0x01},
		Pubkey:    [32]byte{0x02},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags:      [][]string{{"p", "test"}},
		Content:   "Small event",
		Sig:       [64]byte{0x03},
	}

	smallRecord, err := serializer.Serialize(smallEvent)
	if err != nil {
		t.Fatalf("serialize small event failed: %v", err)
	}

	// Write to WAL
	entry1 := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: smallRecord.Data,
	}

	lsn1, err := writer.Write(context.Background(), entry1)
	if err != nil {
		t.Fatalf("write small record to WAL failed: %v", err)
	}

	// Create and serialize a large event
	largeEvent := &types.Event{
		ID:        [32]byte{0x11},
		Pubkey:    [32]byte{0x12},
		CreatedAt: 1655000000,
		Kind:      30023,
		Tags:      [][]string{{"d", "article"}, {"title", "Long Article"}},
		Content:   strings.Repeat("Content paragraph. ", 500),
		Sig:       [64]byte{0x13},
	}

	largeRecord, err := serializer.Serialize(largeEvent)
	if err != nil {
		t.Fatalf("serialize large event failed: %v", err)
	}

	t.Logf("Large record: length=%d, continued=%v, continuation_count=%d",
		largeRecord.Length, largeRecord.Flags.IsContinued(), largeRecord.ContinuationCount)

	// Write to WAL
	entry2 := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: largeRecord.Data,
	}

	lsn2, err := writer.Write(context.Background(), entry2)
	if err != nil {
		t.Fatalf("write large record to WAL failed: %v", err)
	}

	writer.Close()

	// Read them back and verify
	reader := NewFileReader()
	if err := reader.Open(context.Background(), tmpDir, 0); err != nil {
		t.Fatalf("open reader failed: %v", err)
	}
	defer reader.Close()

	// Read first entry
	readEntry1, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("read first entry failed: %v", err)
	}

	if readEntry1.LSN != lsn1 {
		t.Errorf("first entry LSN mismatch: got %d, want %d", readEntry1.LSN, lsn1)
	}

	if len(readEntry1.EventDataOrMetadata) != len(smallRecord.Data) {
		t.Errorf("first entry data mismatch: got %d bytes, want %d bytes",
			len(readEntry1.EventDataOrMetadata), len(smallRecord.Data))
	}

	// Read second entry
	readEntry2, err := reader.Read(context.Background())
	if err != nil {
		t.Fatalf("read second entry failed: %v", err)
	}

	if readEntry2.LSN != lsn2 {
		t.Errorf("second entry LSN mismatch: got %d, want %d", readEntry2.LSN, lsn2)
	}

	if len(readEntry2.EventDataOrMetadata) != len(largeRecord.Data) {
		t.Errorf("second entry data mismatch: got %d bytes, want %d bytes",
			len(readEntry2.EventDataOrMetadata), len(largeRecord.Data))
	}

	// Deserialize second entry to verify integrity
	record := &storage.Record{
		Length:            largeRecord.Length,
		Flags:             largeRecord.Flags,
		Data:              readEntry2.EventDataOrMetadata,
		ContinuationCount: largeRecord.ContinuationCount,
	}

	deserializedEvent, err := serializer.Deserialize(record)
	if err != nil {
		t.Fatalf("deserialize large event failed: %v", err)
	}

	if deserializedEvent.Content != largeEvent.Content {
		t.Errorf("content mismatch after WAL round-trip")
	}

	if deserializedEvent.Kind != largeEvent.Kind {
		t.Errorf("kind mismatch: got %d, want %d", deserializedEvent.Kind, largeEvent.Kind)
	}
}

// TestWALCheckpoint tests checkpoint creation.
func TestWALCheckpoint(t *testing.T) {
	tmpDir := t.TempDir()

	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	if err := writer.Open(context.Background(), cfg); err != nil {
		t.Fatalf("open failed: %v", err)
	}
	defer writer.Close()

	// Write some entries
	for i := 0; i < 3; i++ {
		entry := &Entry{
			Type:                OpTypeInsert,
			EventDataOrMetadata: []byte("event"),
		}
		_, err := writer.Write(context.Background(), entry)
		if err != nil {
			t.Fatalf("write failed: %v", err)
		}
	}

	// Create checkpoint
	checkpointLSN, err := writer.CreateCheckpoint(context.Background())
	if err != nil {
		t.Fatalf("checkpoint failed: %v", err)
	}

	if checkpointLSN != 4 {
		t.Errorf("expected checkpoint LSN 4, got %d", checkpointLSN)
	}

	t.Logf("Created checkpoint at LSN %d", checkpointLSN)
}

// BenchmarkWALWrite benchmarks WAL write performance.
func BenchmarkWALWrite(b *testing.B) {
	tmpDir := b.TempDir()

	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	writer.Open(context.Background(), cfg)
	defer writer.Close()

	entry := &Entry{
		Type:                OpTypeInsert,
		EventDataOrMetadata: []byte("test event data"),
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, _ = writer.Write(context.Background(), entry)
	}
}

// BenchmarkWALRead benchmarks WAL read performance.
func BenchmarkWALRead(b *testing.B) {
	tmpDir := b.TempDir()

	// Pre-populate WAL
	writer := NewFileWriter()
	cfg := Config{
		Dir:             tmpDir,
		MaxSegmentSize:  1024 * 1024 * 1024,
		SyncMode:        "always",
		BatchIntervalMs: 100,
		BatchSizeBytes:  10 * 1024 * 1024,
	}

	writer.Open(context.Background(), cfg)

	for i := 0; i < 1000; i++ {
		entry := &Entry{
			Type:                OpTypeInsert,
			EventDataOrMetadata: []byte("event"),
		}
		writer.Write(context.Background(), entry)
	}

	writer.Close()

	// Benchmark reading
	reader := NewFileReader()
	reader.Open(context.Background(), tmpDir, 0)
	defer reader.Close()

	b.ResetTimer()

	count := 0
	for {
		_, err := reader.Read(context.Background())
		if err == io.EOF {
			break
		}
		if err != nil {
			b.Fatalf("read failed: %v", err)
		}
		count++
		if count >= b.N {
			break
		}
	}
}
