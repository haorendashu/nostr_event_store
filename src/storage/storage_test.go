// Package storage implements tests for multi-page record storage.
package storage

import (
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"nostr_event_store/src/types"
)

// TestSerializerSmallEvent tests serialization of a small event (single-page).
func TestSerializerSmallEvent(t *testing.T) {
	serializer := NewTLVSerializer(4096)

	event := &types.Event{
		ID:        [32]byte{0x01},
		Pubkey:    [32]byte{0x02},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags: [][]string{
			{"e", "abc123"},
			{"p", "def456"},
		},
		Content: "Hello, Nostr!",
		Sig:     [64]byte{0x03},
	}

	record, err := serializer.Serialize(event)
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	if record.Flags.IsContinued() {
		t.Error("small event should not be continued")
	}

	if record.ContinuationCount != 0 {
		t.Errorf("continuation count should be 0, got %d", record.ContinuationCount)
	}

	// Deserialize and verify
	decoded, err := serializer.Deserialize(record)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if decoded.Kind != event.Kind {
		t.Errorf("kind mismatch: got %d, want %d", decoded.Kind, event.Kind)
	}

	if decoded.Content != event.Content {
		t.Errorf("content mismatch: got %s, want %s", decoded.Content, event.Content)
	}
}

// TestSerializerLargeEvent tests serialization of a large event (multi-page).
func TestSerializerLargeEvent(t *testing.T) {
	serializer := NewTLVSerializer(4096)

	// Create a large content (12KB)
	largeContent := strings.Repeat("This is a long-form article about Nostr. ", 300) // ~12KB

	event := &types.Event{
		ID:        [32]byte{0x01},
		Pubkey:    [32]byte{0x02},
		CreatedAt: 1655000000,
		Kind:      30023, // Long-form article
		Tags: [][]string{
			{"d", "my-article"},
			{"title", "A Long Article"},
		},
		Content: largeContent,
		Sig:     [64]byte{0x03},
	}

	record, err := serializer.Serialize(event)
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	if !record.Flags.IsContinued() {
		t.Error("large event should be continued")
	}

	if record.ContinuationCount == 0 {
		t.Error("continuation count should be > 0")
	}

	t.Logf("Large event: length=%d, continuation_count=%d", record.Length, record.ContinuationCount)

	// Deserialize and verify
	decoded, err := serializer.Deserialize(record)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if decoded.Kind != event.Kind {
		t.Errorf("kind mismatch: got %d, want %d", decoded.Kind, event.Kind)
	}

	if decoded.Content != event.Content {
		t.Errorf("content length mismatch: got %d, want %d", len(decoded.Content), len(event.Content))
	}
}

// TestSegmentSinglePage tests writing and reading a single-page record.
func TestSegmentSinglePage(t *testing.T) {
	tmpDir := t.TempDir()
	segPath := filepath.Join(tmpDir, "data.0.seg")

	segment, err := NewFileSegment(0, segPath, 4096, 1024*1024, false)
	if err != nil {
		t.Fatalf("create segment failed: %v", err)
	}
	defer segment.Close()

	serializer := NewTLVSerializer(4096)

	event := &types.Event{
		ID:        [32]byte{0xAB},
		Pubkey:    [32]byte{0xCD},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags:      [][]string{{"p", "test"}},
		Content:   "Short message",
		Sig:       [64]byte{0xEF},
	}

	record, err := serializer.Serialize(event)
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	ctx := context.Background()
	location, err := segment.Append(ctx, record)
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}

	t.Logf("Appended record at segment=%d, offset=%d", location.SegmentID, location.Offset)

	// Read back
	readRecord, err := segment.Read(ctx, location)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if readRecord.Flags.IsContinued() {
		t.Error("read record should not be continued")
	}

	// Deserialize
	decoded, err := serializer.Deserialize(readRecord)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if decoded.Content != event.Content {
		t.Errorf("content mismatch: got %s, want %s", decoded.Content, event.Content)
	}
}

// TestSegmentMultiPage tests writing and reading a multi-page record.
func TestSegmentMultiPage(t *testing.T) {
	tmpDir := t.TempDir()
	segPath := filepath.Join(tmpDir, "data.0.seg")

	segment, err := NewFileSegment(0, segPath, 4096, 10*1024*1024, false)
	if err != nil {
		t.Fatalf("create segment failed: %v", err)
	}
	defer segment.Close()

	serializer := NewTLVSerializer(4096)

	// Create a large event (12KB content)
	largeContent := strings.Repeat("Lorem ipsum dolor sit amet. ", 400)

	event := &types.Event{
		ID:        [32]byte{0x12},
		Pubkey:    [32]byte{0x34},
		CreatedAt: 1655000000,
		Kind:      30023,
		Tags:      [][]string{{"d", "article-1"}, {"title", "Big Article"}},
		Content:   largeContent,
		Sig:       [64]byte{0x56},
	}

	record, err := serializer.Serialize(event)
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	if !record.Flags.IsContinued() {
		t.Error("large record should be continued")
	}

	t.Logf("Multi-page record: length=%d, continuation_count=%d", record.Length, record.ContinuationCount)

	ctx := context.Background()
	location, err := segment.Append(ctx, record)
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}

	t.Logf("Appended multi-page record at segment=%d, offset=%d", location.SegmentID, location.Offset)

	// Read back
	readRecord, err := segment.Read(ctx, location)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	if !readRecord.Flags.IsContinued() {
		t.Error("read record should be continued")
	}

	if readRecord.ContinuationCount != record.ContinuationCount {
		t.Errorf("continuation count mismatch: got %d, want %d",
			readRecord.ContinuationCount, record.ContinuationCount)
	}

	// Deserialize
	decoded, err := serializer.Deserialize(readRecord)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if len(decoded.Content) != len(event.Content) {
		t.Errorf("content length mismatch: got %d, want %d", len(decoded.Content), len(event.Content))
	}

	if decoded.Content != event.Content {
		t.Error("content mismatch after round-trip")
	}
}

// TestScannerSinglePage tests scanning single-page records.
func TestScannerSinglePage(t *testing.T) {
	tmpDir := t.TempDir()
	segPath := filepath.Join(tmpDir, "data.0.seg")

	segment, err := NewFileSegment(0, segPath, 4096, 1024*1024, false)
	if err != nil {
		t.Fatalf("create segment failed: %v", err)
	}
	defer segment.Close()

	serializer := NewTLVSerializer(4096)
	ctx := context.Background()

	// Write 3 small events
	for i := 0; i < 3; i++ {
		event := &types.Event{
			ID:        [32]byte{byte(i)},
			Pubkey:    [32]byte{0x01},
			CreatedAt: uint32(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{{"t", "test"}},
			Content:   "Message " + string(rune('A'+i)),
			Sig:       [64]byte{0x02},
		}

		record, err := serializer.Serialize(event)
		if err != nil {
			t.Fatalf("serialize event %d failed: %v", i, err)
		}

		_, err = segment.Append(ctx, record)
		if err != nil {
			t.Fatalf("append event %d failed: %v", i, err)
		}
	}

	// Scan all records
	scanner := NewScanner(segment)
	count := 0

	for {
		record, location, err := scanner.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("scan failed: %v", err)
		}

		t.Logf("Scanned record %d at offset %d, length=%d", count, location.Offset, record.Length)
		count++
	}

	if count != 3 {
		t.Errorf("expected 3 records, got %d", count)
	}
}

// TestScannerMultiPage tests scanning multi-page records.
func TestScannerMultiPage(t *testing.T) {
	tmpDir := t.TempDir()
	segPath := filepath.Join(tmpDir, "data.0.seg")

	segment, err := NewFileSegment(0, segPath, 4096, 100*1024*1024, false)
	if err != nil {
		t.Fatalf("create segment failed: %v", err)
	}
	defer segment.Close()

	serializer := NewTLVSerializer(4096)
	ctx := context.Background()

	// Write 1 small + 1 large + 1 small (mixed)
	events := []struct {
		content string
		size    string
	}{
		{"Small event 1", "small"},
		{strings.Repeat("Large content. ", 800), "large"},
		{"Small event 2", "small"},
	}

	for i, ev := range events {
		event := &types.Event{
			ID:        [32]byte{byte(i)},
			Pubkey:    [32]byte{0x01},
			CreatedAt: uint32(1655000000 + i),
			Kind:      1,
			Tags:      [][]string{},
			Content:   ev.content,
			Sig:       [64]byte{0x02},
		}

		record, err := serializer.Serialize(event)
		if err != nil {
			t.Fatalf("serialize event %d failed: %v", i, err)
		}

		_, err = segment.Append(ctx, record)
		if err != nil {
			t.Fatalf("append event %d failed: %v", i, err)
		}

		t.Logf("Event %d (%s): length=%d, continued=%v, continuation_count=%d",
			i, ev.size, record.Length, record.Flags.IsContinued(), record.ContinuationCount)
	}

	// Scan all records
	scanner := NewScanner(segment)
	count := 0

	for {
		record, location, err := scanner.Next(ctx)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("scan failed: %v", err)
		}

		decoded, err := serializer.Deserialize(record)
		if err != nil {
			t.Fatalf("deserialize record %d failed: %v", count, err)
		}

		t.Logf("Scanned record %d at offset %d: continued=%v, content_len=%d",
			count, location.Offset, record.Flags.IsContinued(), len(decoded.Content))

		count++
	}

	if count != 3 {
		t.Errorf("expected 3 records, got %d", count)
	}
}

// TestVeryLargeEvent tests a very large event (160KB, like a follow list with 5000 pubkeys).
func TestVeryLargeEvent(t *testing.T) {
	tmpDir := t.TempDir()
	segPath := filepath.Join(tmpDir, "data.0.seg")

	segment, err := NewFileSegment(0, segPath, 4096, 1024*1024*1024, false)
	if err != nil {
		t.Fatalf("create segment failed: %v", err)
	}
	defer segment.Close()

	serializer := NewTLVSerializer(4096)

	// Simulate a follow list with 5000 pubkeys (kind 3)
	tags := make([][]string, 5000)
	for i := 0; i < 5000; i++ {
		// Each "p" tag has a 64-char hex pubkey
		pubkey := strings.Repeat(string(rune('0'+i%10)), 64)
		tags[i] = []string{"p", pubkey}
	}

	event := &types.Event{
		ID:        [32]byte{0xFF},
		Pubkey:    [32]byte{0xEE},
		CreatedAt: 1655000000,
		Kind:      3, // Follow list
		Tags:      tags,
		Content:   "",
		Sig:       [64]byte{0xDD},
	}

	record, err := serializer.Serialize(event)
	if err != nil {
		t.Fatalf("serialize failed: %v", err)
	}

	if !record.Flags.IsContinued() {
		t.Error("very large event should be continued")
	}

	t.Logf("Very large event (5000 tags): length=%d bytes, continuation_count=%d pages",
		record.Length, record.ContinuationCount)

	ctx := context.Background()
	location, err := segment.Append(ctx, record)
	if err != nil {
		t.Fatalf("append failed: %v", err)
	}

	// Read back
	readRecord, err := segment.Read(ctx, location)
	if err != nil {
		t.Fatalf("read failed: %v", err)
	}

	// Deserialize
	decoded, err := serializer.Deserialize(readRecord)
	if err != nil {
		t.Fatalf("deserialize failed: %v", err)
	}

	if len(decoded.Tags) != len(event.Tags) {
		t.Errorf("tags count mismatch: got %d, want %d", len(decoded.Tags), len(event.Tags))
	}

	// Verify a few tags
	for i := 0; i < 10; i++ {
		if len(decoded.Tags[i]) != 2 {
			t.Errorf("tag %d should have 2 elements", i)
		}
		if decoded.Tags[i][0] != "p" {
			t.Errorf("tag %d should be 'p' tag", i)
		}
	}
}

// BenchmarkSmallEventRoundTrip benchmarks small event serialization + segment write/read.
func BenchmarkSmallEventRoundTrip(b *testing.B) {
	tmpDir := b.TempDir()
	segPath := filepath.Join(tmpDir, "bench.seg")

	segment, _ := NewFileSegment(0, segPath, 4096, 1024*1024*1024, false)
	defer segment.Close()

	serializer := NewTLVSerializer(4096)
	ctx := context.Background()

	event := &types.Event{
		ID:        [32]byte{0x01},
		Pubkey:    [32]byte{0x02},
		CreatedAt: 1655000000,
		Kind:      1,
		Tags:      [][]string{{"p", "abc"}},
		Content:   "Hello",
		Sig:       [64]byte{0x03},
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		record, _ := serializer.Serialize(event)
		location, _ := segment.Append(ctx, record)
		_, _ = segment.Read(ctx, location)
	}
}

// BenchmarkLargeEventRoundTrip benchmarks large event (12KB) serialization + segment write/read.
func BenchmarkLargeEventRoundTrip(b *testing.B) {
	tmpDir := b.TempDir()
	segPath := filepath.Join(tmpDir, "bench.seg")

	segment, _ := NewFileSegment(0, segPath, 4096, 1024*1024*1024, false)
	defer segment.Close()

	serializer := NewTLVSerializer(4096)
	ctx := context.Background()

	largeContent := strings.Repeat("Lorem ipsum dolor sit amet. ", 400)

	event := &types.Event{
		ID:        [32]byte{0x01},
		Pubkey:    [32]byte{0x02},
		CreatedAt: 1655000000,
		Kind:      30023,
		Tags:      [][]string{{"d", "article"}},
		Content:   largeContent,
		Sig:       [64]byte{0x03},
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		record, _ := serializer.Serialize(event)
		location, _ := segment.Append(ctx, record)
		_, _ = segment.Read(ctx, location)
	}
}
