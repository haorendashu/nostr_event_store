package cache

import (
	"fmt"
	"testing"
)

// MockWriter implements NodeWriter for testing multi-writer scenarios
type MockWriter struct {
	name     string
	writes   map[uint64]bool // Track which offsets were written
	closed   bool
	pageSize uint32
}

func NewMockWriter(name string) *MockWriter {
	return &MockWriter{
		name:     name,
		writes:   make(map[uint64]bool),
		pageSize: 4096,
	}
}

func (mw *MockWriter) WriteNodePage(offset uint64, buf []byte) error {
	if mw.closed {
		return fmt.Errorf("writer %s is closed", mw.name)
	}
	mw.writes[offset] = true
	return nil
}

func (mw *MockWriter) PageSize() uint32 {
	return mw.pageSize
}

// MockNode implements BTreeNode for testing
type MockNode struct {
	offset uint64
	dirty  bool
	data   []byte
}

func NewMockNode(offset uint64) *MockNode {
	return &MockNode{
		offset: offset,
		dirty:  true,
		data:   make([]byte, 4096),
	}
}

func (mn *MockNode) Offset() uint64 {
	return mn.offset
}

func (mn *MockNode) IsDirty() bool {
	return mn.dirty
}

func (mn *MockNode) SetDirty(dirty bool) {
	mn.dirty = dirty
}

func (mn *MockNode) Serialize(pageSize uint32) ([]byte, error) {
	return mn.data, nil
}

// TestMultiWriterSupportBasic tests that PutWithWriter works correctly
func TestMultiWriterSupportBasic(t *testing.T) {
	writer1 := NewMockWriter("writer1")
	writer2 := NewMockWriter("writer2")

	cache := NewBTreeCacheWithoutWriter(10, 4096)

	// Add nodes with different writers
	node1 := NewMockNode(100)
	node2 := NewMockNode(200)

	err := cache.PutWithWriter(node1, writer1)
	if err != nil {
		t.Fatalf("PutWithWriter for writer1 failed: %v", err)
	}

	err = cache.PutWithWriter(node2, writer2)
	if err != nil {
		t.Fatalf("PutWithWriter for writer2 failed: %v", err)
	}

	// Verify both nodes are in cache
	retrieved1, ok := cache.Get(100)
	if !ok {
		t.Fatalf("Failed to retrieve node 100 from cache")
	}
	if retrieved1.Offset() != 100 {
		t.Errorf("Retrieved node offset: %d, expected: 100", retrieved1.Offset())
	}

	retrieved2, ok := cache.Get(200)
	if !ok {
		t.Fatalf("Failed to retrieve node 200 from cache")
	}
	if retrieved2.Offset() != 200 {
		t.Errorf("Retrieved node offset: %d, expected: 200", retrieved2.Offset())
	}

	t.Log("✅ TestMultiWriterSupportBasic passed")
}

// TestMultiWriterEvictionUsesCorrectWriter tests that eviction uses the correct writer per entry
func TestMultiWriterEvictionUsesCorrectWriter(t *testing.T) {
	writer1 := NewMockWriter("writer1")
	writer2 := NewMockWriter("writer2")

	// Small cache (capacity of 5 entries)
	cache := NewBTreeCacheWithoutWriter(0, 4096)
	cache.capacity = 5

	// Add nodes from different writers
	node1 := NewMockNode(100)
	err := cache.PutWithWriter(node1, writer1)
	if err != nil {
		t.Fatalf("PutWithWriter failed: %v", err)
	}

	node2 := NewMockNode(200)
	err = cache.PutWithWriter(node2, writer2)
	if err != nil {
		t.Fatalf("PutWithWriter failed: %v", err)
	}

	// Fill the cache to trigger eviction
	for i := 3; i < 8; i++ {
		node := NewMockNode(uint64(i * 100))
		// Alternate between writers
		writer := writer1
		if i%2 == 0 {
			writer = writer2
		}
		err := cache.PutWithWriter(node, writer)
		if err != nil {
			t.Fatalf("PutWithWriter for node %d failed: %v", i*100, err)
		}
	}

	// Verify that writer1 received the write for node1 (offset 100)
	// This should have been evicted and written by writer1
	if !writer1.writes[100] {
		t.Errorf("writer1 was not called to write node 100 (writer1.writes: %v)", writer1.writes)
	}

	// Verify that writer2 received the write for node2 (offset 200)
	if !writer2.writes[200] {
		t.Errorf("writer2 was not called to write node 200 (writer2.writes: %v)", writer2.writes)
	}

	t.Log("✅ TestMultiWriterEvictionUsesCorrectWriter passed")
}

// TestMultiWriterFlushDirtyUsesCorrectWriter tests that FlushDirty uses correct writers
func TestMultiWriterFlushDirtyUsesCorrectWriter(t *testing.T) {
	writer1 := NewMockWriter("writer1")
	writer2 := NewMockWriter("writer2")

	cache := NewBTreeCacheWithoutWriter(10, 4096)

	// Add dirty nodes from different writers
	node1 := NewMockNode(100)
	err := cache.PutWithWriter(node1, writer1)
	if err != nil {
		t.Fatalf("PutWithWriter failed: %v", err)
	}

	node2 := NewMockNode(200)
	err = cache.PutWithWriter(node2, writer2)
	if err != nil {
		t.Fatalf("PutWithWriter failed: %v", err)
	}

	// Flush all dirty pages
	flushed, err := cache.FlushDirty()
	if err != nil {
		t.Fatalf("FlushDirty failed: %v", err)
	}

	if flushed != 2 {
		t.Errorf("Expected 2 pages flushed, got %d", flushed)
	}

	// Verify each writer received the correct write
	if !writer1.writes[100] {
		t.Errorf("writer1 should have written node 100")
	}

	if !writer2.writes[200] {
		t.Errorf("writer2 should have written node 200")
	}

	t.Log("✅ TestMultiWriterFlushDirtyUsesCorrectWriter passed")
}

// TestBackwardCompatibilityWithSetWriter tests that old code using SetWriter still works
func TestBackwardCompatibilityWithSetWriter(t *testing.T) {
	writer := NewMockWriter("global_writer")

	cache := NewBTreeCacheWithoutWriter(10, 4096)
	cache.SetWriter(writer)

	// Old code that uses Put() without specifying writer
	node := NewMockNode(100)
	err := cache.Put(node)
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}

	// Retrieve it
	retrieved, ok := cache.Get(100)
	if !ok {
		t.Fatalf("Failed to retrieve node")
	}

	if retrieved.Offset() != 100 {
		t.Errorf("Expected offset 100, got %d", retrieved.Offset())
	}

	// Flush and verify global writer was used
	flushed, err := cache.FlushDirty()
	if err != nil {
		t.Fatalf("FlushDirty failed: %v", err)
	}

	if flushed != 1 {
		t.Errorf("Expected 1 page flushed, got %d", flushed)
	}

	if !writer.writes[100] {
		t.Errorf("Global writer should have written node 100")
	}

	t.Log("✅ TestBackwardCompatibilityWithSetWriter passed")
}

// TestMixedWriterScenario tests mixed usage of Put and PutWithWriter
func TestMixedWriterScenario(t *testing.T) {
	globalWriter := NewMockWriter("global")
	writer1 := NewMockWriter("writer1")
	writer2 := NewMockWriter("writer2")

	cache := NewBTreeCacheWithoutWriter(10, 4096)
	cache.SetWriter(globalWriter)

	// Mix old-style Put() and new-style PutWithWriter()
	node0 := NewMockNode(0)
	cache.Put(node0) // Uses global writer

	node1 := NewMockNode(100)
	cache.PutWithWriter(node1, writer1)

	node2 := NewMockNode(200)
	cache.Put(node2) // Uses global writer

	node3 := NewMockNode(300)
	cache.PutWithWriter(node3, writer2)

	// Flush all
	flushed, err := cache.FlushDirty()
	if err != nil {
		t.Fatalf("FlushDirty failed: %v", err)
	}

	if flushed != 4 {
		t.Errorf("Expected 4 pages flushed, got %d", flushed)
	}

	// Verify each writer got the correct nodes
	if !globalWriter.writes[0] {
		t.Errorf("Global writer should have written node 0")
	}
	if !writer1.writes[100] {
		t.Errorf("writer1 should have written node 100")
	}
	if !globalWriter.writes[200] {
		t.Errorf("Global writer should have written node 200")
	}
	if !writer2.writes[300] {
		t.Errorf("writer2 should have written node 300")
	}

	t.Log("✅ TestMixedWriterScenario passed")
}
