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

func queryByAuthor(ctx context.Context, store *shard.DistributedShardStore, pubkey [32]byte, limit int) ([]*types.Event, error) {
	s, err := store.GetShardByPubkey(pubkey)
	if err != nil {
		return nil, err
	}

	filter := &types.QueryFilter{
		Authors: [][32]byte{pubkey},
		Limit:   limit,
	}

	return s.Query(ctx, filter)
}

func queryAll(ctx context.Context, store *shard.DistributedShardStore, filter *types.QueryFilter) ([]*types.Event, error) {
	var allResults []*types.Event
	for _, s := range store.GetAllShards() {
		results, err := s.Query(ctx, filter)
		if err != nil {
			log.Printf("Warning: query shard %s failed: %v", s.GetID(), err)
			continue
		}
		allResults = append(allResults, results...)
	}
	return allResults, nil
}

func getShardStats(ctx context.Context, store *shard.DistributedShardStore) (map[string]shard.ShardStats, error) {
	stats := make(map[string]shard.ShardStats)
	for _, s := range store.GetAllShards() {
		stat, err := s.Stats(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to get stats for shard %s: %w", s.GetID(), err)
		}
		stats[s.GetID()] = stat
	}
	return stats, nil
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
	ctx := context.Background()

	// ========== 创建 Coordinator ==========
	fmt.Println("\n🎯 Creating DistributedShardStore...")
	storeCfg := config.DefaultConfig()
	storeCfg.DistributedShardConfig.Enabled = true
	coordinator := shard.NewDistributedShardStore(*storeCfg)
	defer coordinator.Close(ctx)

	// ========== 添加本地 Shard ==========
	fmt.Println("\n📦 Adding Local Shard...")
	localCfg := config.DefaultConfig()
	localCfg.WALConfig.Disabled = true

	if err := coordinator.AddLocalShard(ctx, "local-shard-01", localShardDataDir, *localCfg); err != nil {
		return err
	}
	fmt.Println("   ✅ Added local shard: local-shard-01")

	// ========== 添加远程 Shard ==========
	fmt.Println("\n🌐 Adding Remote Shard...")
	if err := coordinator.AddRemoteShard(ctx, "remote-shard-01", remoteAddr, apiKey); err != nil {
		return err
	}
	fmt.Printf("   ✅ Added remote shard: %s (addr=%s)\n", "remote-shard-01", remoteAddr)

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
	if err := coordinator.InsertBatch(ctx, events); err != nil {
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
	aliceResults, err := queryByAuthor(ctx, coordinator, alicePubkey, 10)
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

	allResults, err := queryAll(ctx, coordinator, &types.QueryFilter{
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

	event, err := coordinator.GetByID(ctx, eventID)
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}
	fmt.Printf("   ✅ Found: %s\n", event.Content)

	// ========== 获取统计信息 ==========
	fmt.Println("\n📊 Getting Statistics from All Shards...")
	stats, err := getShardStats(ctx, coordinator)
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
