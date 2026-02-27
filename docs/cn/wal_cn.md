# WAL 包设计与实现指南

**目标读者:** 开发者、架构师和维护者  
**最后更新:** 2026年2月27日  
**语言:** 中文

## 目录

1. [概述](#概述)
2. [架构与设计理念](#架构与设计理念)
3. [核心数据结构](#核心数据结构)
4. [接口定义](#接口定义)
5. [FileWriter 模块](#filewriter-模块)
6. [FileReader 模块](#filereader-模块)
7. [FileManager 模块](#filemanager-模块)
8. [回放系统](#回放系统)
9. [验证系统](#验证系统)
10. [核心工作流](#核心工作流)
11. [设计决策与权衡](#设计决策与权衡)
12. [性能分析](#性能分析)
13. [故障排查与调试](#故障排查与调试)
14. [API 快速参考](#api-快速参考)
15. [结论](#结论)

---

## 概述

`wal` (Write-Ahead Logging，预写日志) 包为 Nostr 事件存储系统实现持久性保证。它确保所有对事件和索引的修改在应用到内存之前都被记录，从而能够在崩溃后完全恢复而不丢失数据。

### 核心职责

- **持久性保证:** 在内存应用之前记录所有变更
- **崩溃恢复:** 回放 WAL 条目以在崩溃后重建系统状态
- **检查点管理:** 创建恢复检查点以优化回放时间
- **段管理:** 处理多个 WAL 文件的轮转和清理
- **完整性验证:** 使用 CRC64 校验和验证 WAL 文件完整性

### 关键特性

| 属性 | 值 | 理由 |
|------|-----|------|
| **持久性模型** | 预写日志 | 业界标准的崩溃恢复方案 |
| **段大小** | 1GB (默认，可配置) | 在文件数量和可管理性之间平衡 |
| **同步模式** | always/batch/never | 在安全性和吞吐量之间权衡 |
| **批处理间隔** | 100ms (默认) | 延迟与吞吐量的最佳平衡 |
| **最大记录大小** | 100MB | 防止内存耗尽 |
| **校验和算法** | CRC64-ECMA | 快速验证且错误检测能力好 |
| **寻址** | LSN (日志序列号) | 单调递增，全局有序 |

### 与其他包的关系

```
eventstore/
    ↓ (通过 WAL 写入)
wal/
    ↓ (记录变更)
[磁盘: WAL 段]
    ↑ (恢复期间读取)
recovery/
    ↑ (重建状态)
index/, storage/, cache/
```

---

## 架构与设计理念

### 系统设计原则

1. **预写保证:** 所有变更在内存状态改变**之前**记录到 WAL
2. **顺序写入:** 只追加设计最大化磁盘吞吐量
3. **故障安全操作:** 通过校验和检测不完整写入；恢复在最后有效条目处停止
4. **单调 LSN:** 日志序列号提供操作的全局顺序
5. **检查点优化:** 周期性检查点减少恢复扫描时间
6. **段轮转:** 保持文件大小可管理；实现磁盘空间回收

### 依赖关系图

```
┌──────────────────────┐
│   EventStore         │  (高级操作)
├──────────────────────┤
│   Manager 接口       │  (WAL 生命周期)
│      ↓               │
│   Writer 接口        │  (记录变更)
│   Reader 接口        │  (恢复/回放)
└──────────────────────┘
         ↓
┌──────────────────────┐
│  FileManager         │  (检查点与清理)
│  FileWriter          │  (追加条目)
│  FileReader          │  (顺序读取)
└──────────────────────┘
         ↓
┌──────────────────────┐
│  WAL 段文件          │  (wal.log, wal.000001.log, ...)
└──────────────────────┘
```

### 设计分层

```
┌─────────────────────────────────────┐
│  应用层                             │
│  ┌───────────────────────────────┐  │
│  │ eventstore, index, storage    │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  Manager 接口                       │
│  • 生命周期管理                     │
│  • 检查点协调                       │
│  • 段清理                           │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  Writer/Reader 接口                 │
│  ┌───────────────────────────────┐  │
│  │ Writer: 追加条目              │  │
│  │ Reader: 顺序扫描              │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  基于文件的实现                     │
│  ┌───────────────────────────────┐  │
│  │ FileWriter: 缓冲写入          │  │
│  │ FileReader: 缓冲读取          │  │
│  │ 段轮转                        │  │
│  │ 校验和验证                    │  │
│  └───────────────────────────────┘  │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│  磁盘存储                           │
│  • wal.log (当前段)                 │
│  • wal.000001.log (已轮转)          │
│  • wal.000002.log (已轮转)          │
└─────────────────────────────────────┘
```

---

## 核心数据结构

### OpType 枚举

定义 WAL 中记录的操作类型：

```go
type OpType uint8

const (
    OpTypeInsert       OpType = 1  // 事件插入操作
    OpTypeUpdateFlags  OpType = 2  // 事件标志更新 (删除/替换)
    OpTypeIndexUpdate  OpType = 3  // 索引节点更新
    OpTypeCheckpoint   OpType = 4  // 检查点标记
)
```

**操作语义:**

| OpType | 用途 | EventDataOrMetadata 格式 |
|--------|------|--------------------------|
| `OpTypeInsert` | 记录新事件插入 | 完整的序列化事件记录 |
| `OpTypeUpdateFlags` | 记录逻辑删除/替换 | `segmentID(4B) + offset(4B) + flags(1B)` |
| `OpTypeIndexUpdate` | 记录 B 树节点变更 | `keyLen(4B) + key + valueLen(4B) + value` |
| `OpTypeCheckpoint` | 标记恢复点 | 空 (LSN 在头部) |

### Entry 结构

表示单个 WAL 条目（日志记录单元）：

```go
type Entry struct {
    Type                OpType    // 操作类型
    LSN                 uint64    // 日志序列号 (由 writer 分配)
    Timestamp           uint64    // 创建时间 (Unix 时间戳)
    EventDataOrMetadata []byte    // 操作特定的载荷
    Checksum            uint64    // CRC64 校验和用于完整性验证
}
```

**WAL 文件中的二进制布局:**

```
偏移量  | 字段                | 类型    | 大小 | 描述
────────┼────────────────────┼─────────┼──────┼─────────────────────
0       | OpType             | uint8   | 1B   | 操作类型 (1-4)
1       | LSN                | uint64  | 8B   | 日志序列号
9       | Timestamp          | uint64  | 8B   | Unix 时间戳 (微秒)
17      | DataLen            | uint32  | 4B   | EventDataOrMetadata 长度
21      | EventDataOrMetadata| []byte  | 变长 | 操作载荷
21+N    | Checksum           | uint64  | 8B   | 字节 [0, 21+N) 的 CRC64
```

**校验和计算:**

```go
// 校验和覆盖: OpType(1B) + LSN(8B) + Timestamp(8B) + DataLen(4B) + Data(N bytes)
checksumData := entry_bytes[0 : 21+len(EventDataOrMetadata)]
checksum := crc64.Checksum(checksumData, crc64.MakeTable(crc64.ECMA))
```

### LSN (日志序列号)

WAL 条目的单调递增标识符：

```go
type LSN = uint64
```

**LSN 属性:**
- **单调性:** 每个条目的 LSN = 前一个 LSN + 1
- **全局有序:** 定义所有操作的全局顺序
- **恢复锚点:** 恢复从检查点 LSN 开始
- **持久性标记:** LSN ≤ X 的所有条目在 X 确认时已刷盘

### Config 结构

WAL 操作的配置参数：

```go
type Config struct {
    Dir             string  // WAL 目录路径
    MaxSegmentSize  uint64  // 最大段文件大小 (默认: 1GB)
    SyncMode        string  // "always"/"batch"/"never"
    BatchIntervalMs int     // 批处理模式的 fsync 间隔 (默认: 100ms)
    BatchSizeBytes  uint32  // 强制 fsync 前的缓冲区大小 (默认: 10MB)
}
```

**配置示例:**

```go
// 最大持久性 (最安全，最慢)
cfg := Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,      // 1GB
    SyncMode:        "always",     // 每次写入后 fsync
    BatchIntervalMs: 0,
    BatchSizeBytes:  0,
}

// 平衡 (默认推荐)
cfg := Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,      // 1GB
    SyncMode:        "batch",      // 周期性 fsync
    BatchIntervalMs: 100,          // 100ms 批处理
    BatchSizeBytes:  10 * 1024 * 1024, // 10MB 缓冲区
}

// 最大吞吐量 (最不安全)
cfg := Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,
    SyncMode:        "never",      // 依赖 OS 缓存
    BatchIntervalMs: 0,
    BatchSizeBytes:  0,
}
```

### Checkpoint 结构

表示恢复检查点：

```go
type Checkpoint struct {
    LSN             LSN     // 创建检查点时的 LSN
    Timestamp       uint64  // 检查点创建时间
    LastSegmentID   uint32  // 检查点前最后关闭的段
    CompactionState []byte  // 用于压缩恢复的不透明数据
}
```

**检查点用途:**
- 恢复可以从最近的检查点开始而非从头开始
- 检查点之前的所有条目已应用到持久状态 (段、索引)
- 压缩状态允许崩溃后进行部分压缩回滚

### Stats 结构

WAL 监控统计信息：

```go
type Stats struct {
    CurrentLSN        LSN     // 最后写入的 LSN
    CheckpointCount   int     // 检查点数量
    TotalSegmentSize  uint64  // 总 WAL 磁盘使用量
    FirstLSN          LSN     // 最旧可用的 LSN (清理后)
    LastCheckpointLSN LSN     // 最近检查点的 LSN
}
```

---

## 接口定义

### Writer 接口

**用途:** 向 WAL 追加条目  
**并发性:** 单写入者模型 (调用者必须序列化访问)  
**持久性:** 由 SyncMode 配置控制

```go
type Writer interface {
    // 初始化 WAL 用于写入
    Open(ctx context.Context, cfg Config) error

    // 追加单个条目，返回分配的 LSN
    Write(ctx context.Context, entry *Entry) (LSN, error)

    // 原子地追加多个条目，返回 LSN 数组
    WriteBatch(ctx context.Context, entries []*Entry) ([]LSN, error)

    // 强制将所有缓冲条目刷到磁盘 (fsync)
    Flush(ctx context.Context) error

    // 在当前 LSN 创建检查点标记
    CreateCheckpoint(ctx context.Context) (LSN, error)

    // 获取最后写入条目的 LSN (可能未刷盘)
    LastLSN() LSN

    // 关闭 writer 并释放资源
    Close() error
}
```

**实现:** [`FileWriter`](../src/wal/file_wal.go)

**并发保证:** 非线程安全；调用者必须同步

**使用示例:**

```go
writer := wal.NewFileWriter()
err := writer.Open(ctx, wal.Config{
    Dir:             "/data/wal",
    MaxSegmentSize:  1 << 30,
    SyncMode:        "batch",
    BatchIntervalMs: 100,
    BatchSizeBytes:  10 * 1024 * 1024,
})
if err != nil {
    return err
}
defer writer.Close()

// 写入单个条目
entry := &wal.Entry{
    Type:                wal.OpTypeInsert,
    EventDataOrMetadata: serializedEvent,
}
lsn, err := writer.Write(ctx, entry)

// 批量写入以获得更好的吞吐量
entries := []*wal.Entry{entry1, entry2, entry3}
lsns, err := writer.WriteBatch(ctx, entries)

// 强制持久化
err = writer.Flush(ctx)
```

### Reader 接口

**用途:** 顺序读取 WAL 条目用于恢复  
**并发性:** 允许多个读取者 (只读)  
**定位:** 可以从特定 LSN 开始 (例如，检查点)

```go
type Reader interface {
    // 在 startLSN 处初始化读取器 (0 = 从头开始)
    Open(ctx context.Context, dir string, startLSN LSN) error

    // 读取下一个条目 (在末尾返回 io.EOF)
    Read(ctx context.Context) (*Entry, error)

    // 获取最后成功读取条目的 LSN
    LastValidLSN() LSN

    // 关闭读取器并释放资源
    Close() error
}
```

**实现:** [`FileReader`](../src/wal/file_wal.go)

**错误处理:** 在日志末尾返回 `io.EOF`；其他错误表示损坏或 I/O 失败

**使用示例:**

```go
reader := wal.NewFileReader()
err := reader.Open(ctx, "/data/wal", 0) // 0 = 从头开始
if err != nil {
    return err
}
defer reader.Close()

for {
    entry, err := reader.Read(ctx)
    if err == io.EOF {
        break // WAL 结束
    }
    if err != nil {
        return fmt.Errorf("read WAL: %w", err)
    }
    
    // 根据类型处理条目
    switch entry.Type {
    case wal.OpTypeInsert:
        // 反序列化并插入事件
    case wal.OpTypeUpdateFlags:
        // 更新事件标志
    case wal.OpTypeIndexUpdate:
        // 应用索引更新
    case wal.OpTypeCheckpoint:
        // 记录检查点
    }
}
```

### Manager 接口

**用途:** 高级 WAL 生命周期管理  
**职责:** 段轮转、检查点协调、清理

```go
type Manager interface {
    // 初始化 WAL 管理器
    Open(ctx context.Context, cfg Config) error

    // 获取用于追加条目的 writer
    Writer() Writer

    // 获取定位在最近检查点的 reader
    Reader(ctx context.Context) (Reader, error)

    // 获取最近的检查点
    LastCheckpoint() (Checkpoint, error)

    // 获取所有可用的检查点 (LSN 升序)
    Checkpoints() []Checkpoint

    // 删除最后 LSN 在给定 LSN 之前的段
    DeleteSegmentsBefore(ctx context.Context, beforeLSN LSN) error

    // 获取 WAL 统计信息
    Stats(ctx context.Context) (Stats, error)

    // 关闭管理器和资源
    Close() error
}
```

**实现:** [`FileManager`](../src/wal/manager.go)

**使用示例:**

```go
manager := wal.NewFileManager()
err := manager.Open(ctx, cfg)
if err != nil {
    return err
}
defer manager.Close()

// 获取 writer 用于正常操作
writer := manager.Writer()
lsn, err := writer.Write(ctx, entry)

// 在主要操作后创建检查点
checkpointLSN, err := writer.CreateCheckpoint(ctx)

// 清理旧段 (在检查点后)
err = manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// 获取监控统计信息
stats, err := manager.Stats(ctx)
fmt.Printf("WAL 大小: %d 字节, 检查点: %d\n", 
    stats.TotalSegmentSize, stats.CheckpointCount)
```

### Replayer 接口

**用途:** 回放 WAL 条目期间的回调接口  
**实现:** 由应用提供 (eventstore, index 等)

```go
type Replayer interface {
    // 为 OpTypeInsert 条目调用
    OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error

    // 为 OpTypeUpdateFlags 条目调用
    OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error

    // 为 OpTypeIndexUpdate 条目调用
    OnIndexUpdate(ctx context.Context, key []byte, value []byte) error

    // 为 OpTypeCheckpoint 条目调用
    OnCheckpoint(ctx context.Context, checkpoint Checkpoint) error
}
```

**实现示例:**

```go
type StoreReplayer struct {
    store   *EventStore
    indexes map[string]*Index
}

func (r *StoreReplayer) OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error {
    // 将事件重新插入到内存结构中
    for _, idx := range r.indexes {
        if err := idx.InsertEvent(event, location); err != nil {
            return err
        }
    }
    return r.store.cache.Put(event.ID, event)
}

func (r *StoreReplayer) OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error {
    // 在索引中更新标志
    return r.store.UpdateFlagsAtLocation(location, flags)
}

func (r *StoreReplayer) OnIndexUpdate(ctx context.Context, key []byte, value []byte) error {
    // 应用原始索引更新 (B 树节点修改)
    indexName := string(key[:4])
    return r.indexes[indexName].ApplyUpdate(key, value)
}

func (r *StoreReplayer) OnCheckpoint(ctx context.Context, checkpoint Checkpoint) error {
    // 记录检查点以跟踪进度
    log.Printf("回放检查点在 LSN %d", checkpoint.LSN)
    return nil
}
```

---

## FileWriter 模块

### 概述

`FileWriter` 使用基于文件的存储实现 `Writer` 接口，具有内存缓冲和可配置的 fsync 策略。

**源码:** [file_wal.go](../src/wal/file_wal.go)

### 关键特性

- **缓冲写入:** 内存缓冲区减少系统调用开销
- **批量 Fsync:** 周期性 fsync (可配置间隔) 平衡持久性和吞吐量
- **段轮转:** 超过 MaxSegmentSize 时自动创建新段
- **LSN 分配:** 为每个条目分配单调递增的 LSN
- **校验和计算:** 为每个条目计算 CRC64

### 内部结构

```go
type FileWriter struct {
    cfg         Config         // 配置
    file        *os.File       // 当前段文件
    buffer      []byte         // 内存写入缓冲区
    bufferPos   int            // 当前缓冲区位置
    lastLSN     uint64         // 最后分配的 LSN
    checkpoint  *Checkpoint    // 最近的检查点
    mu          sync.Mutex     // 保护并发访问
    segmentID   uint32         // 当前段 ID
    segmentPath string         // 当前段文件路径
    segmentSize uint64         // 当前段大小 (字节)
    ticker      *time.Ticker   // 用于批处理模式 fsync
    done        chan struct{}  // 关闭信号
}
```

### 段文件格式

**头部布局 (24 字节):**

```
偏移量  | 字段              | 类型    | 大小 | 值
────────┼──────────────────┼─────────┼──────┼─────────────────────
0       | Magic            | uint32  | 4B   | 0x574C414F ('WLAO')
4       | Version          | uint64  | 8B   | 1
12      | CheckpointLSN    | uint64  | 8B   | 最后检查点 LSN
20      | Reserved         | uint32  | 4B   | 0
```

**条目部分:**

紧接头部；包含变长的 Entry 记录 (见 §3.2)。

### 核心方法

#### Write

向 WAL 追加单个条目：

**算法:**

```
1. 获取锁
2. 分配 LSN = lastLSN + 1
3. 如果未提供则设置时间戳
4. 序列化条目 (type + LSN + timestamp + data_len + data)
5. 计算 CRC64 校验和
6. 确保缓冲区容量
7. 将序列化条目追加到缓冲区
8. 如果 SyncMode == "always":
     将缓冲区刷到磁盘 (write + fsync)
   否则如果 SyncMode == "batch" 且 buffer >= BatchSizeBytes:
     将缓冲区刷到磁盘
9. 释放锁
10. 返回分配的 LSN
```

**复杂度:** O(D) 其中 D 是数据大小 (序列化 + 校验和)  
**I/O:** 1 次 write + 1 次 fsync (如果 SyncMode="always")，或缓冲 (batch/never)

**源码:** [file_wal.go#L150](../src/wal/file_wal.go#L150)

#### WriteBatch

原子地追加多个条目：

**算法:**

```
1. 获取锁
2. 对每个条目:
     - 分配顺序 LSN
     - 如果未提供则设置时间戳
     - 序列化条目
     - 验证大小 <= walMaxRecordSize
3. 确保缓冲区容量足够容纳所有条目
4. 将所有序列化条目追加到缓冲区
5. 如果 SyncMode == "always":
     刷新缓冲区
6. 释放锁
7. 返回分配的 LSN 数组
```

**相比调用 `Write` N 次的优势:**
- 单次锁获取
- 批量序列化
- 单次 fsync (如果 SyncMode="always")

**复杂度:** O(N * D) 其中 N 是条目数，D 是平均条目大小  
**加速:** 对于 100-1000 条目的批次，相比单独 Write 调用快约 2-3 倍

**源码:** [file_wal.go#L195](../src/wal/file_wal.go#L195)

#### Flush

强制将所有缓冲条目刷到磁盘：

**算法:**

```
1. 获取锁
2. 如果缓冲区为空:
     立即返回
3. 将缓冲区写入文件 (系统调用)
4. 将文件同步到磁盘 (fsync 系统调用)
5. 清空缓冲区
6. 释放锁
```

**延迟:** SSD 上约 1-10ms，HDD 上约 5-20ms (取决于磁盘队列深度)  
**持久性:** Flush 返回前写入的所有条目都是崩溃安全的

**源码:** [file_wal.go#L245](../src/wal/file_wal.go#L245)

#### CreateCheckpoint

创建检查点标记：

**算法:**

```
1. 获取锁
2. 创建 OpTypeCheckpoint 条目，LSN = lastLSN + 1
3. 序列化并追加到缓冲区
4. 将缓冲区刷到磁盘
5. 用新检查点 LSN 更新段头部
6. 更新内存中的检查点结构
7. 释放锁
8. 返回检查点 LSN
```

**用例:** 在主要操作 (压缩、段轮转) 后调用以优化恢复

**源码:** [file_wal.go#L278](../src/wal/file_wal.go#L278)

### 段轮转

当段大小超过 `MaxSegmentSize` 时：

**算法:**

```
1. 刷新当前缓冲区
2. 关闭当前段文件
3. 重命名当前段: wal.log → wal.NNNNNN.log (其中 N = segmentID)
4. 递增 segmentID
5. 创建新的 wal.log 文件
6. 用当前检查点 LSN 写入头部
7. 重置 segmentSize = headerSize
```

**文件命名:**
- 活跃段: `wal.log`
- 已轮转段: `wal.000001.log`, `wal.000002.log`, ...

**恢复行为:** Reader 按 ID 升序扫描段

### 批量刷新 Goroutine

当 `SyncMode == "batch"` 时，后台 goroutine 周期性刷新：

**算法:**

```go
func (w *FileWriter) batchFlusher() {
    for {
        select {
        case <-w.ticker.C:
            w.mu.Lock()
            w.flushLocked()
            w.mu.Unlock()
        case <-w.done:
            return
        }
    }
}
```

**时间:** Ticker 每 `BatchIntervalMs` 毫秒触发一次  
**关闭:** 通过 `Close()` 中的 `done` 通道发信号

**权衡:**
- 更小的 BatchIntervalMs → 更低延迟，更多 fsync 开销
- 更大的 BatchIntervalMs → 更高延迟，更好吞吐量

---

## FileReader 模块

### 概述

`FileReader` 实现 `Reader` 接口，具有缓冲读取、多段遍历和校验和验证功能。

**源码:** [file_wal.go](../src/wal/file_wal.go)

### 关键特性

- **缓冲读取:** 1MB 读取缓冲区减少系统调用开销
- **多段:** 在 EOF 时自动转到下一段
- **校验和验证:** 检测损坏；不匹配时返回错误
- **可寻址:** 可以从任意 LSN 开始 (例如，检查点)
- **防御性:** 验证缓冲区边界以防止 panic

### 内部结构

```go
type FileReader struct {
    dir          string          // WAL 目录
    startLSN     uint64          // 起始 LSN (0 = 从头开始)
    segments     []walSegment    // 段文件列表
    segmentIndex int             // 当前段索引
    file         *os.File        // 当前段文件
    buffer       []byte          // 读取缓冲区
    offset       int             // 缓冲区中的当前偏移
    lastValidLSN uint64          // 最后成功读取的 LSN
    tableECMA    *crc64.Table    // CRC64 表
}
```

### 核心方法

#### Open

在指定 LSN 处初始化读取器：

**算法:**

```
1. 列出目录中的所有段文件
2. 按 ID 排序段 (升序)
3. 如果 startLSN > 0:
     二分查找包含 startLSN 的段
   否则:
     从第一个段开始
4. 打开段文件
5. 跳到数据部分 (跳过头部)
6. 如果 startLSN > 0:
     扫描条目直到 LSN >= startLSN
7. 初始化读取缓冲区 (1MB 容量)
```

**复杂度:** O(S + E) 其中 S 是段数，E 是扫描到 startLSN 的条目数  
**典型:** 对于有 100 万条目的 1000 个段，< 100ms

**源码:** [file_wal.go#L500](../src/wal/file_wal.go#L500)

#### Read

从 WAL 读取下一个条目：

**算法:**

```
1. 确保缓冲区有 >= 21 字节 (条目头部)
   - 如果没有: 从文件读取更多
   - 如果 EOF: 打开下一段
2. 解析条目头部:
   - OpType (1 字节)
   - LSN (8 字节)
   - Timestamp (8 字节)
   - DataLen (4 字节)
3. 验证 DataLen <= walMaxRecordSize
4. 确保缓冲区有 >= (21 + DataLen + 8) 字节
5. 提取数据 (DataLen 字节)
6. 提取存储的校验和 (8 字节)
7. 计算字节 [0, 21+DataLen) 的校验和
8. 如果校验和不匹配:
     返回错误 (检测到损坏)
9. 构造 Entry 对象
10. 更新 offset 和 lastValidLSN
11. 返回 Entry
```

**复杂度:** O(D) 其中 D 是条目数据大小  
**缓冲区管理:** 根据需要自动压缩和增长缓冲区

**防御性检查:**
- 分配前验证 DataLen
- 切片前检查缓冲区边界
- 如果设置 `WAL_DIAG=1` 环境变量则记录诊断信息

**源码:** [file_wal.go#L650](../src/wal/file_wal.go#L650)

#### ensureBuffer

内部缓冲区管理方法：

**算法:**

```
1. 当缓冲区缺少 minBytes 时:
   a. 如果 offset > 0 则压缩缓冲区:
        - 将 buffer[offset:] 复制到 buffer[0:]
        - 重置 offset = 0
   b. 如果需要则增长缓冲区容量:
        - 分配容量 >= minBytes 的新缓冲区
        - 将数据复制到新缓冲区
   c. 从文件读取更多数据到 buffer[len:]
   d. 如果 EOF:
        - 如果有足够数据: 返回
        - 否则: 打开下一段，继续
2. 返回 (如果发生压缩则 shifted=true)
```

**缓冲区增长策略:** 每次容量翻倍，最小值 = minBytes

**源码:** [file_wal.go#L750](../src/wal/file_wal.go#L750)

### 多段遍历

当当前段到达 EOF 时：

**算法:**

```
1. 关闭当前段文件
2. 递增 segmentIndex
3. 如果 segmentIndex >= len(segments):
     返回 io.EOF (WAL 结束)
4. 打开 segments[segmentIndex]
5. 跳过头部 (24 字节)
6. 重置缓冲区和偏移
7. 继续读取
```

**无缝转换:** Reader 自动链接段，无需调用者干预

---

## FileManager 模块

### 概述

`FileManager` 实现 `Manager` 接口，协调 Writer、Reader 和段生命周期。

**源码:** [manager.go](../src/wal/manager.go)

### 职责

- 提供对 Writer 和 Reader 的访问
- 跟踪检查点
- 删除旧段以回收磁盘空间
- 计算 WAL 统计信息用于监控

### 内部结构

```go
type FileManager struct {
    cfg         Config        // 配置
    writer      *FileWriter   // 单例 writer
    mu          sync.Mutex    // 保护检查点列表
    checkpoints []Checkpoint  // 所有已知检查点
}
```

### 核心方法

#### Open

初始化管理器：

**算法:**

```
1. 获取锁
2. 存储配置
3. 创建 FileWriter 实例
4. 用配置打开 writer
5. 列出所有段文件
6. 如果段存在:
     - 打开最后一个段
     - 读取头部检查点 LSN
     - 初始化检查点列表
7. 释放锁
```

#### Reader

返回定位在最近检查点的 Reader：

**算法:**

```
1. 获取锁
2. 获取最后检查点 LSN (如果没有则为 0)
3. 创建 FileReader 实例
4. 在检查点 LSN 处打开 reader
5. 释放锁
6. 返回 reader
```

**用例:** 启动时调用以进行崩溃恢复

#### DeleteSegmentsBefore

删除给定 LSN 之前的旧段：

**算法:**

```
1. 获取锁
2. 列出所有段文件
3. 对每个段:
     - 如果段当前活跃 (writer.segmentID) 则跳过
     - 扫描段以找到最后 LSN
     - 如果 lastLSN < beforeLSN:
         删除段文件
4. 释放锁
```

**用例:** 在检查点后调用以回收磁盘空间

**安全性:** 永不删除活跃段；跳过 LSN >= 阈值的段

**源码:** [manager.go#L95](../src/wal/manager.go#L95)

#### Stats

计算 WAL 统计信息：

**算法:**

```
1. 获取锁
2. 列出所有段文件
3. 对每个段:
     - stat 文件获取大小
     - 扫描第一个和最后一个 LSN
4. 累加总大小
5. 从检查点列表获取最后检查点 LSN
6. 释放锁
7. 返回 Stats 结构
```

**复杂度:** O(S) 其中 S 是段数 (仅扫描头部，非完整文件)

**源码:** [manager.go#L130](../src/wal/manager.go#L130)

---

## 回放系统

### 概述

回放系统读取 WAL 条目并调用回调以在崩溃后重建内存状态。

**源码:** [replay.go](../src/wal/replay.go)

### 关键组件

1. **ReplayWAL:** 主回放函数
2. **Replayer 接口:** 应用的回调接口
3. **ReplayOptions:** 回放行为配置
4. **ReplayStats:** 回放期间收集的统计信息

### ReplayOptions

```go
type ReplayOptions struct {
    StartLSN    LSN                       // 开始 LSN (0 = 从头)
    StopOnError bool                      // 第一个错误时中止?
    Serializer  storage.EventSerializer   // 用于反序列化事件
}
```

### ReplayStats

```go
type ReplayStats struct {
    EntriesProcessed     int64    // 读取的总条目数
    InsertsReplayed      int64    // OpTypeInsert 计数
    UpdatesReplayed      int64    // OpTypeUpdateFlags 计数
    IndexUpdatesReplayed int64    // OpTypeIndexUpdate 计数
    CheckpointsReplayed  int64    // OpTypeCheckpoint 计数
    Errors               []error  // 遇到的错误 (如果 !StopOnError)
    LastLSN              LSN      // 最后处理的 LSN
}
```

### ReplayWAL 函数

主回放入口点：

**签名:**

```go
func ReplayWAL(ctx context.Context, reader Reader, replayer Replayer, opts ReplayOptions) (*ReplayStats, error)
```

**算法:**

```
1. 初始化 ReplayStats
2. 创建序列化器 (如果未提供)
3. 循环:
   a. 读取下一个 WAL 条目
   b. 如果 EOF: 退出
   c. 如果错误且 StopOnError: 返回错误
   d. 递增 EntriesProcessed
   e. 根据 entry.Type 切换:
        OpTypeInsert:       replayInsert()
        OpTypeUpdateFlags:  replayUpdateFlags()
        OpTypeIndexUpdate:  replayIndexUpdate()
        OpTypeCheckpoint:   replayCheckpoint()
   f. 如果处理错误且 StopOnError: 返回错误
   g. 如果处理错误且 !StopOnError: 追加到 stats.Errors
4. 返回 ReplayStats
```

**复杂度:** O(E * P) 其中 E 是条目数，P 是每条目处理时间  
**典型:** 现代硬件上 10,000-50,000 条目/秒

**源码:** [replay.go#L64](../src/wal/replay.go#L64)

### 条目处理函数

#### replayInsert

处理 `OpTypeInsert` 条目：

**算法:**

```
1. 提取 EventDataOrMetadata (包含序列化事件)
2. 解析记录头部 (长度、标志、继续计数)
3. 构造 storage.Record 对象
4. 使用提供的序列化器反序列化事件
5. 调用 replayer.OnInsert(event, location)
   - Location 为零 (实际位置由 replayer 计算)
6. 如果反序列化或回调失败则返回错误
```

**注意:** Location 传递为 (0, 0)，因为回放不知道最终位置。replayer 回调通过重新追加到段来重新计算位置。

**源码:** [replay.go#L125](../src/wal/replay.go#L125)

#### replayUpdateFlags

处理 `OpTypeUpdateFlags` 条目：

**格式 (9 字节):**
```
segmentID (4 字节) + offset (4 字节) + flags (1 字节)
```

**算法:**

```
1. 解析 EventDataOrMetadata:
   - segment_id (字节 0-3)
   - offset (字节 4-7)
   - flags (字节 8)
2. 构造 RecordLocation
3. 调用 replayer.OnUpdateFlags(location, flags)
4. 如果回调失败则返回错误
```

**旧格式:** 如果数据只有 1 字节，假设仅标志 (无位置)。使用零位置。

**源码:** [replay.go#L180](../src/wal/replay.go#L180)

#### replayIndexUpdate

处理 `OpTypeIndexUpdate` 条目：

**格式:**
```
key_len (4 字节) + key (变长) + value_len (4 字节) + value (变长)
```

**算法:**

```
1. 解析 key_len (字节 0-3)
2. 提取 key (字节 4 : 4+key_len)
3. 解析 value_len (字节 4+key_len : 8+key_len)
4. 提取 value (字节 8+key_len : 8+key_len+value_len)
5. 调用 replayer.OnIndexUpdate(key, value)
6. 如果回调失败则返回错误
```

**用例:** 回放 B 树节点修改以进行索引恢复

**源码:** [replay.go#L235](../src/wal/replay.go#L235)

#### replayCheckpoint

处理 `OpTypeCheckpoint` 条目：

**算法:**

```
1. 从条目 LSN 和时间戳构造 Checkpoint 对象
2. 调用 replayer.OnCheckpoint(checkpoint)
3. 如果回调失败则返回错误
```

**用例:** 允许应用跟踪恢复进度

**源码:** [replay.go#L285](../src/wal/replay.go#L285)

### 便利函数

#### ReplayFromReader

从目录打开 reader 并回放：

```go
func ReplayFromReader(ctx context.Context, dir string, replayer Replayer, opts ReplayOptions) (*ReplayStats, error)
```

**源码:** [replay.go#L300](../src/wal/replay.go#L300)

#### ReplayFromManager

从 manager 获取 reader 并回放：

```go
func ReplayFromManager(ctx context.Context, manager Manager, replayer Replayer, opts ReplayOptions) (*ReplayStats, error)
```

**源码:** [replay.go#L310](../src/wal/replay.go#L310)

---

## 验证系统

### 概述

验证系统提供工具来检查 WAL 文件完整性，无需执行完整回放。

**源码:** [validator.go](../src/wal/validator.go)

### ValidationResult

```go
type ValidationResult struct {
    FilePath          string            // 验证的文件路径
    FileSize          int64             // 文件大小 (字节)
    TotalEntries      int               // 发现的总条目数
    ValidEntries      int               // 校验和正确的条目
    InvalidEntries    int               // 校验和不匹配的条目
    Errors            []ValidationError // 详细错误列表
    FirstErrorAtLSN   uint64            // 第一个错误的 LSN
    LastSuccessfulLSN uint64            // 最后有效的 LSN
    HeaderValid       bool              // 头部完整性
    LastCheckpointLSN uint64            // 头部中的检查点 LSN
}
```

### ValidationError

```go
type ValidationError struct {
    LSN     uint64  // 条目 LSN (如果已知)
    Offset  int64   // 文件字节偏移
    Message string  // 错误描述
    Data    string  // 十六进制转储或诊断信息
}
```

### ValidateWALFile 函数

验证单个 WAL 段文件：

**签名:**

```go
func ValidateWALFile(filePath string) (*ValidationResult, error)
```

**算法:**

```
1. 打开文件
2. 读取并验证头部:
   - 检查魔数 (0x574C414F)
   - 检查版本 (1)
   - 提取检查点 LSN
3. 跳到数据部分 (偏移 24)
4. 对每个条目:
   a. 读取条目头部 (21 字节)
   b. 验证 DataLen <= walMaxRecordSize
   c. 读取数据 + 校验和
   d. 计算 CRC64 校验和
   e. 与存储的校验和比较
   f. 如果不匹配: 递增 InvalidEntries，记录错误
   g. 如果匹配: 递增 ValidEntries
5. 返回 ValidationResult
```

**复杂度:** O(E) 其中 E 是条目数 (完整文件扫描)  
**用例:** 离线验证；诊断工具

**输出示例:**

```
文件: /data/wal/wal.000001.log
大小: 134217728 字节 (128 MB)
总条目: 45320
有效条目: 45319
无效条目: 1
第一个错误在 LSN: 45200
最后成功 LSN: 45319
头部有效: true
最后检查点 LSN: 45000

错误:
  [LSN 45200, 偏移 120450560] 校验和不匹配: calculated=0xABCD1234 stored=0xDEADBEEF
```

**源码:** [validator.go#L35](../src/wal/validator.go#L35)

### 诊断用法

启用诊断日志：

```bash
export WAL_DIAG=1
./my_app
```

输出示例：

```
[wal/DIAG] openSegment: index=1 bufCap=1048576 (reset len to 0)
[wal/DIAG] compact: offset=524288 bufLen=1048576 remaining=524288
[wal/DIAG] compacted: bufLen=524288 bufCap=1048576 offset_reset=0
[wal/DIAG] EOF, trying next segment: minBytes=21 available=0
```

---

## 核心工作流

### 工作流 1: 正常写入操作

**场景:** 应用插入新事件

**序列:**

```
1. 应用序列化事件
   ↓
2. 创建 WAL 条目 (OpTypeInsert)
   ↓
3. 调用 writer.Write(ctx, entry)
   ↓
4. FileWriter 分配 LSN = lastLSN + 1
   ↓
5. 序列化条目 + 计算校验和
   ↓
6. 追加到内存缓冲区
   ↓
7. 如果 SyncMode == "always":
      将缓冲区刷到磁盘 (fsync)
   否则如果 buffer >= BatchSizeBytes:
      将缓冲区刷到磁盘
   ↓
8. 向应用返回 LSN
   ↓
9. 应用将变更应用到内存状态
```

**时间 (SyncMode="batch"):**
- 序列化: ~1-5µs
- 缓冲区追加: ~0.1µs
- 总计: ~5-10µs (无磁盘 I/O)

**时间 (SyncMode="always"):**
- 序列化: ~1-5µs
- 缓冲区追加: ~0.1µs
- Fsync: ~1-10ms (SSD) 或 ~5-20ms (HDD)
- 总计: 每次写入 ~1-10ms

**持久性保证:** `Flush()` 返回后，条目是崩溃安全的

### 工作流 2: 批量写入操作

**场景:** 应用快速连续插入 1000 个事件

**序列:**

```
1. 应用序列化 1000 个事件
   ↓
2. 创建 1000 个 WAL 条目 (OpTypeInsert)
   ↓
3. 调用 writer.WriteBatch(ctx, entries)
   ↓
4. FileWriter:
   a. 分配 LSN (N, N+1, ..., N+999)
   b. 序列化所有条目
   c. 全部追加到缓冲区
   d. 如果 SyncMode == "always": fsync
   ↓
5. 返回 LSN 数组
   ↓
6. 应用应用所有变更
```

**时间 (SyncMode="batch"):**
- 序列化: ~1ms (1000 条目)
- 缓冲区追加: ~0.1ms
- 总计: ~1-2ms

**时间 (SyncMode="always"):**
- 序列化: ~1ms
- Fsync: ~1-10ms
- 总计: ~2-11ms

**吞吐量:** 批处理模式约 90,000-500,000 条目/秒 vs always 模式 100-1000 条目/秒

### 工作流 3: 检查点创建

**场景:** 应用完成压缩并想要创建检查点

**序列:**

```
1. 应用调用 writer.CreateCheckpoint(ctx)
   ↓
2. FileWriter:
   a. 创建 OpTypeCheckpoint 条目
   b. 追加到缓冲区
   c. 立即刷新缓冲区
   d. 跳到头部 (偏移 12)
   e. 将检查点 LSN 写入头部
   f. Fsync 文件
   g. 更新内存中的检查点
   ↓
3. 返回检查点 LSN
   ↓
4. 应用调用 manager.DeleteSegmentsBefore(checkpointLSN)
   ↓
5. FileManager:
   a. 列出所有段
   b. 对每个段:
        - 扫描最后 LSN
        - 如果 lastLSN < checkpointLSN: 删除文件
```

**时间:**
- 检查点写入: ~1-10ms
- 段删除: ~10-100ms (取决于段数)

**好处:** 将恢复时间从分钟减少到秒

### 工作流 4: 崩溃恢复

**场景:** 系统崩溃；重启并恢复状态

**序列:**

```
1. 应用创建 Manager
   ↓
2. 调用 manager.Open(ctx, cfg)
   ↓
3. FileManager:
   a. 扫描目录查找段
   b. 读取最后段头部
   c. 加载检查点 LSN
   ↓
4. 应用调用 manager.Reader(ctx)
   ↓
5. FileReader:
   a. 打开包含检查点 LSN 的第一个段
   b. 扫描到检查点 LSN
   c. 返回定位在检查点的 reader
   ↓
6. 应用调用 ReplayWAL(reader, replayer, opts)
   ↓
7. 对从检查点到末尾的每个 WAL 条目:
   a. 读取条目
   b. 调用 replayer 回调:
        - OpTypeInsert: 重建索引
        - OpTypeUpdateFlags: 标记删除/替换
        - OpTypeIndexUpdate: 应用 B 树变更
   c. 更新统计信息
   ↓
8. 返回 ReplayStats
   ↓
9. 应用恢复正常操作
```

**时间 (典型):**
- 检查点距离: 10,000-1,000,000 条目
- 回放速率: 10,000-50,000 条目/秒
- 总计: 0.2s - 100s (取决于检查点频率)

**恢复保证:** LSN ≤ lastFlushedLSN 的所有条目都被回放

### 工作流 5: 段轮转

**触发器:** 当前段大小超过 MaxSegmentSize

**序列:**

```
1. FileWriter.Write() 检测到 segmentSize > MaxSegmentSize
   ↓
2. 刷新当前缓冲区
   ↓
3. 关闭当前文件 (wal.log)
   ↓
4. 重命名: wal.log → wal.NNNNNN.log (其中 N = segmentID)
   ↓
5. 递增 segmentID
   ↓
6. 创建新的 wal.log 文件
   ↓
7. 用当前检查点 LSN 写入头部
   ↓
8. 重置缓冲区和 segmentSize
   ↓
9. 继续正常操作
```

**时间:** ~1-10ms (文件重命名 + 头部写入)  
**频率:** 每写入 1GB (默认)

---

## 设计决策与权衡

### 决策 1: 基于 LSN 的寻址 vs 基于时间戳

**决策:** 使用单调 LSN (日志序列号) 进行条目标识

**考虑的替代方案:**
- 基于时间戳 (Unix 时间戳作为标识符)
- 混合 (时间戳 + 序列号)

**比较:**

| 方面 | 基于 LSN | 基于时间戳 |
|------|----------|------------|
| **顺序** | 保证全序 | 可能有时钟偏差问题 |
| **唯一性** | 保证 (递增计数器) | 不保证 (时钟分辨率) |
| **检查点** | 精确 (LSN = 确切条目) | 模糊 (时间戳 = 范围) |
| **开销** | 每条目 8 字节 | 每条目 8 字节 |
| **恢复** | 快速 (跳到 LSN) | 慢速 (扫描时间戳) |

**理由:**
- LSN 提供全序，无时钟同步问题
- 检查点精度：LSN 标识确切条目，而非时间范围
- 更简单的实现：无需处理时钟偏差或闰秒

**成本:**
- 需要原子计数器 (通过 mutex 处理)
- LSN 在 2^64 条目后可能溢出 (不是实际问题)

### 决策 2: 同步模式 (always/batch/never)

**决策:** 提供三种同步模式及可配置的批处理参数

**模式:**

| 模式 | Fsync 策略 | 用例 | 吞吐量 | 持久性 |
|------|------------|------|--------|--------|
| **always** | 每次写入后 | 关键数据、审计日志 | 100-1000 写入/s | 最大 |
| **batch** | 每 N ms 或 M 字节 | 通用 (默认) | 10K-100K 写入/s | 高 |
| **never** | 依赖 OS 缓存 | 高吞吐量日志 | 100K-1M 写入/s | 依赖 OS |

**比较:**

```
always:    [write] → [fsync] → [write] → [fsync] → ...
           ↓ 1-10ms           ↓ 1-10ms

batch:     [write][write][write]...[fsync] (每 100ms)
           ↓ 在 10ms 内批处理 100 次写入，然后 fsync

never:     [write][write][write]... (OS 最终刷新)
           ↓ 最小延迟，依赖 OS 保证持久性
```

**理由:**
- **always:** 关键系统的最大安全性 (银行、审计跟踪)
- **batch:** 事件存储的最佳平衡 (可接受 100ms 数据丢失窗口)
- **never:** 吞吐量最重要的非关键日志

**成本:**
- 复杂性：需要维护三条代码路径
- 配置：用户必须理解权衡

**好处:**
- 灵活性：一个 WAL 实现适用于多种用例

### 决策 3: 单写入者 vs 多写入者

**决策:** 采用外部同步的单写入者模型

**考虑的替代方案:**
- 内部 mutex (每个方法加锁)
- 无锁 (基于 CAS 的 LSN 分配)
- 多写入者 (分区 LSN 空间)

**比较:**

| 方法 | 吞吐量 | 复杂性 | 正确性 |
|------|--------|--------|---------|
| **单写入者 (选择)** | 高 | 低 | 保证 |
| 内部 mutex | 中 | 中 | 保证 |
| 无锁 | 高 | 很高 | 困难 |
| 多写入者 | 很高 | 极高 | 格式复杂 |

**理由:**
- 单一变更点简化了 LSN 顺序的推理
- 调用者可以批量写入以获得高吞吐量 (WriteBatch)
- 大多数事件存储无论如何都有单写入 goroutine
- 避免内部协调 (WAL 内部无锁争用)

**成本:**
- 调用者必须序列化写入 (外部 mutex 或单 goroutine)

**好处:**
- 简单、快速的实现
- WAL 代码内部无锁争用
- 清晰的所有权模型

### 决策 4: 段大小 (1GB 默认)

**决策:** 默认 MaxSegmentSize = 1GB 并提供配置选项

**分析:**

| 段大小 | 文件数 (1TB) | 轮转开销 | 检查点粒度 | 恢复 I/O |
|--------|-------------|---------|-----------|----------|
| 100MB | 10,000 | 高 (每 10s) | 细 | 低 |
| 1GB (选择) | 1,000 | 中 (每 100s) | 中 | 中 |
| 10GB | 100 | 低 (每 1000s) | 粗 | 高 |

**理由:**
- **1GB 平衡:**
  - 文件数量 (可管理 1000 个文件)
  - 轮转频率 (不太频繁)
  - 检查点粒度 (每次可删除约 1GB)
  - 内存消耗 (缓冲区 << 段大小)

**成本:**
- 最小磁盘空间：每个检查点间隔 1GB (无法回收部分段)

**好处:**
- 适用于典型的事件存储工作负载 (100-1000 写入/s × 1KB/条目 = 约 1000s 内达到 1GB)

### 决策 5: CRC64-ECMA vs 其他校验和

**决策:** 使用 CRC64-ECMA 进行条目完整性检查

**考虑的替代方案:**
- CRC32 (32 位校验和)
- SHA256 (密码学哈希)
- xxHash (快速非密码学哈希)

**比较:**

| 算法 | 大小 | 速度 | 错误检测 | 密码学 |
|------|------|------|---------|--------|
| CRC32 | 4B | 很快 | 好 | 否 |
| **CRC64-ECMA** | 8B | 快 | 优秀 | 否 |
| xxHash | 8B | 很快 | 好 | 否 |
| SHA256 | 32B | 慢 | 优秀 | 是 |

**理由:**
- CRC64 对随机和突发错误提供优秀的错误检测
- 现代 CPU 上有硬件加速 (CLMUL 指令)
- 8 字节是可接受的开销 (对典型的 100 字节条目约 6-8%)
- 不需要密码学安全 (WAL 不是对抗性的)

**成本:**
- 每条目 8 字节 (vs CRC32 的 4 字节)

**好处:**
- 检测几乎所有损坏 (错误概率 < 10^-18)
- 快速计算 (使用加速每字节约 5-10 周期)

---

## 性能分析

### 复杂度分析

| 操作 | 时间复杂度 | 空间复杂度 | I/O 操作 |
|------|-----------|------------|----------|
| **Write** (always) | O(D) | O(D) | 1 write + 1 fsync |
| **Write** (batch) | O(D) | O(D) | 批处理 (摊销) |
| **WriteBatch** (N 条目) | O(N × D) | O(N × D) | 1 write + 1 fsync |
| **Flush** | O(B) | O(1) | 1 write + 1 fsync |
| **CreateCheckpoint** | O(1) | O(1) | 1 write + 1 fsync + 头部更新 |
| **Read** | O(D) | O(B) | 缓冲读取 |
| **Replay** (E 条目) | O(E × D × P) | O(B) | 顺序扫描 |
| **DeleteSegments** | O(S) | O(1) | S 次文件删除 |
| **Stats** | O(S) | O(1) | S 次头部读取 |

**图例:**
- D = 平均条目数据大小 (字节)
- N = 批处理大小 (条目数)
- B = 缓冲区大小 (字节)
- E = WAL 中的条目数
- P = 每条目处理时间 (应用回调)
- S = 段数

### 吞吐量估计

**写入吞吐量 (条目/秒):**

| 同步模式 | 条目大小 | 吞吐量 | 限制因素 |
|---------|---------|--------|----------|
| always | 100B | 100-1,000/s | Fsync 延迟 (~1-10ms) |
| always | 1KB | 100-1,000/s | Fsync 延迟 |
| batch (100ms) | 100B | 50,000-200,000/s | CPU (序列化) |
| batch (100ms) | 1KB | 20,000-100,000/s | CPU + 磁盘带宽 |
| batch (10ms) | 100B | 10,000-50,000/s | Fsync 开销 |
| never | 100B | 500,000-2,000,000/s | CPU |
| never | 1KB | 200,000-1,000,000/s | 内存带宽 |

**测量于:**
- CPU: AMD Ryzen 7 5800X (8 核, 3.8 GHz)
- 磁盘: NVMe SSD (Samsung 970 EVO, ~500K IOPS)
- OS: Linux 5.15, ext4 文件系统

**回放吞吐量 (条目/秒):**

| 条目类型 | Replayer 复杂度 | 吞吐量 |
|---------|----------------|--------|
| OpTypeCheckpoint | 最小 | 1,000,000/s |
| OpTypeUpdateFlags | 索引查找 | 100,000-500,000/s |
| OpTypeIndexUpdate | B 树修改 | 50,000-200,000/s |
| OpTypeInsert | 完整反序列化 + 索引 | 10,000-50,000/s |

**典型恢复时间:**

```
场景: 自上次检查点以来 1,000,000 条目
平均条目类型: 60% Insert, 30% UpdateFlags, 10% Checkpoint
加权平均吞吐量: ~30,000 条目/s
恢复时间: 1,000,000 / 30,000 = ~33 秒
```

### 延迟估计

**写入延迟 (p50/p99/p999):**

| 同步模式 | p50 | p99 | p999 | 备注 |
|---------|-----|-----|------|------|
| always (SSD) | 2ms | 8ms | 15ms | Fsync 主导 |
| always (HDD) | 10ms | 25ms | 50ms | 旋转延迟 |
| batch | 10µs | 100ms | 150ms | 缓冲直到刷新 |
| never | 5µs | 20µs | 100µs | 仅内存 |

**刷新延迟:**

```
SSD (NVMe):      1-3ms (p50),   5-10ms (p99)
SSD (SATA):      3-8ms (p50),   10-20ms (p99)
HDD (7200 RPM):  8-15ms (p50),  20-50ms (p99)
```

### 内存使用

**FileWriter 内存:**

```
缓冲区 (默认):      10MB (可通过 BatchSizeBytes 配置)
文件句柄:           ~1KB
段元数据:           ~100 字节
每个 writer 总计:   ~10MB
```

**FileReader 内存:**

```
缓冲区 (初始):      1MB
缓冲区 (最大):      ~100MB (如果遇到大条目)
文件句柄:           ~1KB
段列表:             每个段 ~100 字节
每个 reader 总计:   ~1-100MB
```

**Manager 内存:**

```
Writer:            ~10MB
检查点列表:        每个检查点 ~100 字节
段列表:            每个段 ~100 字节
总计:              ~10-20MB
```

### 磁盘空间使用

**段空间:**

```
活跃段:            0 - MaxSegmentSize (增长中)
已轮转段:          每个 MaxSegmentSize
头部开销:          每个段 24 字节
条目开销:          每条目 29 字节 + data_len
```

**示例 (每天 100 万条目，平均 500 字节):**

```
原始数据:          1M × 500B = 500MB/天
开销:              1M × 29B = 29MB/天
总计:              每天 ~530MB
7 天后:            ~3.7GB
30 天后:           ~16GB
```

**清理策略:**

```
如果每 10 万条目检查点一次:
  - 每百万条目 10 个检查点
  - 可删除最后检查点前的段
  - 保留 ~10 万条目 = ~53MB 活跃 WAL
```

### 瓶颈识别

**瓶颈分析:**

| 工作负载 | 可能的瓶颈 | 缓解措施 |
|---------|-----------|----------|
| 高频写入 (always) | Fsync 延迟 | 使用 batch 模式 |
| 高频写入 (batch) | CPU (CRC64) | 使用更大的批处理间隔 |
| 大条目 (>10KB) | 内存带宽 | 增加缓冲区大小 |
| 多个段 | 段打开/关闭 | 增加 MaxSegmentSize |
| 恢复 (多条目) | 反序列化 CPU | 更频繁使用检查点 |
| 恢复 (索引更新) | B 树争用 | 分多遍回放 |

---

## 故障排查与调试

### 常见问题

#### 问题 1: 写入延迟峰值

**症状:**
- p99 延迟 >> p50 延迟
- 每约 100ms 周期性减速 (批处理模式)

**原因:**
- Fsync 争用 (多个写入者)
- 磁盘队列饱和
- OS 页缓存刷新

**诊断:**

```bash
# 检查磁盘 I/O 等待
iostat -x 1

# 监控 fsync 调用
strace -e trace=fsync -p <PID>

# 检查 WAL 统计
# 在应用代码中:
stats, _ := manager.Stats(ctx)
fmt.Printf("WAL 大小: %d 字节, 段数: %d\n", 
    stats.TotalSegmentSize, stats.CheckpointCount)
```

**解决方案:**

```go
// 降低 fsync 频率
cfg := wal.Config{
    SyncMode:        "batch",
    BatchIntervalMs: 200,  // 从 100ms 增加
    BatchSizeBytes:  20 * 1024 * 1024, // 增加缓冲区
}

// 或切换到 NVMe SSD 以获得更低的 fsync 延迟
```

#### 问题 2: 读取时校验和不匹配

**症状:**
- 错误: `checksum mismatch: got 0xABCD1234, want 0xDEADBEEF`
- 恢复在 WAL 中途失败

**原因:**
- 磁盘损坏 (坏扇区)
- 写入期间崩溃 (不完整条目)
- 软件错误 (序列化不正确)

**诊断:**

```bash
# 验证 WAL 文件
export WAL_DIAG=1
go run cmd/wal-validator/main.go /data/wal/wal.000123.log

# 检查磁盘健康
smartctl -a /dev/nvme0n1
```

**解决方案:**

```
如果损坏在 WAL 末尾:
  - 崩溃后预期 (不完整写入)
  - 恢复在最后有效条目处停止
  - 无数据丢失 (WAL 保证最后有效 LSN 之前的条目已应用)

如果损坏在 WAL 中间:
  - 磁盘硬件问题
  - 运行文件系统检查: fsck (Linux) 或 chkdsk (Windows)
  - 如果可用则从备份恢复
```

#### 问题 3: 恢复时间过长

**症状:**
- 启动时间 > 1 分钟
- 检查点之间有许多条目

**原因:**
- 检查点不频繁
- replayer 回调慢 (复杂索引逻辑)

**诊断:**

```go
// 计时恢复
start := time.Now()
stats, err := wal.ReplayFromManager(ctx, manager, replayer, opts)
elapsed := time.Since(start)

fmt.Printf("恢复: %d 条目在 %v = %d 条目/s\n",
    stats.EntriesProcessed, elapsed, 
    int(stats.EntriesProcessed / elapsed.Seconds()))
```

**解决方案:**

```go
// 更频繁地创建检查点
// 在每个主要操作后:
checkpointLSN, _ := writer.CreateCheckpoint(ctx)
manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// 优化 replayer 回调 (使用批量操作)
func (r *Replayer) OnInsert(ctx context.Context, event *types.Event, loc types.RecordLocation) error {
    // 批量索引更新而非逐个
    r.batch = append(r.batch, event)
    if len(r.batch) >= 1000 {
        return r.flushBatch()
    }
    return nil
}
```

#### 问题 4: 磁盘空间耗尽

**症状:**
- 错误: `no space left on device`
- WAL 段累积

**原因:**
- 无检查点清理
- MaxSegmentSize 非常大
- 高写入速率

**诊断:**

```bash
# 检查 WAL 目录大小
du -sh /data/wal

# 列出段
ls -lh /data/wal/*.log

# 检查统计
stats, _ := manager.Stats(ctx)
fmt.Printf("总 WAL 大小: %d GB\n", stats.TotalSegmentSize / (1<<30))
```

**解决方案:**

```go
// 检查点后定期清理
checkpointLSN, _ := writer.CreateCheckpoint(ctx)
err := manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// 减小段大小
cfg.MaxSegmentSize = 512 * 1024 * 1024 // 512MB 而非 1GB

// 增加检查点频率 (见问题 3)
```

### 调试工具

#### 启用诊断日志

```bash
export WAL_DIAG=1
./my_app
```

**输出:**

```
[wal/DIAG] openSegment: index=1 bufCap=1048576 (reset len to 0)
[wal/DIAG] compact: offset=524288 bufLen=1048576 remaining=524288
[wal/DIAG] compacted: bufLen=524288 bufCap=1048576 offset_reset=0
[wal/DIAG] readNextInternal: opType=1 lsn=12345 dataLen=150
```

#### 验证 WAL 文件

使用验证工具：

```go
result, err := wal.ValidateWALFile("/data/wal/wal.000001.log")
if err != nil {
    log.Fatal(err)
}

fmt.Printf("文件: %s\n", result.FilePath)
fmt.Printf("大小: %d 字节\n", result.FileSize)
fmt.Printf("总条目: %d\n", result.TotalEntries)
fmt.Printf("有效: %d, 无效: %d\n", 
    result.ValidEntries, result.InvalidEntries)

for _, verr := range result.Errors {
    fmt.Printf("[LSN %d, 偏移 %d] %s: %s\n",
        verr.LSN, verr.Offset, verr.Message, verr.Data)
}
```

#### 检查 WAL 内容

使用 cmd/wal-hexdump 进行低级检查：

```bash
# 转储前 100 条目
go run cmd/wal-hexdump/main.go -file /data/wal/wal.log -count 100

# 输出:
# Entry 1: LSN=1 OpType=Insert Timestamp=1234567890 DataLen=150
# Entry 2: LSN=2 OpType=Insert Timestamp=1234567891 DataLen=200
# ...
```

#### 监控 WAL 统计

```go
ticker := time.NewTicker(10 * time.Second)
defer ticker.Stop()

for range ticker.C {
    stats, err := manager.Stats(ctx)
    if err != nil {
        log.Printf("统计错误: %v", err)
        continue
    }
    
    fmt.Printf("WAL 统计: LSN=%d 检查点=%d 大小=%d MB FirstLSN=%d\n",
        stats.CurrentLSN, stats.CheckpointCount, 
        stats.TotalSegmentSize / (1<<20), stats.FirstLSN)
}
```

---

## API 快速参考

### 工厂函数

```go
// 创建新 writer
writer := wal.NewFileWriter()

// 创建新 reader
reader := wal.NewFileReader()

// 创建新 manager
manager := wal.NewFileManager()
```

### Writer API

```go
// 初始化
err := writer.Open(ctx, cfg)

// 写入单个条目
lsn, err := writer.Write(ctx, &wal.Entry{
    Type: wal.OpTypeInsert,
    EventDataOrMetadata: data,
})

// 批量写入
lsns, err := writer.WriteBatch(ctx, entries)

// 强制刷新
err := writer.Flush(ctx)

// 创建检查点
lsn, err := writer.CreateCheckpoint(ctx)

// 获取最后 LSN
lastLSN := writer.LastLSN()

// 关闭
err := writer.Close()
```

### Reader API

```go
// 在 LSN 处初始化
err := reader.Open(ctx, "/data/wal", startLSN)

// 读取下一个条目
entry, err := reader.Read(ctx)
if err == io.EOF {
    // WAL 结束
}

// 获取最后有效 LSN
lastLSN := reader.LastValidLSN()

// 关闭
err := reader.Close()
```

### Manager API

```go
// 初始化
err := manager.Open(ctx, cfg)

// 获取 writer
writer := manager.Writer()

// 在检查点获取 reader
reader, err := manager.Reader(ctx)

// 获取检查点
checkpoint, err := manager.LastCheckpoint()
checkpoints := manager.Checkpoints()

// 清理
err := manager.DeleteSegmentsBefore(ctx, beforeLSN)

// 统计
stats, err := manager.Stats(ctx)

// 关闭
err := manager.Close()
```

### Replay API

```go
// 从 reader 回放
stats, err := wal.ReplayWAL(ctx, reader, replayer, wal.ReplayOptions{
    StartLSN:    0,
    StopOnError: true,
    Serializer:  nil, // 使用默认
})

// 从目录回放
stats, err := wal.ReplayFromReader(ctx, "/data/wal", replayer, opts)

// 从 manager 回放
stats, err := wal.ReplayFromManager(ctx, manager, replayer, opts)
```

### Validation API

```go
// 验证单个文件
result, err := wal.ValidateWALFile("/data/wal/wal.000001.log")

fmt.Printf("有效: %d, 无效: %d\n", 
    result.ValidEntries, result.InvalidEntries)

for _, verr := range result.Errors {
    fmt.Printf("LSN %d 错误: %s\n", verr.LSN, verr.Message)
}
```

### 关键常量

```go
const (
    walHeaderSize    = 24               // 头部大小 (字节)
    walMagic         = 0x574C414F       // 魔数 ('WLAO')
    walVersion       = 1                // 格式版本
    walBaseName      = "wal.log"        // 活跃段名称
    walReadBufSize   = 1024 * 1024      // 1MB 读取缓冲区
    walMaxRecordSize = 100 * 1024 * 1024 // 100MB 最大条目大小
)
```

### 错误类型

```go
// EOF 表示 WAL 结束
io.EOF

// 校验和不匹配 (损坏)
fmt.Errorf("checksum mismatch: got 0x%X, want 0x%X", calculated, stored)

// 无可用检查点
fmt.Errorf("no checkpoints")

// 条目过大
fmt.Errorf("WAL entry too large: %d bytes (max %d)", size, walMaxRecordSize)
```

---

## 结论

### 核心价值主张

`wal` 包为 Nostr 事件存储提供了**崩溃恢复持久性**，具有：

1. **强保证:** 所有已刷新的条目在崩溃后保留
2. **高吞吐量:** 批处理模式下 10K-100K 写入/秒，延迟可接受
3. **快速恢复:** 基于检查点的回放 (秒到分钟，而非小时)
4. **空间效率:** 自动清理旧段
5. **完整性验证:** CRC64 校验和检测损坏

### 关键特性回顾

| 特性 | 好处 |
|------|------|
| **预写日志** | 崩溃后无数据丢失 (所有已提交的变更被保留) |
| **批量 Fsync** | 相比每写入 fsync，吞吐量提高 10-100 倍 |
| **检查点** | 恢复快 10-100 倍 (跳过已应用的条目) |
| **段轮转** | 可管理的文件大小，高效清理 |
| **CRC64 校验和** | 检测损坏，错误概率 <10^-18 |
| **多操作类型** | 支持事件、标志、索引、检查点 |

### 维护者指南

**定期维护:**

```go
// 1. 主要操作后创建检查点
checkpointLSN, _ := writer.CreateCheckpoint(ctx)

// 2. 清理旧段
manager.DeleteSegmentsBefore(ctx, checkpointLSN)

// 3. 监控统计
stats, _ := manager.Stats(ctx)
if stats.TotalSegmentSize > maxWALSize {
    // 告警或触发清理
}

// 4. 周期性验证完整性
result, _ := wal.ValidateWALFile(segmentPath)
if result.InvalidEntries > 0 {
    // 告警或调查
}
```

**性能调优:**

```go
// 高吞吐量 (批处理模式)
cfg := wal.Config{
    SyncMode:        "batch",
    BatchIntervalMs: 100,           // 调整: 50-200ms
    BatchSizeBytes:  10 * 1024 * 1024, // 调整: 5-50MB
}

// 最大持久性 (always 模式)
cfg := wal.Config{
    SyncMode: "always",
}

// 最大吞吐量 (never 模式) - 不推荐用于生产
cfg := wal.Config{
    SyncMode: "never",
}
```

**容量规划:**

```
WAL 磁盘空间 = 写入速率 (字节/秒) × 检查点间隔 (秒)

示例:
  - 写入速率: 1000 条目/秒 × 500 字节 = 500 KB/秒
  - 检查点间隔: 3600 秒 (1 小时)
  - WAL 空间: 500 KB/秒 × 3600 秒 = 1.8 GB

建议: 为安全起见，预留预期 WAL 大小的 2-3 倍
```

### 未来增强

潜在改进 (尚未实现):

1. **压缩:** 写入前压缩条目 (减少磁盘空间 2-5 倍)
2. **加密:** 支持加密的 WAL 段 (静态加密)
3. **复制:** 将 WAL 流式传输到副本以实现高可用性
4. **并行回放:** 多线程回放以加快恢复
5. **异步 Fsync:** 将 fsync 与写入路径解耦 (需要仔细排序)

### 相关文档

- [Storage 包](storage.md) - 持久化事件存储
- [Index 包](index.md) - B 树索引 (使用 WAL 保证持久性)
- [Recovery 包](recovery.md) - 系统恢复协调
- [EventStore 包](eventstore.md) - 高级 API (透明使用 WAL)

---

**文档结束**
