// Shard Coordinator Demo
// 演示如何使用 Coordinator 统一管理本地和远程 shard
// This demo shows how to use a Coordinator to manage both local and remote shards
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/remote"
	"github.com/haorendashu/nostr_event_store/src/shard"
	"github.com/haorendashu/nostr_event_store/src/types"
)

const (
	remoteAddr          = "localhost:50051"
	apiKey              = "coordinator-demo-key-2026"
	remoteServerDataDir = "./coordinator_demo_remote_data"
	localShardDataDir   = "./coordinator_demo_local_data"
)

// HybridCoordinator 是一个混合模式的 Coordinator
// 支持同时管理本地和远程 shard
type HybridCoordinator struct {
	shards   map[string]shard.Shard
	hashRing *shard.HashRing
	ctx      context.Context
}

// NewHybridCoordinator 创建一个新的混合 Coordinator
func NewHybridCoordinator() *HybridCoordinator {
	return &HybridCoordinator{
		shards:   make(map[string]shard.Shard),
		hashRing: shard.NewHashRing(150), // 150 个虚拟节点
		ctx:      context.Background(),
	}
}

// AddLocalShard 添加一个本地 shard
func (c *HybridCoordinator) AddLocalShard(id, dataDir string, cfg config.Config) error {
	localShard, err := shard.NewLocalShard(id, dataDir, cfg)
	if err != nil {
		return fmt.Errorf("failed to create local shard: %w", err)
	}

	if err := localShard.Open(c.ctx); err != nil {
		return fmt.Errorf("failed to open local shard: %w", err)
	}

	c.shards[id] = localShard
	c.hashRing.AddNode(id)
	fmt.Printf("   ✅ Added local shard: %s\n", id)
	return nil
}

// AddRemoteShard 添加一个远程 shard
func (c *HybridCoordinator) AddRemoteShard(id, addr, apiKey string, cfg *config.RemoteConfig) error {
	remoteShard, err := shard.NewRemoteShard(id, addr, apiKey, cfg)
	if err != nil {
		return fmt.Errorf("failed to create remote shard: %w", err)
	}

	if err := remoteShard.Open(c.ctx); err != nil {
		return fmt.Errorf("failed to connect to remote shard: %w", err)
	}

	c.shards[id] = remoteShard
	c.hashRing.AddNode(id)
	fmt.Printf("   ✅ Added remote shard: %s (addr=%s)\n", id, addr)
	return nil
}

// GetShardByPubkey 根据 pubkey 获取对应的 shard
func (c *HybridCoordinator) GetShardByPubkey(pubkey [32]byte) (shard.Shard, error) {
	shardID, err := c.hashRing.GetNode(pubkey[:])
	if err != nil {
		return nil, err
	}

	s, exists := c.shards[shardID]
	if !exists {
		return nil, fmt.Errorf("shard %s not found", shardID)
	}

	return s, nil
}

// Insert 插入事件（自动路由到对应的 shard）
func (c *HybridCoordinator) Insert(event *types.Event) error {
	s, err := c.GetShardByPubkey(event.Pubkey)
	if err != nil {
		return err
	}

	return s.Insert(c.ctx, event)
}

// InsertBatch 批量插入事件（自动按 pubkey 路由）
func (c *HybridCoordinator) InsertBatch(events []*types.Event) error {
	// 按 shard 分组
	batches := make(map[string][]*types.Event)

	for _, event := range events {
		s, err := c.GetShardByPubkey(event.Pubkey)
		if err != nil {
			return err
		}
		batches[s.GetID()] = append(batches[s.GetID()], event)
	}

	// 并发写入各个 shard
	for shardID, events := range batches {
		s := c.shards[shardID]
		if err := s.InsertBatch(c.ctx, events); err != nil {
			return fmt.Errorf("failed to insert to shard %s: %w", shardID, err)
		}
	}

	return nil
}

// QueryByAuthor 查询指定作者的事件
func (c *HybridCoordinator) QueryByAuthor(pubkey [32]byte, limit int) ([]*types.Event, error) {
	s, err := c.GetShardByPubkey(pubkey)
	if err != nil {
		return nil, err
	}

	filter := &types.QueryFilter{
		Authors: [][32]byte{pubkey},
		Limit:   limit,
	}

	return s.Query(c.ctx, filter)
}

// QueryAll 查询所有 shard（跨 shard 查询）
func (c *HybridCoordinator) QueryAll(filter *types.QueryFilter) ([]*types.Event, error) {
	var allResults []*types.Event

	for _, s := range c.shards {
		results, err := s.Query(c.ctx, filter)
		if err != nil {
			log.Printf("Warning: query shard %s failed: %v", s.GetID(), err)
			continue
		}
		allResults = append(allResults, results...)
	}

	return allResults, nil
}

// GetByID 根据 ID 获取事件（需要查询所有 shard）
func (c *HybridCoordinator) GetByID(eventID [32]byte) (*types.Event, error) {
	for _, s := range c.shards {
		event, err := s.GetByID(c.ctx, eventID)
		if err == nil {
			return event, nil
		}
	}

	return nil, fmt.Errorf("event not found")
}

// GetStats 获取所有 shard 的统计信息
func (c *HybridCoordinator) GetStats() (map[string]shard.ShardStats, error) {
	stats := make(map[string]shard.ShardStats)

	for id, s := range c.shards {
		stat, err := s.Stats(c.ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get stats for shard %s: %w", id, err)
		}
		stats[id] = stat
	}

	return stats, nil
}

// Close 关闭所有 shard
func (c *HybridCoordinator) Close() error {
	for _, s := range c.shards {
		if err := s.Close(c.ctx); err != nil {
			log.Printf("Error closing shard %s: %v", s.GetID(), err)
		}
	}
	return nil
}

func main() {
	fmt.Println("=== Shard Coordinator Demo ===")
	fmt.Println("演示使用 Coordinator 统一管理本地和远程 shard")
	fmt.Println("Demonstrating unified shard management with Coordinator")
	fmt.Println()

	// 步骤 1: 启动远程服务器
	fmt.Println("📡 Step 1: Starting Remote Server...")
	serverDone := make(chan struct{})
	go runRemoteServer(serverDone)

	time.Sleep(2 * time.Second)

	// 步骤 2: 创建 Coordinator 并添加 shard
	fmt.Println("\n🔧 Step 2: Creating Coordinator and Adding Shards...")
	if err := runCoordinatorDemo(); err != nil {
		log.Fatalf("Demo error: %v", err)
	}

	// 步骤 3: 优雅关闭
	fmt.Println("\n🛑 Step 3: Graceful Shutdown...")
	fmt.Println("Auto shutdown after demo run...")

	fmt.Println("\nShutting down...")
	close(serverDone)
	time.Sleep(1 * time.Second)

	// 清理数据目录
	fmt.Println("Cleaning up...")
	os.RemoveAll(remoteServerDataDir)
	os.RemoveAll(localShardDataDir)

	fmt.Println("\n✅ Demo completed successfully!")
}

// runRemoteServer 启动远程 EventStore 服务器
func runRemoteServer(done chan struct{}) {
	cfg := config.DefaultConfig()
	cfg.WALConfig.Disabled = true
	cfg.StorageConfig.MaxSegmentSize = 100 * 1024 * 1024
	cfg.RemoteConfig.Mode = "remote"
	cfg.RemoteConfig.GRPCListenAddr = remoteAddr
	cfg.RemoteConfig.APIKey = apiKey

	listener := remote.NewListener(&remote.ListenerConfig{
		GRPCListenAddr: cfg.RemoteConfig.GRPCListenAddr,
		APIKey:         cfg.RemoteConfig.APIKey,
		Logger:         log.New(os.Stdout, "[REMOTE-SERVER] ", log.LstdFlags),
	})

	store := eventstore.New(&eventstore.Options{
		Config:   cfg,
		Listener: listener,
	})

	listener.SetEventStore(store)

	ctx := context.Background()
	if err := store.Open(ctx, remoteServerDataDir, true); err != nil {
		log.Fatalf("Failed to open EventStore: %v", err)
	}
	defer store.Close(ctx)

	fmt.Printf("[REMOTE-SERVER] gRPC server listening on %s\n", remoteAddr)

	<-done
	fmt.Println("[REMOTE-SERVER] Shutting down...")
}

// runCoordinatorDemo 演示 Coordinator 的使用
func runCoordinatorDemo() error {
	// ========== 创建 Coordinator ==========
	fmt.Println("\n🎯 Creating Hybrid Coordinator...")
	coordinator := NewHybridCoordinator()
	defer coordinator.Close()

	// ========== 添加本地 Shard ==========
	fmt.Println("\n📦 Adding Local Shard...")
	localCfg := config.DefaultConfig()
	localCfg.WALConfig.Disabled = true

	if err := coordinator.AddLocalShard("local-shard-01", localShardDataDir, *localCfg); err != nil {
		return err
	}

	// ========== 添加远程 Shard ==========
	fmt.Println("\n🌐 Adding Remote Shard...")
	remoteCfg := &config.RemoteConfig{
		RequestTimeout: 10,
	}

	if err := coordinator.AddRemoteShard("remote-shard-01", remoteAddr, apiKey, remoteCfg); err != nil {
		return err
	}

	fmt.Println("\n✅ Coordinator initialized with 2 shards")

	// ========== 测试写入操作 ==========
	fmt.Println("\n📝 Testing Insert Operations...")

	// 创建测试事件（不同作者会路由到不同 shard）
	events := []*types.Event{
		createTestEvent("Alice", 1, "Hello from Alice"),
		createTestEvent("Alice", 1, "Second message from Alice"),
		createTestEvent("Bob", 1, "Hello from Bob"),
		createTestEvent("Charlie", 1, "Hello from Charlie"),
		createTestEvent("David", 1, "Hello from David"),
		createTestEvent("Eve", 7, "Eve's profile update"),
	}

	fmt.Printf("   Writing %d events through coordinator...\n", len(events))
	if err := coordinator.InsertBatch(events); err != nil {
		return fmt.Errorf("failed to insert batch: %w", err)
	}
	fmt.Println("   ✅ Successfully wrote events (auto-routed by pubkey)")

	// ========== 显示路由信息 ==========
	fmt.Println("\n🔀 Event Routing Information:")
	for i, event := range events {
		s, _ := coordinator.GetShardByPubkey(event.Pubkey)
		shardType := "🏠 Local"
		if !s.IsLocal() {
			shardType = "🌐 Remote"
		}
		fmt.Printf("   %d. Author: %-8s → Shard: %-16s [%s]\n",
			i+1, getAuthorName(event.Pubkey), s.GetID(), shardType)
	}

	// ========== 测试单作者查询 ==========
	fmt.Println("\n🔍 Testing Single Author Query...")
	alicePubkey := stringToPubkey("Alice")

	fmt.Println("   Querying Alice's events (auto-routed to specific shard)...")
	aliceResults, err := coordinator.QueryByAuthor(alicePubkey, 10)
	if err != nil {
		return fmt.Errorf("failed to query: %w", err)
	}

	s, _ := coordinator.GetShardByPubkey(alicePubkey)
	fmt.Printf("   ✅ Found %d events from Alice in shard %s\n", len(aliceResults), s.GetID())
	for i, evt := range aliceResults {
		fmt.Printf("      %d. %s\n", i+1, evt.Content)
	}

	// ========== 测试跨 Shard 查询 ==========
	fmt.Println("\n🔍 Testing Cross-Shard Query...")
	fmt.Println("   Querying all kind=1 events across all shards...")

	allResults, err := coordinator.QueryAll(&types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 20,
	})
	if err != nil {
		return fmt.Errorf("failed to query all: %w", err)
	}

	fmt.Printf("   ✅ Found %d events across all shards\n", len(allResults))

	// ========== 按 ID 获取事件 ==========
	fmt.Println("\n🔎 Testing Get Event by ID...")
	eventID := events[0].ID
	fmt.Printf("   Looking for event ID %s...\n", hex.EncodeToString(eventID[:8]))

	event, err := coordinator.GetByID(eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}
	fmt.Printf("   ✅ Found: %s\n", event.Content)

	// ========== 获取统计信息 ==========
	fmt.Println("\n📊 Getting Statistics from All Shards...")
	stats, err := coordinator.GetStats()
	if err != nil {
		return fmt.Errorf("failed to get stats: %w", err)
	}

	fmt.Println("\n   ┌─────────────────────────────────────────────────────┐")
	for id, stat := range stats {
		shardType := "Local "
		addr := "N/A"
		if stat.IsRemote {
			shardType = "Remote"
			addr = stat.RemoteAddr
		}
		fmt.Printf("   │ Shard: %-20s [%s]        │\n", id, shardType)
		if stat.IsRemote {
			fmt.Printf("   │   - Address: %-32s │\n", addr)
		}
		fmt.Printf("   │   - Events:  %-32d │\n", stat.EventCount)
		fmt.Printf("   │   - Size:    %-28d bytes │\n", stat.TotalSize)
		fmt.Printf("   │   - Healthy: %-32v │\n", stat.IsHealthy)
		fmt.Println("   ├─────────────────────────────────────────────────────┤")
	}
	fmt.Println("   └─────────────────────────────────────────────────────┘")

	// ========== 展示 Coordinator 的优势 ==========
	fmt.Println("\n💡 Coordinator Benefits:")
	fmt.Println("   ✅ Unified API - 统一的 API 接口")
	fmt.Println("   ✅ Auto Routing - 自动路由到正确的 shard")
	fmt.Println("   ✅ Load Balancing - 基于一致性哈希的负载均衡")
	fmt.Println("   ✅ Transparent - 应用层无需关心是本地还是远程")
	fmt.Println("   ✅ Scalable - 可以动态添加/删除 shard")

	return nil
}

// createTestEvent 创建测试事件
func createTestEvent(author string, kind uint16, content string) *types.Event {
	pubkey := stringToPubkey(author)

	event := &types.Event{
		Pubkey:    pubkey,
		CreatedAt: uint32(time.Now().Unix()),
		Kind:      kind,
		Content:   content,
		Tags:      [][]string{},
		Sig:       [64]byte{},
	}

	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%d:%s",
		author, kind, event.CreatedAt, content)))
	event.ID = hash

	return event
}

// stringToPubkey 字符串转 pubkey
func stringToPubkey(s string) [32]byte {
	hash := sha256.Sum256([]byte(s))
	return hash
}

// getAuthorName 从 pubkey 获取作者名称（用于演示）
func getAuthorName(pubkey [32]byte) string {
	// 简单映射，实际应用中不会这样做
	authors := []string{"Alice", "Bob", "Charlie", "David", "Eve"}
	for _, name := range authors {
		if pubkey == stringToPubkey(name) {
			return name
		}
	}
	return hex.EncodeToString(pubkey[:4])
}
