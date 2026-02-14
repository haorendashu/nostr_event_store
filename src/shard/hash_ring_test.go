package shard

import (
	"fmt"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHashRingBasicOperations tests basic add/remove/get operations
func TestHashRingBasicOperations(t *testing.T) {
	ring := NewHashRing(150)

	// Initially empty
	assert.Equal(t, 0, ring.GetNodeCount())

	// Add nodes
	ring.AddNode("node-a")
	ring.AddNode("node-b")
	ring.AddNode("node-c")

	assert.Equal(t, 3, ring.GetNodeCount())

	// Get node for a key
	key := []byte("test-event-id-12345")
	node, err := ring.GetNode(key)
	require.NoError(t, err)
	assert.Contains(t, []string{"node-a", "node-b", "node-c"}, node)

	// Same key should always map to same node
	for i := 0; i < 10; i++ {
		node2, err := ring.GetNode(key)
		require.NoError(t, err)
		assert.Equal(t, node, node2)
	}
}

// TestHashRingConsistency tests that keys remain on same node after add/remove
func TestHashRingConsistency(t *testing.T) {
	ring := NewHashRing(150)

	// Add initial nodes
	ring.AddNode("node-a")
	ring.AddNode("node-b")

	// Map 100 keys
	testKeys := generateTestKeys(100)
	initialMapping := make(map[string]string) // key -> node

	for _, key := range testKeys {
		node, err := ring.GetNode(key)
		require.NoError(t, err)
		initialMapping[string(key)] = node
	}

	// Add a new node
	ring.AddNode("node-c")

	// Check how many keys moved
	movedCount := 0
	for _, key := range testKeys {
		node, err := ring.GetNode(key)
		require.NoError(t, err)
		if node != initialMapping[string(key)] {
			movedCount++
		}
	}

	// With consistent hashing, adding 1 node to 2 nodes should move ~33% of keys
	// Allow some variance (20-50%)
	movedPercent := float64(movedCount) / float64(len(testKeys)) * 100
	t.Logf("Keys moved after adding node: %d/%d (%.1f%%)", movedCount, len(testKeys), movedPercent)

	assert.True(t, movedPercent >= 20 && movedPercent <= 50,
		"Expected 20-50%% of keys to move, got %.1f%%", movedPercent)
}

// TestHashRingDistribution tests that keys are evenly distributed across nodes
func TestHashRingDistribution(t *testing.T) {
	tests := []struct {
		name         string
		numNodes     int
		numKeys      int
		virtualNodes int
		maxVariance  float64 // maximum allowed variance from perfect distribution
	}{
		{
			name:         "2 nodes, 1000 keys, 50 vnodes",
			numNodes:     2,
			numKeys:      1000,
			virtualNodes: 50,
			maxVariance:  20.0, // allow 20% variance
		},
		{
			name:         "4 nodes, 10000 keys, 150 vnodes",
			numNodes:     4,
			numKeys:      10000,
			virtualNodes: 150,
			maxVariance:  18.0, // allow 18% variance
		},
		{
			name:         "8 nodes, 10000 keys, 300 vnodes",
			numNodes:     8,
			numKeys:      10000,
			virtualNodes: 300,
			maxVariance:  18.0, // allow 18% variance
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ring := NewHashRing(tt.virtualNodes)

			// Add nodes
			for i := 0; i < tt.numNodes; i++ {
				ring.AddNode(fmt.Sprintf("node-%d", i))
			}

			// Generate test keys
			testKeys := generateTestKeys(tt.numKeys)

			// Get distribution
			distribution := ring.GetDistribution(testKeys)

			// Calculate statistics
			expectedPerNode := tt.numKeys / tt.numNodes
			maxDeviation := 0.0

			t.Logf("Distribution for %d nodes, %d keys:", tt.numNodes, tt.numKeys)
			for i := 0; i < tt.numNodes; i++ {
				nodeID := fmt.Sprintf("node-%d", i)
				count := distribution[nodeID]
				deviation := math.Abs(float64(count-expectedPerNode)) / float64(expectedPerNode) * 100
				maxDeviation = math.Max(maxDeviation, deviation)
				t.Logf("  %s: %d keys (%.1f%% of expected)", nodeID, count, 100.0*float64(count)/float64(expectedPerNode))
			}

			t.Logf("  Max deviation: %.1f%%", maxDeviation)

			assert.True(t, maxDeviation <= tt.maxVariance,
				"Max deviation %.1f%% exceeds allowed variance %.1f%%",
				maxDeviation, tt.maxVariance)
		})
	}
}

// TestHashRingRemoveNode tests node removal
func TestHashRingRemoveNode(t *testing.T) {
	ring := NewHashRing(150)

	// Add nodes
	ring.AddNode("node-a")
	ring.AddNode("node-b")
	ring.AddNode("node-c")

	assert.Equal(t, 3, ring.GetNodeCount())

	// Remove a node
	ring.RemoveNode("node-b")

	assert.Equal(t, 2, ring.GetNodeCount())

	// All keys should now map to node-a or node-c
	testKeys := generateTestKeys(100)
	for _, key := range testKeys {
		node, err := ring.GetNode(key)
		require.NoError(t, err)
		assert.Contains(t, []string{"node-a", "node-c"}, node)
	}
}

// TestHashRingEmpty tests behavior with empty ring
func TestHashRingEmpty(t *testing.T) {
	ring := NewHashRing(150)

	key := []byte("test-key")
	_, err := ring.GetNode(key)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "empty")
}

// TestHashRingSingleNode tests single-node ring
func TestHashRingSingleNode(t *testing.T) {
	ring := NewHashRing(150)
	ring.AddNode("node-a")

	testKeys := generateTestKeys(100)
	for _, key := range testKeys {
		node, err := ring.GetNode(key)
		require.NoError(t, err)
		assert.Equal(t, "node-a", node)
	}
}

// TestHashRingVirtualNodeCount tests different virtual node counts
func TestHashRingVirtualNodeCount(t *testing.T) {
	tests := []struct {
		virtualNodes int
		expectTotal  int // expected total hashes with 2 nodes
	}{
		{50, 100},
		{150, 300},
		{300, 600},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("vnodes=%d", tt.virtualNodes), func(t *testing.T) {
			ring := NewHashRing(tt.virtualNodes)
			ring.AddNode("node-a")
			ring.AddNode("node-b")

			stats := ring.GetStats()
			assert.Equal(t, 2, stats.PhysicalNodes)
			assert.Equal(t, tt.virtualNodes, stats.VirtualNodes)
			assert.Equal(t, tt.expectTotal, stats.TotalHashes)
		})
	}
}

// TestHashRingGetNodes tests retrieving all nodes
func TestHashRingGetNodes(t *testing.T) {
	ring := NewHashRing(150)

	ring.AddNode("node-c")
	ring.AddNode("node-a")
	ring.AddNode("node-b")

	nodes := ring.GetNodes()
	assert.Equal(t, []string{"node-a", "node-b", "node-c"}, nodes) // Should be sorted
}

// BenchmarkHashRingGetNode benchmarks node lookup performance
func BenchmarkHashRingGetNode(b *testing.B) {
	ring := NewHashRing(150)

	// Add 4 nodes
	for i := 0; i < 4; i++ {
		ring.AddNode(fmt.Sprintf("node-%d", i))
	}

	key := []byte("benchmark-event-id-12345678901234567890")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = ring.GetNode(key)
	}
}

// BenchmarkHashRingAddNode benchmarks node addition performance
func BenchmarkHashRingAddNode(b *testing.B) {
	for i := 0; i < b.N; i++ {
		ring := NewHashRing(150)
		ring.AddNode(fmt.Sprintf("node-%d", i))
	}
}

// Helper: generateTestKeys creates test event IDs
func generateTestKeys(count int) [][]byte {
	keys := make([][]byte, count)
	for i := 0; i < count; i++ {
		// Generate better pseudo-random 32-byte keys using multiple prime multipliers
		key := make([]byte, 32)
		seed := uint64(i)
		for j := 0; j < 32; j++ {
			// Use different primes for each byte position for better distribution
			seed = seed*7919 + 6571
			key[j] = byte((seed >> (8 * (j % 8))) & 0xFF)
		}
		keys[i] = key
	}
	return keys
}
