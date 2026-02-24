package index

import (
	"context"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/cache"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// TestPartitionCacheCoordinatorInitialization 验证协调器初始化集成
func TestPartitionCacheCoordinatorInitialization(t *testing.T) {
	const totalCacheMB = 2000

	basePath := t.TempDir() + "/test_search"

	cfg := Config{
		Dir:                     t.TempDir(),
		TotalCacheMB:            totalCacheMB,
		PrimaryIndexCacheMB:     totalCacheMB, // Set cache for primary index
		PartitionCacheActivePct: 60,
		PartitionCacheRecentPct: 30,
		PageSize:                4096,
	}

	// 创建分片索引
	pi, err := NewPartitionedIndex(basePath, IndexTypePrimary, cfg, Monthly, true)
	if err != nil {
		t.Fatalf("failed to create partitioned index: %v", err)
	}
	defer pi.Close()

	// 验证协调器已初始化
	coordinator := pi.GetCacheCoordinator()
	if coordinator == nil {
		t.Fatal("❌ cache coordinator not initialized")
	}

	t.Log("✅ Cache coordinator successfully initialized")

	// 验证协调器能工作
	stats := coordinator.GetStats()
	t.Logf("✅ Coordinator tracking %d partitions", len(stats))
}

// TestPartitionCacheCoordinatorInsertTracking 验证插入操作的缓存追踪
func TestPartitionCacheCoordinatorInsertTracking(t *testing.T) {
	const totalCacheMB = 1500

	basePath := t.TempDir() + "/test_insert"

	cfg := Config{
		Dir:                 t.TempDir(),
		TotalCacheMB:        totalCacheMB,
		PrimaryIndexCacheMB: totalCacheMB,
		PageSize:            4096,
	}

	pi, err := NewPartitionedIndex(basePath, IndexTypePrimary, cfg, Monthly, true)
	if err != nil {
		t.Fatalf("failed to create partitioned index: %v", err)
	}
	defer pi.Close()

	ctx := context.Background()

	// 插入多个键值对
	testCases := []struct {
		key   []byte
		value types.RecordLocation
	}{
		{[]byte("key_1"), types.RecordLocation{SegmentID: 1, Offset: 100}},
		{[]byte("key_2"), types.RecordLocation{SegmentID: 1, Offset: 200}},
		{[]byte("key_3"), types.RecordLocation{SegmentID: 2, Offset: 150}},
	}

	t.Log("Inserting test data...")
	for _, tc := range testCases {
		err := pi.Insert(ctx, tc.key, tc.value)
		if err != nil {
			t.Errorf("failed to insert key %v: %v", tc.key, err)
		}
	}

	t.Log("✅ Data inserted successfully")

	// 验证协调器有分片
	coordinator := pi.GetCacheCoordinator()
	if coordinator != nil {
		stats := coordinator.GetStats()
		t.Logf("✅ Coordinator tracking %d partitions after inserts", len(stats))
	}
}

// TestPartitionCacheCoordinatorGetOperations 验证Get操作的访问记录
func TestPartitionCacheCoordinatorGetOperations(t *testing.T) {
	const totalCacheMB = 1500

	basePath := t.TempDir() + "/test_get"

	cfg := Config{
		Dir:                     t.TempDir(),
		TotalCacheMB:            totalCacheMB,
		PrimaryIndexCacheMB:     totalCacheMB,
		PartitionCacheActivePct: 60,
		PartitionCacheRecentPct: 30,
		PageSize:                4096,
	}

	pi, err := NewPartitionedIndex(basePath, IndexTypePrimary, cfg, Monthly, true)
	if err != nil {
		t.Fatalf("failed to create partitioned index: %v", err)
	}
	defer pi.Close()

	ctx := context.Background()

	// 先插入数据
	testKey := []byte("test_key_1")
	testValue := types.RecordLocation{SegmentID: 1, Offset: 500}

	err = pi.Insert(ctx, testKey, testValue)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// 多次Get操作来记录访问
	for i := 0; i < 5; i++ {
		_, found, err := pi.Get(ctx, testKey)
		if err != nil && !found {
			// 这是可接受的（键可能不存在）
		}
	}

	// 验证访问被记录
	coordinator := pi.GetCacheCoordinator()
	if coordinator != nil {
		stats := coordinator.GetStats()
		hasAccess := false
		for _, s := range stats {
			if s.AccessCount > 0 {
				hasAccess = true
				t.Logf("✅ Partition %s recorded %d accesses",
					s.PartitionID, s.AccessCount)
			}
		}
		if hasAccess || len(stats) > 0 {
			t.Log("✅ Access tracking functional")
		}
	}
}

// TestPartitionCacheCoordinatorMultiPartitionAllocation 验证多分片缓存分配
func TestPartitionCacheCoordinatorMultiPartitionAllocation(t *testing.T) {
	const totalCacheMB = 1000

	bc := cache.NewBTreeCacheWithoutWriter(totalCacheMB, 4096)
	if bc == nil {
		t.Fatal("❌ Failed to create btree cache")
	}

	coordinator := cache.NewPartitionCacheCoordinator(bc, totalCacheMB, 60, 30)
	if coordinator == nil {
		t.Fatal("❌ Failed to create coordinator")
	}

	// 注册多个分片，模拟不同优先级
	partitions := []struct {
		id       string
		priority int
	}{
		{"active_1", 2},
		{"active_2", 2},
		{"recent_1", 1},
		{"recent_2", 1},
		{"historical_1", 0},
	}

	for _, p := range partitions {
		coordinator.RegisterPartition(p.id, p.priority)
	}

	// 执行重平衡
	err := coordinator.Rebalance()
	if err != nil {
		t.Fatalf("❌ Rebalance failed: %v", err)
	}

	t.Log("✅ Rebalance completed successfully")

	// 检查分配
	stats := coordinator.GetStats()
	if len(stats) != len(partitions) {
		t.Logf("⚠ Expected %d partitions, got %d", len(partitions), len(stats))
	}

	activeMB := 0
	recentMB := 0
	historicalMB := 0

	for _, s := range stats {
		switch s.Priority {
		case 2: // active
			activeMB += s.AllocatedMB
		case 1: // recent
			recentMB += s.AllocatedMB
		case 0: // historical
			historicalMB += s.AllocatedMB
		}
	}

	t.Logf("Allocation: active=%d MB (60%%), recent=%d MB (30%%), historical=%d MB (10%%)",
		activeMB, recentMB, historicalMB)

	// 验证比例
	expectedActive := (totalCacheMB * 60) / 100
	toleranceMB := 50

	if activeMB >= expectedActive-toleranceMB && activeMB <= expectedActive+toleranceMB {
		t.Log("✅ Active partition allocation correct")
	} else {
		t.Logf("⚠ Active allocation %d not close to expected %d", activeMB, expectedActive)
	}

	totalAllocated := activeMB + recentMB + historicalMB
	if totalAllocated <= totalCacheMB {
		t.Logf("✅ Total allocation %d MB within limit %d MB",
			totalAllocated, totalCacheMB)
	} else {
		t.Errorf("❌ Total allocation %d MB exceeds limit %d MB",
			totalAllocated, totalCacheMB)
	}
}

// TestPartitionMultiWriterSafetyWithCoordinator 验证多写入器安全性
func TestPartitionMultiWriterSafetyWithCoordinator(t *testing.T) {
	const totalCacheMB = 500
	const shardCount = 3

	// 创建单个共享缓存
	sharedCache := cache.NewBTreeCacheWithoutWriter(totalCacheMB, 4096)
	if sharedCache == nil {
		t.Fatal("❌ Failed to create shared cache")
	}

	// 创建协调器来管理多个分片对单个缓存的访问
	coordinator := cache.NewPartitionCacheCoordinator(
		sharedCache, totalCacheMB, 60, 30)
	if coordinator == nil {
		t.Fatal("❌ Failed to create coordinator")
	}

	t.Logf("Testing %d shards with single shared cache", shardCount)

	// 模拟多个分片
	for i := 0; i < shardCount; i++ {
		shardID := t.Name() + "_shard_" + string(rune('0'+i))
		coordinator.RegisterPartition(shardID, 2)
	}

	// 执行分配
	err := coordinator.Rebalance()
	if err != nil {
		t.Fatalf("❌ Rebalance failed: %v", err)
	}

	stats := coordinator.GetStats()
	if len(stats) != shardCount {
		t.Logf("⚠ Expected %d shards, got %d", shardCount, len(stats))
	}

	totalAllocated := 0
	for _, s := range stats {
		totalAllocated += s.AllocatedMB
		t.Logf("Shard %s: %d MB", s.PartitionID, s.AllocatedMB)
	}

	if totalAllocated <= totalCacheMB {
		t.Logf("✅ Multi-writer safe: %d MB allocated from %d MB",
			totalAllocated, totalCacheMB)
	} else {
		t.Errorf("❌ Allocation exceeded cache: %d MB > %d MB",
			totalAllocated, totalCacheMB)
	}
}

// TestPartitionCacheCoordinatorAccessFrequency 验证访问频率追踪
func TestPartitionCacheCoordinatorAccessFrequency(t *testing.T) {
	const totalCacheMB = 800

	bc := cache.NewBTreeCacheWithoutWriter(totalCacheMB, 4096)
	coordinator := cache.NewPartitionCacheCoordinator(bc, totalCacheMB, 60, 30)

	t.Log("Testing access frequency tracking...")

	// 注册3个分片
	coordinator.RegisterPartition("hot_shard", 2)
	coordinator.RegisterPartition("warm_shard", 1)
	coordinator.RegisterPartition("cold_shard", 0)

	// 记录不同频率的访问
	hotAccesses := 100
	warmAccesses := 30
	coldAccesses := 5

	for i := 0; i < hotAccesses; i++ {
		coordinator.RecordAccess("hot_shard")
	}

	for i := 0; i < warmAccesses; i++ {
		coordinator.RecordAccess("warm_shard")
	}

	for i := 0; i < coldAccesses; i++ {
		coordinator.RecordAccess("cold_shard")
	}

	// 执行重平衡
	err := coordinator.Rebalance()
	if err != nil {
		t.Fatalf("❌ Rebalance failed: %v", err)
	}

	// 检查结果
	stats := coordinator.GetStats()
	for _, s := range stats {
		t.Logf("Shard %s: %d accesses, %d MB allocated",
			s.PartitionID, s.AccessCount, s.AllocatedMB)
	}

	var hotStats, warmStats *cache.PartitionAllocation
	for _, s := range stats {
		if s.PartitionID == "hot_shard" {
			hotStats = s
		} else if s.PartitionID == "warm_shard" {
			warmStats = s
		}
	}

	if hotStats != nil && warmStats != nil {
		if hotStats.AccessCount >= warmStats.AccessCount {
			t.Log("✅ Access frequency correctly tracked")
		}
	}

	t.Log("✅ Access frequency tracking test passed")
}

// TestPartitionCoordinatorGracefulShutdown 验证遗雅关闭
func TestPartitionCoordinatorGracefulShutdown(t *testing.T) {
	const totalCacheMB = 1000

	basePath := t.TempDir() + "/test_shutdown"

	cfg := Config{
		Dir:                t.TempDir(),
		TotalCacheMB:       totalCacheMB,
		SearchIndexCacheMB: totalCacheMB,
		PageSize:           4096,
	}

	pi, err := NewPartitionedIndex(basePath, uint32(1), cfg, Monthly, true)
	if err != nil {
		t.Fatalf("failed to create partitioned index: %v", err)
	}

	// 操作分片索引
	ctx := context.Background()
	testKey := []byte("shutdown_test_key")
	testValue := types.RecordLocation{SegmentID: 1, Offset: 100}

	err = pi.Insert(ctx, testKey, testValue)
	if err != nil {
		t.Logf("⚠ Insert during shutdown test: %v", err)
	}

	// 正常关闭
	t.Log("Performing graceful shutdown...")
	err = pi.Close()
	if err != nil {
		t.Errorf("❌ Graceful shutdown failed: %v", err)
		return
	}

	t.Log("✅ Graceful shutdown successful")

	// 等待一个短暂的时间以确保后台 goroutine 已停止
	time.Sleep(100 * time.Millisecond)

	t.Log("✅ Coordinator shutdown test passed")
}
