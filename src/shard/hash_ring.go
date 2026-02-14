// Package shard implements horizontal sharding for distributed event storage.
// It provides consistent hashing, shard routing, and federated query coordination.
package shard

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"sync"
)

// HashRing implements consistent hashing with virtual nodes for even distribution.
// Virtual nodes (replicas) ensure that when nodes are added or removed,
// only a small fraction of keys need to be remapped.
type HashRing struct {
	mu           sync.RWMutex
	nodes        map[uint32]string // hash -> node ID
	sortedHashes []uint32          // sorted hash values for binary search
	virtualNodes int               // number of virtual nodes per physical node
}

// NewHashRing creates a new consistent hash ring.
// virtualNodes: number of virtual nodes per physical node (recommended: 150-300)
func NewHashRing(virtualNodes int) *HashRing {
	if virtualNodes <= 0 {
		virtualNodes = 150 // default
	}
	return &HashRing{
		nodes:        make(map[uint32]string),
		sortedHashes: make([]uint32, 0),
		virtualNodes: virtualNodes,
	}
}

// AddNode adds a physical node to the hash ring.
// Creates multiple virtual nodes to ensure even distribution.
func (r *HashRing) AddNode(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Create virtual nodes
	for i := 0; i < r.virtualNodes; i++ {
		virtualKey := fmt.Sprintf("%s#%d", nodeID, i)
		hash := r.hashKey([]byte(virtualKey))
		r.nodes[hash] = nodeID
		r.sortedHashes = append(r.sortedHashes, hash)
	}

	// Sort hashes for binary search
	sort.Slice(r.sortedHashes, func(i, j int) bool {
		return r.sortedHashes[i] < r.sortedHashes[j]
	})
}

// RemoveNode removes a physical node from the hash ring.
func (r *HashRing) RemoveNode(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Remove all virtual nodes for this physical node
	newHashes := make([]uint32, 0, len(r.sortedHashes))
	for _, hash := range r.sortedHashes {
		if r.nodes[hash] != nodeID {
			newHashes = append(newHashes, hash)
		} else {
			delete(r.nodes, hash)
		}
	}
	r.sortedHashes = newHashes
}

// GetNode returns the node ID responsible for the given key.
// Uses consistent hashing to determine which node should handle this key.
func (r *HashRing) GetNode(key []byte) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.sortedHashes) == 0 {
		return "", fmt.Errorf("hash ring is empty")
	}

	hash := r.hashKey(key)

	// Binary search for the first node with hash >= key hash
	idx := sort.Search(len(r.sortedHashes), func(i int) bool {
		return r.sortedHashes[i] >= hash
	})

	// Wrap around if we've gone past the end
	if idx == len(r.sortedHashes) {
		idx = 0
	}

	nodeHash := r.sortedHashes[idx]
	return r.nodes[nodeHash], nil
}

// GetNodes returns all unique physical node IDs in the ring.
func (r *HashRing) GetNodes() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodeSet := make(map[string]bool)
	for _, nodeID := range r.nodes {
		nodeSet[nodeID] = true
	}

	nodes := make([]string, 0, len(nodeSet))
	for nodeID := range nodeSet {
		nodes = append(nodes, nodeID)
	}

	sort.Strings(nodes) // For deterministic ordering
	return nodes
}

// GetNodeCount returns the number of physical nodes in the ring.
func (r *HashRing) GetNodeCount() int {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodeSet := make(map[string]bool)
	for _, nodeID := range r.nodes {
		nodeSet[nodeID] = true
	}
	return len(nodeSet)
}

// GetDistribution returns statistics about key distribution across nodes.
// Useful for testing and monitoring shard balance.
func (r *HashRing) GetDistribution(testKeys [][]byte) map[string]int {
	distribution := make(map[string]int)

	for _, key := range testKeys {
		node, err := r.GetNode(key)
		if err == nil {
			distribution[node]++
		}
	}

	return distribution
}

// hashKey computes a 32-bit hash of the key using SHA256.
// We use the first 4 bytes of SHA256 for consistency.
func (r *HashRing) hashKey(key []byte) uint32 {
	hash := sha256.Sum256(key)
	return binary.BigEndian.Uint32(hash[:4])
}

// Stats returns statistics about the hash ring.
type RingStats struct {
	PhysicalNodes int
	VirtualNodes  int
	TotalHashes   int
}

// GetStats returns statistics about the hash ring.
func (r *HashRing) GetStats() RingStats {
	r.mu.RLock()
	defer r.mu.RUnlock()

	nodeSet := make(map[string]bool)
	for _, nodeID := range r.nodes {
		nodeSet[nodeID] = true
	}

	return RingStats{
		PhysicalNodes: len(nodeSet),
		VirtualNodes:  r.virtualNodes,
		TotalHashes:   len(r.sortedHashes),
	}
}
