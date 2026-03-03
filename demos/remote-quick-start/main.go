// Remote Quick Start Demo
// 这个示例展示了如何快速启动一个 Remote Mode 的 EventStore 服务器和客户端
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/haorendashu/nostr_event_store/src/client"
	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/remote"
	"github.com/haorendashu/nostr_event_store/src/types"
)

const (
	serverAddr = "localhost:50051"
	apiKey     = "demo-quick-start-key-2026"
	dataDir    = "./quick_start_data"
)

func main() {
	fmt.Println("=== Remote Quick Start Demo ===")
	fmt.Println()

	// 步骤 1: 启动服务器
	fmt.Println("📡 Step 1: Starting Remote Server...")
	serverDone := make(chan struct{})
	go runServer(serverDone)

	// 等待服务器启动
	time.Sleep(2 * time.Second)

	// 步骤 2: 运行客户端操作
	fmt.Println("\n📱 Step 2: Running Client Operations...")
	if err := runClient(); err != nil {
		log.Fatalf("Client error: %v", err)
	}

	// 步骤 3: 优雅关闭
	fmt.Println("\n🛑 Step 3: Graceful Shutdown...")
	fmt.Println("Press Ctrl+C to exit...")

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\nShutting down...")
	close(serverDone)
	time.Sleep(1 * time.Second)

	fmt.Println("\n✅ Demo completed successfully!")
}

// runServer 启动 Remote Mode 的 EventStore 服务器
func runServer(done chan struct{}) {
	// 1. 创建配置（基于默认配置）
	cfg := config.DefaultConfig()

	// 2. 自定义配置
	cfg.WALConfig.Disabled = true                        // 禁用 WAL（演示用）
	cfg.StorageConfig.MaxSegmentSize = 100 * 1024 * 1024 // 100MB
	cfg.RemoteConfig.Mode = "remote"
	cfg.RemoteConfig.GRPCListenAddr = serverAddr
	cfg.RemoteConfig.APIKey = apiKey

	// 3. 创建 Remote Listener
	listener := remote.NewListener(&remote.ListenerConfig{
		GRPCListenAddr: cfg.RemoteConfig.GRPCListenAddr,
		APIKey:         cfg.RemoteConfig.APIKey,
		Logger:         log.New(os.Stdout, "[SERVER] ", log.LstdFlags),
	})

	// 4. 创建 EventStore
	store := eventstore.New(&eventstore.Options{
		Config:   cfg,
		Listener: listener,
	})

	// 5. 设置 EventStore 引用（必须在 Open 之前调用）
	listener.SetEventStore(store)

	// 6. 打开 EventStore（这会自动启动 gRPC 服务器）
	ctx := context.Background()
	if err := store.Open(ctx, dataDir, true); err != nil {
		log.Fatalf("Failed to open EventStore: %v", err)
	}
	defer func() {
		if err := store.Close(ctx); err != nil {
			log.Printf("Failed to close EventStore: %v", err)
		}
		// 清理数据目录
		os.RemoveAll(dataDir)
	}()

	fmt.Printf("[SERVER] gRPC server listening on %s\n", serverAddr)
	fmt.Printf("[SERVER] API Key: %s\n", apiKey)

	// 等待关闭信号
	<-done
	fmt.Println("[SERVER] Shutting down...")
}

// runClient 运行客户端操作示例
func runClient() error {
	// 1. 创建客户端 (展示新的 Keepalive 配置)
	cfg := &client.Config{
		Address:             serverAddr,
		APIKey:              apiKey,
		RequestTimeout:      5 * time.Second,
		ConnectTimeout:      2 * time.Second,
		MaxRetries:          3,
		KeepaliveTime:       10 * time.Second, // 每 10 秒发送心跳
		KeepaliveTimeout:    3 * time.Second,  // 心跳超时 3 秒
		PermitWithoutStream: true,             // 允许在没有活动流时发送心跳
		MaxReconnectBackoff: 30 * time.Second, // 最大重连退避时间
	}

	c, err := client.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("failed to create client: %w", err)
	}
	defer c.Close()

	// 1.5. 检查连接状态（新功能）
	fmt.Println("\n🔌 Connection State Check...")
	connState := c.GetConnectionState()
	fmt.Printf("   Connection State: %v\n", connState)
	if !c.IsConnected() {
		fmt.Println("   ⏳ Waiting for connection to be ready...")
		if err := c.WaitForReady(context.Background(), 5*time.Second); err != nil {
			return fmt.Errorf("connection not ready: %w", err)
		}
	}
	fmt.Println("   ✅ Connection is READY")

	newRequestContext := func() (context.Context, context.CancelFunc) {
		return context.WithTimeout(context.Background(), cfg.RequestTimeout)
	}

	// 2. 健康检查
	fmt.Println("\n🔍 Health Check...")
	healthCtx, cancel := newRequestContext()
	healthy, err := c.HealthCheck(healthCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("health check failed: %w", err)
	}
	if !healthy {
		return fmt.Errorf("server is not healthy")
	}
	fmt.Println("   ✅ Server is healthy")

	// 3. 写入单个事件
	fmt.Println("\n📝 Writing a single event...")
	event1 := createTestEvent("Alice", 1, "Hello, Nostr Remote Mode!")
	writeCtx, cancel := newRequestContext()
	loc, err := c.WriteEvent(writeCtx, event1)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to write event: %w", err)
	}
	fmt.Printf("   ✅ Event written: ID=%s, Location=%+v\n",
		hex.EncodeToString(event1.ID[:8]), loc)

	// 4. 批量写入事件
	fmt.Println("\n📝 Writing batch events...")
	events := []*types.Event{
		createTestEvent("Alice", 1, "Message 1 from Alice"),
		createTestEvent("Alice", 1, "Message 2 from Alice"),
		createTestEvent("Bob", 1, "Hello from Bob"),
		createTestEvent("Bob", 1, "Another message from Bob"),
		createTestEvent("Charlie", 7, "Charlie's profile update"),
	}

	batchCtx, cancel := newRequestContext()
	locs, err := c.WriteEvents(batchCtx, events)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to write events: %w", err)
	}
	fmt.Printf("   ✅ %d events written\n", len(locs))

	// 5. 根据 ID 获取事件
	fmt.Println("\n🔎 Getting event by ID...")
	getCtx, cancel := newRequestContext()
	retrieved, err := c.GetEvent(getCtx, event1.ID)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to get event: %w", err)
	}
	fmt.Printf("   ✅ Retrieved: %s (kind=%d)\n", retrieved.Content, retrieved.Kind)

	// 6. 查询 Alice 的所有消息
	fmt.Println("\n🔍 Querying Alice's events...")
	alicePubkey := stringToPubkey("Alice")
	filter := &types.QueryFilter{
		Authors: [][32]byte{alicePubkey},
		Limit:   10,
	}

	queryAliceCtx, cancel := newRequestContext()
	results, err := c.QueryAll(queryAliceCtx, filter)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to query: %w", err)
	}
	fmt.Printf("   ✅ Found %d events from Alice:\n", len(results))
	for i, evt := range results {
		fmt.Printf("      %d. %s\n", i+1, evt.Content)
	}

	// 7. 按 Kind 查询
	fmt.Println("\n🔍 Querying by kind (kind=1)...")
	kindFilter := &types.QueryFilter{
		Kinds: []uint16{1},
		Limit: 10,
	}

	queryKindCtx, cancel := newRequestContext()
	kindResults, err := c.QueryAll(queryKindCtx, kindFilter)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to query by kind: %w", err)
	}
	fmt.Printf("   ✅ Found %d events with kind=1\n", len(kindResults))

	// 8. 统计查询
	fmt.Println("\n📊 Query count...")
	countCtx, cancel := newRequestContext()
	count, err := c.QueryCount(countCtx, &types.QueryFilter{})
	cancel()
	if err != nil {
		return fmt.Errorf("failed to query count: %w", err)
	}
	fmt.Printf("   ✅ Total events in store: %d\n", count)

	// 9. 删除事件
	fmt.Println("\n🗑️  Deleting an event...")
	deleteCtx, cancel := newRequestContext()
	err = c.DeleteEvent(deleteCtx, events[0].ID)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to delete event: %w", err)
	}
	fmt.Println("   ✅ Event deleted")

	// 10. 获取服务器统计信息
	fmt.Println("\n📈 Server Stats...")
	statsCtx, cancel := newRequestContext()
	stats, err := c.Stats(statsCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to get stats: %w", err)
	}
	fmt.Printf("   ✅ Stats: %+v\n", stats)

	// 11. 强制刷新
	fmt.Println("\n💾 Flushing to disk...")
	flushCtx, cancel := newRequestContext()
	err = c.Flush(flushCtx)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to flush: %w", err)
	}
	fmt.Println("   ✅ Flushed successfully")

	return nil
}

// createTestEvent 创建一个测试事件
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

	// 计算事件 ID（简化版，仅用于演示）
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s:%d:%s", author, kind, content)))
	event.ID = hash

	return event
}

// stringToPubkey 将字符串转换为 32 字节 pubkey（仅用于演示）
func stringToPubkey(s string) [32]byte {
	hash := sha256.Sum256([]byte(s))
	return hash
}
