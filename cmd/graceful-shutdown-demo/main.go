package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/types"
)

func main() {
	fmt.Println("=== Nostr Event Store - 优雅退出示例 ===\n")

	ctx := context.Background()

	// 配置
	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = "./demo_data/data"
	cfg.WALConfig.WALDir = "./demo_data/wal"
	cfg.IndexConfig.IndexDir = "./demo_data/indexes"

	// 初始化 store
	fmt.Println("初始化 Event Store...")
	store := eventstore.New(&eventstore.Options{
		Config: cfg,
	})

	if err := store.Open(ctx, "./demo_data", true); err != nil {
		fmt.Printf("❌ 打开失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Event Store 已打开")

	// ✅ 关键：设置信号处理
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM, syscall.SIGINT)

	// 启动工作
	done := make(chan bool)
	go func() {
		defer func() {
			done <- true
		}()

		fmt.Println("\n开始写入事件...")
		fmt.Println("提示：按 Ctrl+C 可以优雅退出\n")

		// 模拟持续写入事件
		for i := 0; i < 100; i++ {
			event := &types.Event{
				ID:        [32]byte{byte(i)},
				Pubkey:    [32]byte{1},
				CreatedAt: uint32(time.Now().Unix()),
				Kind:      1,
				Content:   fmt.Sprintf("Test event %d", i),
			}

			if _, err := store.WriteEvent(ctx, event); err != nil {
				fmt.Printf("写入事件 %d 失败: %v\n", i, err)
				return
			}

			if (i+1)%10 == 0 {
				fmt.Printf("已写入 %d 个事件...\n", i+1)
			}

			// 模拟工作间隔
			time.Sleep(100 * time.Millisecond)
		}

		fmt.Println("\n✅ 所有事件写入完成")
	}()

	// 等待完成或信号
	var wasInterrupted bool
	select {
	case <-done:
		fmt.Println("\n工作正常完成")
	case sig := <-sigChan:
		fmt.Printf("\n\n🛑 收到信号: %v\n", sig)
		fmt.Println("正在优雅关闭...")
		wasInterrupted = true
	}

	// ✅ 优雅关闭流程
	fmt.Println("\n--- 开始关闭流程 ---")

	// 1. 刷新所有待写入的数据
	closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	fmt.Println("1/3 刷新待写入数据...")
	if err := store.Flush(closeCtx); err != nil {
		fmt.Printf("⚠️  刷新失败: %v\n", err)
	} else {
		fmt.Println("✅ 数据已刷新到磁盘")
	}

	// 2. 如果需要，可以创建检查点
	// (在实际应用中，Close 会自动处理)

	// 3. 关闭 store
	fmt.Println("2/3 关闭 Event Store...")
	if err := store.Close(closeCtx); err != nil {
		fmt.Printf("❌ 关闭失败: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("✅ Event Store 已安全关闭")

	// 4. 显示统计信息
	fmt.Println("3/3 清理完成")
	fmt.Println("\n--- 关闭完成 ---")

	if wasInterrupted {
		fmt.Println("\n✅ 程序被中断，但数据已安全保存！")
		fmt.Println("   下次启动时会自动恢复一致性")
	} else {
		fmt.Println("\n✅ 程序正常退出，所有数据已保存")
	}
}
