# Storage 包设计与实现指南

**目标读者:** 开发者、架构师和维护者  
**最后更新:** 2026年2月27日  
**语言:** 中文

## 目录

1. [概述](#概述)
2. [架构与设计理念](#架构与设计理念)
3. [核心数据结构](#核心数据结构)
4. [接口定义](#接口定义)
5. [TLVSerializer 模块](#tlvserializer-模块)
6. [FileSegment 模块](#filesegment-模块)
7. [FileSegmentManager 模块](#filesegmentmanager-模块)
8. [Scanner 模块](#scanner-模块)
9. [核心工作流](#核心工作流)
10. [设计决策与权衡](#设计决策与权衡)
11. [性能分析](#性能分析)
12. [故障排查与调试](#故障排查与调试)
13. [API 快速参考](#api-快速参考)

---

## 概述

`storage` 包是 Nostr 事件存储的基础持久化层。它提供：

- **事件序列化:** 使用 TLV 编码将 Nostr 事件与二进制格式相互转换
- **基于段的存储:** 经过优化的追加写文件段，适合顺序 I/O
- **基于位置的寻址:** 两元组寻址 (segmentID, offset) 以实现高效索引
- **多页支持:** 透明处理超过页面大小的事件
- **并发访问:** 通过 RWMutex 保护的线程安全操作
- **扫描能力:** 支持前向和反向扫描事件

### 关键特性

| 属性 | 值 | 理由 |
|------|------|------|
| **模型** | 仅追加 | 简化并发性，支持 WAL 集成 |
| **页面大小** | 4KB/8KB/16KB (可配置) | 磁盘 I/O 效率、缓存对齐 |
| **最大记录大小** | 100MB (硬限制) | 内存安全、验证边界 |
| **寻址方式** | (segmentID, offset) | 轻量级索引值，避免指针 |
| **标志位** | 持久化在头信息中 | 支持逻辑删除和替换 |
| **并发性** | 每个段一个 RWMutex | 实际上的无锁读取 |

---

## 架构与设计理念

### 系统设计原则

1. **一写多读 (WORM):** 段文件从不修改历史数据，新操作创建新记录
2. **页面对齐:** 所有数据组织为固定大小的页面以实现高效磁盘操作
3. **基于位置的寻址:** 轻量级 (segmentID, offset) 元组替代指针间接引用
4. **透明的多页处理:** 大型事件自动分割到多个页面，使用魔数验证
5. **持久化状态:** 所有状态变化立即反映在段头信息中以实现崩溃恢复
6. **分层接口:** 后端无关的设计支持格式和实现更改

### 依赖关系图

storage 包被以下包使用：

```
eventstore/    (高层存储抽象)
index/         (通过位置定位记录)
recovery/      (恢复时读取段)
shard/         (每个分片的存储实例)
query/         (查询引擎扫描)
    ↑
storage/       (核心持久化层)
    ↓
types/         (核心数据类型: Event, EventFlags, RecordLocation)
```

### 设计层次

```
┌─────────────────────────────────┐
│  应用层 (eventstore)             │
├─────────────────────────────────┤
│      Store 接口                  │
│  ┌──────────────────────────────┤
│  │  SegmentManager              │
│  │  ┌─────────────────────────┐ │
│  │  │ Open/RotateSegment      │ │
│  │  │ GetSegment/ListSegments │ │
│  │  └─────────────────────────┘ │
│  └──────────────────────────────┤
├─────────────────────────────────┤
│     Segment 抽象                 │
│  ┌──────────────────────────────┤
│  │ FileSegment (实现)           │
│  │ ┌─────────────────────────┐ │
│  │ │ 多页逻辑                │ │
│  │ │ 并发读写                │ │
│  │ └─────────────────────────┘ │
│  └──────────────────────────────┤
├─────────────────────────────────┤
│   Scanner 抽象                   │
│   • Scanner (前向)               │
│   • ReverseScanner (反向)        │
├─────────────────────────────────┤
│   序列化 (TLVSerializer)          │
│   • 事件 → 二进制                 │
│   • 二进制 → 事件                 │
├─────────────────────────────────┤
│   页面 I/O (PageWriter/Reader)   │
│   • 磁盘抽象                     │
└─────────────────────────────────┘
```

---

## 核心数据结构

### Record 结构

存储在段中的每条记录具有以下布局：

```
┌────────────────────────────────────────────────┐
│ 字段              │ 类型      │ 大小  │ 说明  │
├────────────────────────────────────────────────┤
│ Length             │ uint32    │ 4B    │ 记录总字节数 (包括头) │
│ Flags              │ byte      │ 1B    │ 删除、替换、多页标志 │
│ [ContinuationCount]│ uint16    │ 2B*   │ *仅在 FlagContinued 设置时 │
│ ID                 │ [32]byte  │ 32B   │ 事件 ID (SHA256 哈希) │
│ Pubkey             │ [32]byte  │ 32B   │ 创建者公钥 │
│ CreatedAt          │ uint32    │ 4B    │ Unix 时间戳 │
│ Kind               │ uint16    │ 2B    │ 事件种类 │
│ Tags Length        │ uint32    │ 4B    │ TLV 编码标签字节数 │
│ Tags Data          │ []byte    │ Var   │ TLV 格式 (参见第5章) │
│ Content Length     │ uint32    │ 4B    │ 内容字节数 │
│ Content            │ []byte    │ Var   │ 事件内容字符串 │
│ Signature          │ [64]byte  │ 64B   │ Schnorr 签名 │
│ Reserved           │ byte      │ 1B    │ 未来扩展性 │
└────────────────────────────────────────────────┘
```

**二进制布局示例 (单页记录):**

```
字节范围  | 字段
──────────┼─────────────────
0-3       | Length = 0x0000_02AB (683 字节)
4         | Flags = 0x00 (无标志)
5-36      | ID (32 字节)
37-68     | Pubkey (32 字节)
69-72     | CreatedAt
73-74     | Kind
75-78     | TagsLength = 0x00_00_00_FF (255 字节)
79-333    | Tags (255 字节, TLV 格式)
334-337   | ContentLength
338-...   | Content
...       | Signature (64 字节)
...       | Reserved (1 字节)
```

### RecordLocation 结构

标识存储层中一条记录的位置元组：

```go
type RecordLocation struct {
    SegmentID uint32  // 段文件标识符 (如 data.5.seg)
    Offset    uint32  // 段内字节偏移量 (在头页后)
}
```

**为什么使用两元组寻址?**
- 轻量级: 每个索引条目 8 字节 vs 指针 (64 位系统 16+ 字节)
- 平台无关: 在序列化/反序列化中存活
- 自包含: 支持高效的段本地元数据
- 无碰撞: 直接映射到磁盘位置

### ContinuationPage 结构

当记录超过页面大小时，其数据分散在多个"续页"中：

```
续页布局:
┌──────────────────────────────────────────┐
│ 偏移  │ 字段          │ 大小 │ 值        │
├──────────────────────────────────────────┤
│ 0-3   │ Magic         │ 4B   │ 0x434F4E54│ ('CONT' ASCII)
│ 4-5   │ ChunkLen      │ 2B   │ 数据大小  │
│ 6-N   │ ChunkData     │ Var  │ 载荷      │
│ N+1-..│ Padding       │ Var  │ 填充      │
└──────────────────────────────────────────┘
```

**多页记录组装:**

```
第一页 (包含 RecordLocation 头):
  Flags = 0x80 (FlagContinued 已设置)
  ContinuationCount = 2 (需要 2 个续页)
  [部分记录数据...]

续页 1:
  Magic = 0x434F4E54 ('CONT')
  ChunkLen = <续页 1 大小>
  [续页数据 1...]

续页 2:
  Magic = 0x434F4E54 ('CONT')
  ChunkLen = <续页 2 大小>
  [续页数据 2...]

→ 重组后: first_data + cont1_data + cont2_data
```

### EventFlags 枚举

存储在记录头中的持久化标志：

```go
const (
    FlagDeleted   = 0x01  // 位 0: 逻辑删除标记
    FlagReplaced  = 0x02  // 位 1: 记录被较新版本替换
    FlagReserved2 = 0x04  // 位 2: 保留
    FlagReserved3 = 0x08  // 位 3: 保留
    FlagReserved4 = 0x10  // 位 4: 保留
    FlagReserved5 = 0x20  // 位 5: 保留
    FlagReserved6 = 0x40  // 位 6: 保留
    FlagContinued = 0x80  // 位 7: 记录跨越多页
)
```

**标志语义:**
- **FlagDeleted**: 事件被逻辑删除时设置；扫描时过滤时取消设置
- **FlagReplaced**: 事件被 NIP-16 相同种类/公钥/标签组合替换时设置
- **FlagContinued**: 记录大小 ≥ page_size 时设置；表示需要多页重组
- **保留位**: 可供未来使用，无需格式更改

---

## 接口定义

### PageWriter 接口

用于写入固定大小页面的抽象：

```go
type PageWriter interface {
    WritePage(page []byte) (int, error)  // 写一个页面，返回已写字节数
    Flush() error                         // 同步到磁盘 (fsync)
    Close() error                         // 释放资源
}
```

**实现:**
- `FilePageWriter`: 直接文件 I/O 包装器

**说明:**
- 页面大小必须与段配置匹配 (4KB/8KB/16KB)
- 实现者负责适当的填充
- Flush 确保崩溃时的持久性

### PageReader 接口

用于读取固定大小页面的抽象：

```go
type PageReader interface {
    ReadPage(page []byte) (int, error)  // 将一个页面读入缓冲区
    Close() error                        // 释放资源
}
```

**实现:**
- `FilePageReader`: 直接文件 I/O 包装器

**说明:**
- 没有更多页面可用时返回 `io.EOF`
- 实现者负责边界检查

### Segment 接口

单个段文件操作的核心抽象：

```go
type Segment interface {
    ID() uint32                              // 段标识符
    Append(record *Record) (uint32, error)   // 追加记录，返回偏移量
    AppendBatch(records []*Record) ([]uint32, error)  // 原子批量追加
    Read(offset uint32) (*Record, error)     // 读取指定偏移处的记录
    IsFull() bool                            // 检查段是否满
    Size() uint64                            // 当前段大小 (字节)
    Write() error                            // 将头信息持久化到磁盘
    Close() error                            // 最终化并关闭
}
```

**实现:**
- `FileSegment`: 文件支持的持久存储

**并发保证:**
- 所有操作通过内部 RWMutex 线程安全
- 多个读者 = 无锁争用
- 写操作序列化

### SegmentManager 接口

管理多个段文件的生命周期：

```go
type SegmentManager interface {
    Open(path string) error                       // 从目录初始化
    CurrentSegment() (Segment, error)             // 获取活动可写段
    RotateSegment() (Segment, error)              // 创建新段，返回旧段
    GetSegment(id uint32) (Segment, error)        // 按 ID 检索段
    ListSegments() ([]uint32, error)              // 所有段 ID
    DeleteSegment(id uint32) error                // 删除段文件
    Flush() error                                 // 同步所有段
    Close() error                                 // 关闭
}
```

**实现:**
- `FileSegmentManager`: 基于磁盘的段管理

**不变式:**
- 同时只有一个可写段
- 段 ID 单调递增
- 已删除段不重用

### EventSerializer 接口

将事件序列化/反序列化为/来自二进制记录：

```go
type EventSerializer interface {
    Serialize(event *types.Event) (*Record, error)    // 事件 → Record
    Deserialize(record *Record) (*types.Event, error) // Record → 事件
    SizeHint(event *types.Event) uint32               // 估计字节数
}
```

**实现:**
- `TLVSerializer`: TLV 编码格式 (参见第5章)

**不变式:**
- Deserialize(Serialize(e)) ≈ e (语义保留)
- SizeHint ≥ 实际序列化大小
- 所有事件字段持久化，除内部状态

### Store 接口

结合所有组件的顶层抽象：

```go
type Store interface {
    Open(path string) error                              // 初始化
    Close() error                                        // 关闭
    WriteEvent(event *types.Event) (*RecordLocation, error)  // 持久化事件
    ReadEvent(loc *RecordLocation) (*types.Event, error) // 检索事件
    UpdateEventFlags(loc *RecordLocation, flags byte) error // 标记为删除/替换
    Flush() error                                        // 同步所有状态
}
```

**典型实现流程:**
1. 通过 EventSerializer 序列化事件
2. 通过 SegmentManager 追加到当前段
3. 返回 RecordLocation 给调用者 (用于索引)
4. 读取时反向此过程

---

## TLVSerializer 模块

### 目的

将 Nostr 事件编码为与段存储兼容的二进制格式，并将其解码回来。TLV (Tag-Length-Value) 格式提供：

- **灵活性**: 易于添加新标签类型，不破坏旧读者
- **紧凑性**: 可选字段无浪费空间
- **可扩展性**: 客户端无需完整反序列化即可过滤标签类型

### 序列化算法

```
输入: Nostr 事件
  ↓
[1] 大小估计
    estimated_size = fixed_fields (155B) + tags_bytes + content_bytes
    if estimated_size ≥ page_size:
        设置 FlagContinued 标志
        计算 continuation_count = ceil((size - page_size) / page_size) + 1
  ↓
[2] 创建记录头
    record.Length = estimated_size
    record.Flags |= (FlagContinued 如果多页)
    record.ContinuationCount = continuation_count
  ↓
[3] 编码固定字段 (76 字节)
    • ID (32B) - 事件哈希
    • Pubkey (32B) - 创建者
    • CreatedAt (4B) - uint32 时间戳
    • Kind (2B) - 事件种类
    • Reserved (1B) - 未来使用
  ↓
[4] 编码标签 (TLV 格式)
    for each tag in event.Tags:
        TLV-encode(tag)
    result = tags_tlv_bytes
  ↓
[5] 编码内容
    content_bytes = UTF-8 encode(event.Content)
  ↓
[6] 组装记录
    record.Data = [fixed] + [tags_tlv] + [content]
  ↓
输出: 准备好追加到段的记录
```

### 标签 TLV 格式

标签采用为扫描优化的变长格式编码：

```
标签部分布局:
┌─────────────────────────────────────────┐
│ 偏移  │ 字段        │ 类型   │ 大小    │
├─────────────────────────────────────────┤
│ 0-1   │ TagCount    │ uint16 │ 2B     │
│ 2-2   │ Tag[0].Type │ byte   │ 1B     │
│ 3-4   │ Tag[0].Len  │ uint16 │ 2B     │
│ 5-5   │ Tag[0].ValCnt│ byte  │ 1B     │
│ 6-7   │ Val[0].Len  │ uint16 │ 2B     │
│ 8-N   │ Val[0].Data │ bytes  │ Var    │
│ N+1-..│ Val[1..N]   │ bytes  │ Var    │
│ ...   │ Tag[1..N]   │ ...    │ ...    │
└─────────────────────────────────────────┘

标签编码模式 (对每个标签重复):
  [type: 1B] [total_len: 2B] [value_count: 1B] [value_items...]
  
值项模式 (对标签中每个值重复):
  [value_len: 2B] [value_data: <N 字节>]
```

**示例: 两个标签**

```
标签: [["e", "abc123"], ["p", "xyz789", "relay.example.com"]]

编码:
  00 02           ← TagCount = 2
  ────────────────
  65              ← Tag[0].Type = 'e' (0x65)
  00 0B           ← Tag[0].Len = 11 字节
  01              ← Tag[0].ValCount = 1
  00 06           ← Val[0].Len = 6
  61 62 63 31 32 33 ← Val[0].Data = "abc123"
  ────────────────
  70              ← Tag[1].Type = 'p' (0x70)
  00 1C           ← Tag[1].Len = 28 字节
  02              ← Tag[1].ValCount = 2
  00 06           ← Val[0].Len = 6
  78 79 7A 37 38 39 ← Val[0].Data = "xyz789"
  00 12           ← Val[1].Len = 18
  72 65 6C 61 79 2E 65 78 61 6D 70 6C 65 2E 63 6F 6D ← "relay.example.com"
```

### 反序列化算法

```
输入: 来自段的记录
  ↓
[1] 提取固定字段
    id, pubkey, created_at, kind, reserved ← 前 76B
  ↓
[2] 解码标签 TLV
    if record.Flags & FlagContinued:
        从续页重组完整记录
    
    tag_count ← 从标签偏移读取 uint16
    tags = []
    for i=0 to tag_count:
        tag_type ← 读取字节
        tag_len ← 读取 uint16
        val_count ← 读取字节
        values = []
        for j=0 to val_count:
            val_len ← 读取 uint16
            val_data ← 读取 val_len 字节
            values.append(val_data)
        tags.append(Tag{Type: tag_type, Values: values})
  ↓
[3] 提取内容
    content_len ← 读取 uint32
    content ← 读取 content_len 字节为 UTF-8 字符串
  ↓
[4] 提取签名
    signature ← 读取 64B
  ↓
[5] 填充事件标志
    event.Flags = record.Flags (保留删除/替换状态)
  ↓
输出: 重组的事件
```

### 性能特性

| 操作 | 复杂度 | 说明 |
|------|---------|------|
| 序列化 | O(n) | n = 标签总字节数 + 内容字节数 |
| 反序列化 | O(n) | 相同；无法跳过到签名 |
| SizeHint | O(n) | 必须估计标签长度 |
| 多页分割 | O(n/p) | p = 页面大小；线性复制成本 |

### 限制与约束

```go
const (
    MaxRecordSize    = 100 * 1024 * 1024  // 100 MB 硬限制
    MaxTagCount      = math.MaxUint16      // 65,535 标签/事件
    MaxTagValueLen   = math.MaxUint16      // 65,535 字节/标签值
    MaxContentLen    = math.MaxUint32      // ~4 GB 理论最大值
)

// 反序列化中的验证:
if record_size > MaxRecordSize: error
if tag_count > MaxTagCount: error
for each tag value:
    if val_len > MaxTagValueLen: error
if content_len > MaxContentLen: error
```

---

## FileSegment 模块

### 目的

使用文件支持的持久化实现 Segment 接口。管理单个段文件 I/O、多页记录处理和线程安全并发访问。

### 文件布局

```
段文件 (data.{ID}.seg):

┌──────────────────────────────────────────────┐
│            头页 (PageSize)                   │
├──────────────────────────────────────────────┤
│ 0-3        │ Magic: 0x4E535452 ('NSTR')     │
│ 4-7        │ PageSize (如 4096)              │
│ 8-15       │ CreatedAt (int64 时间戳)       │
│ 16-19      │ SegmentID (uint32)             │
│ 20-23      │ RecordCount (uint32)           │
│ 24-27      │ NextFreeOffset (uint32)        │
│ 28-31      │ Version (uint32, 当前为 1)     │
│ 32-39      │ CompactionMarker (int64)       │
│ 40 到末尾  │ 填充 / 保留                    │
└──────────────────────────────────────────────┘

┌──────────────────────────────────────────────┐
│         数据页 (1..N)                        │
├──────────────────────────────────────────────┤
│ 每页 = PageSize 字节                         │
│ 包含 Record(s) 或续页数据                    │
│ 未对齐片段填充至页边界                      │
└──────────────────────────────────────────────┘
```

**头字段详情:**

| 字段 | 类型 | 字节 | 目的 |
|------|------|------|------|
| Magic | uint32 | 4 | 格式签名验证 |
| PageSize | uint32 | 4 | 配置的页面大小 (4K/8K/16K) |
| CreatedAt | int64 | 8 | 段创建时间戳 |
| SegmentID | uint32 | 4 | 唯一段标识符 |
| RecordCount | uint32 | 4 | 追加的总记录数 (用于统计) |
| NextFreeOffset | uint32 | 4 | 下次追加的字节偏移量 |
| Version | uint32 | 4 | 格式版本 (用于兼容性) |
| CompactionMarker | int64 | 8 | 追踪压缩状态 |

### 单页记录写入

**条件:** `recordLen < pageSize`

**算法:**

```
输入: 要追加的记录
  ↓
[1] 获取写锁
    segment.mu.Lock()
  ↓
[2] 验证空间
    if nextOffset + recordLen > maxSegmentSize:
        return ErrSegmentFull
    if recordLen > MaxRecordSize:
        return ErrRecordTooLarge
  ↓
[3] 确定对齐
    aligned_len = recordLen
    page_boundary = ((nextOffset + recordLen - 1) / pageSize + 1) * pageSize
    if (nextOffset % pageSize) + recordLen > pageSize:
        // 记录将跨越页边界
        padding = page_boundary - nextOffset - recordLen
        aligned_len = recordLen + padding
  ↓
[4] 写入文件
    seek(headerPageSize + nextOffset)
    write(record_data, recordLen)
    if aligned_len > recordLen:
        write(zeros, padding)  // 对齐到页边界
  ↓
[5] 更新头
    nextOffset += aligned_len
    recordCount += 1
    write_header_page()  // 立即持久化
  ↓
[6] 释放锁
    segment.mu.Unlock()
  ↓
输出: Offset = nextOffset - aligned_len (用于索引)
```

**对齐示例 (4KB 页面):**

```
前: nextOffset = 1000B
记录: recordLen = 3500B
跨越: [1000, 4500) 将跨越 4096B 边界
填充: 4096 - 1000 - 3500 = 0 → 无需填充
后: nextOffset = 4500B → 舍入到 8192B (下一页)
```

### 多页记录写入

**条件:** `recordLen ≥ pageSize`

**算法:**

```
输入: 要追加的记录 (大小 ≥ pageSize)
  ↓
[1] 获取写锁
    segment.mu.Lock()
  ↓
[2] 计算所需页数
    pages_needed = ceil(recordLen / pageSize)
    total_bytes = pages_needed * pageSize
  ↓
[3] 设置续页计数
    record.Flags |= FlagContinued
    record.ContinuationCount = pages_needed - 1
  ↓
[4] 写入第一页
    first_page_data_size = pageSize - 7  // 保留用于头信息
    write(record_header, 7B)
    write(first_page_data[:first_page_data_size])
    nextOffset += pageSize
  ↓
[5] 写入续页
    data_offset = first_page_data_size
    for i=1 to pages_needed-1:
        write_magic(0x434F4E54)  // 'CONT'
        chunk_size = min(remaining_data, pageSize - 6)
        write_chunk_len(chunk_size)
        write(record_data[data_offset:data_offset+chunk_size])
        data_offset += chunk_size
        nextOffset += pageSize
  ↓
[6] 更新头
    recordCount += 1
    write_header_page()
  ↓
[7] 释放锁
    segment.mu.Unlock()
  ↓
输出: Offset = <第一页头后的位置>
```

### 单页记录读取

**条件:** `record.Flags & FlagContinued == 0`

**算法:**

```
输入: 段内偏移量
  ↓
[1] 获取读锁
    segment.mu.RLock()
  ↓
[2] 读取记录头 (7 字节)
    seek(headerPageSize + offset)
    read(length, 4B)
    read(flags, 1B)
    // 如果 flags & FlagContinued: 转到多页读取
  ↓
[3] 验证记录
    if offset + length > segment.Size():
        return ErrRecordTruncated
    if length > MaxRecordSize:
        return ErrInvalidRecord
  ↓
[4] 读取完整记录
    seek(headerPageSize + offset)
    read(data, length)
  ↓
[5] 释放锁
    segment.mu.RUnlock()
  ↓
输出: 完整的记录
```

### 多页记录读取

**条件:** `record.Flags & FlagContinued != 0`

**算法:**

```
输入: 第一页的偏移量
  ↓
[1] 获取读锁
    segment.mu.RLock()
  ↓
[2] 读取记录头
    read(length, 4B)
    read(flags, 1B)
    read(continuation_count, 2B)
  ↓
[3] 读取第一页数据
    first_page_size = pageSize - 7
    read(data_buffer[:first_page_size])
  ↓
[4] 读取续页
    for i=1 to continuation_count:
        current_offset += pageSize
        read(magic, 4B)
        if magic != 0x434F4E54:
            return ErrInvalidContinuation
        read(chunk_len, 2B)
        if chunk_len > pageSize - 6:
            return ErrInvalidChunkLen
        read_and_append(chunk_len 字节到缓冲区)
  ↓
[5] 重组记录
    record.Data = concatenated_buffer
    record.Length = total_bytes_read
    record.Flags = flags_from_first_page
  ↓
[6] 释放锁
    segment.mu.RUnlock()
  ↓
输出: 完整重组的记录
```

### 并发模型

```go
type FileSegment struct {
    mu sync.RWMutex  // 保护所有可变状态
    
    // 由 mu 保护:
    nextOffset  uint32
    recordCount uint32
    header      *Header
    
    // 文件描述符本身对并发读取是安全的
    // (操作系统级处理多个读者 + 一个写者序列化)
}

// 读操作 (Scanner, Read):
//   segment.mu.RLock()
//   多个读者并行进行
//   segment.mu.RUnlock()

// 写操作 (Append, UpdateFlags):
//   segment.mu.Lock()
//   独占访问
//   segment.mu.Unlock()
```

### 线程安全保证

1. **多个并发读:**  ✓ RWMutex 支持无锁并行读
2. **写时读:** ✓ 读取看到提交状态 (写操作原子更新头)
3. **多个并发写:** ✗ 通过写锁序列化
4. **关闭后追加:** ✗ 返回 ErrSegmentClosed

---

## FileSegmentManager 模块

### 目的

管理单个目录内多个段文件的生命周期。处理段发现、创建、轮转和清理。

### 初始化 (Open)

```
输入: 目录路径
  ↓
[1] 扫描目录
    list_files(path)
    过滤 *.seg 文件
    从文件名提取段 ID
    按 ID 升序排序
  ↓
[2] 加载现有段
    for each segment ID:
        open_segment(id)
        读取头以验证格式
        检查魔数
        验证版本兼容性
    如果文件损坏: return ErrInvalidSegment
  ↓
[3] 初始化状态
    if segments found:
        nextSegmentID = max(existing_ids) + 1
        currentSegment = <必须重新打开最新可写的>
    else:
        create_new_segment(0)
        nextSegmentID = 1
  ↓
输出: 准备就绪
```

**段文件命名约定:**

```
格式: data.{ID}.seg
示例:
  data.0.seg     ← 首个创建的段
  data.1.seg     ← data.0 满后轮转
  data.42.seg    ← 第 42 个段
```

### 当前段管理

```go
type FileSegmentManager struct {
    mu               sync.RWMutex
    directory        string
    pageSize         uint32
    maxSegmentSize   uint64

    segments         map[uint32]*FileSegment  // 所有加载的段
    currentSegID     uint32                   // 当前可写段 ID
    nextSegmentID    uint32                   // 下次分配的 ID
}
```

**不变式:**
- 同时只有一个段可写 (currentSegID)
- 历史段保持只读
- 段 ID 不重用 (即使删除后)

### 段轮转

**触发:** `CurrentSegment().IsFull() == true`

**算法:**

```
输入: 无 (从 Full 条件隐含)
  ↓
[1] 获取写锁
    manager.mu.Lock()
  ↓
[2] 获取当前段
    old_seg = segments[currentSegID]
  ↓
[3] 最终化当前段
    old_seg.Write()  // 持久化头
    old_seg.Close()  // 释放文件句柄
  ↓
[4] 创建新段
    new_id = nextSegmentID++
    new_seg = FileSegment.Create(directory, new_id, pageSize)
    segments[new_id] = new_seg
    currentSegID = new_id
  ↓
[5] 释放锁
    manager.mu.Unlock()
  ↓
输出: 新段引用 (用于继续追加)
```

### 按 ID 获取段

```
manager.GetSegment(id uint32)

  ↓
manager.mu.RLock()
  seg = segments[id]
manager.mu.RUnlock()
  ↓
if seg == nil:
    return ErrSegmentNotFound
return seg
```

**用例:** 索引查找检索 (segmentID, offset) 并调用 GetSegment 获取段。

### 列出所有段

```
manager.ListSegments()

  ↓
manager.mu.RLock()
  ids = []uint32{}
  for id := range segments:
      ids.append(id)
  sort(ids)
manager.mu.RUnlock()
  ↓
return ids  // 升序
```

### 删除段

**警告:** 仅对已完成段有效，不包括 currentSegID。

```
输入: 要删除的段 ID
  ↓
[1] 获取写锁
    manager.mu.Lock()
  ↓
[2] 验证不是当前段
    if id == currentSegID:
        return ErrCannotDeleteCurrent
  ↓
[3] 获取并关闭段
    seg = segments[id]
    if seg == nil:
        manager.mu.Unlock()
        return ErrSegmentNotFound
    seg.Close()
  ↓
[4] 删除文件
    unlink(directory/data.{id}.seg)
  ↓
[5] 更新映射
    delete(segments, id)
  ↓
[6] 释放锁
    manager.mu.Unlock()
  ↓
输出: 成功或错误
```

### 刷新所有段

确保所有段头和数据同步到磁盘：

```
manager.Flush()
  ↓
manager.mu.RLock()
  segment_list = [所有内存中的段]
manager.mu.RUnlock()
  ↓
for each segment in segment_list:
    segment.Write()  // 持久化头
    segment.Flush()  // 同步 (fsync) 到磁盘
  ↓
return error (如果任何段失败)
```

**用法:** 在优雅关闭前调用，或按配置间隔调用以实现持久性。

### 关闭 (Close)

```
manager.Close()
  ↓
manager.Flush()  // 最后一次同步
  ↓
manager.mu.Lock()
  for each segment:
      segment.Close()
  segments.clear()
manager.mu.Unlock()
```

---

## Scanner 模块

### 目的

提供对段或多个段内记录的前向和反向迭代。支持批量操作：索引构建、崩溃恢复、压缩。

### 前向扫描器 (Scanner)

从段开始顺序迭代记录。

**状态:**

```go
type Scanner struct {
    segment     Segment
    currentPage uint32
    offset      int             // 页内字节偏移量 (当前记录)
    eof         bool
}
```

**Next() 方法:**

```
输入: 无
  ↓
[1] 检查 EOF
    if scanner.eof:
        return nil, io.EOF
  ↓
[2] 定位下一条记录
    offset = scanner.offset
    if offset >= pageSize - 7:  // 靠近页末尾
        scanner.currentPage++
        offset = 0
  ↓
[3] 读取记录头 (7 字节)
    (length, flags, continuation_count)
  ↓
[4] 多页?
    if flags & FlagContinued != 0:
        record = segment.Read(offset)  // 多页重组
        scanner.offset = page_boundary
    else:
        record = segment.Read(offset)  // 单页读取
        scanner.offset += length
        if (scanner.offset % pageSize) != 0:
            scanner.offset = round_up_to_page()
  ↓
[5] 返回记录
    return record, nil
  ↓
[6] 到达末尾
    scanner.eof = true
    return nil, io.EOF
```

**Seek(offset uint32) 方法:**

定位扫描器到特定偏移量 (如从崩溃恢复后)：

```
scanner.offset = offset
scanner.eof = false
```

**Reset() 方法:**

扫描器回到开始：

```
scanner.offset = 0
scanner.eof = false
```

### 反向扫描器 (ReverseScanner)

从段末尾向后迭代。

**棘手方面:** 多页记录需要前向解析以理解边界。ReverseScanner 必须：

1. 从后向前逐页扫描
2. 在每页上前向扫描以查找所有记录
3. 按反向顺序返回记录
4. 避免重访续页

**算法草图:**

```
输入: N 页段
  ↓
[1] 从末尾开始
    current_page = N - 1
    records_in_page = []
  ↓
[2] 前向扫描当前页
    offset = 0
    while offset < page_len:
        record = read_record_at(offset)
        records_in_page.append((record, offset))
        offset += record.length
        if record.flags & FlagContinued:
            offset = next_page_boundary
  ↓
[3] 按反向顺序返回记录
    for record in reverse(records_in_page):
        yield record
  ↓
[4] 移到上一页
    if page > 0:
        current_page--
        goto [2]
    else:
        return io.EOF
```

**为什么复杂?** 在 N 页开始的记录可能有续页。ReverseScanner 必须：
- 不返回同一记录两次 (追踪访问的偏移量)
- 理解页边界 (0x434F4E54 标记)

### 工具函数

#### ScanAll

扫描段中所有记录，可选过滤：

```go
func ScanAll(segment Segment, includeDeleted bool) ([]Record, error) {
    scanner := NewScanner(segment)
    records := []Record{}
    
    for {
        record, err := scanner.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return nil, err
        }
        
        if !includeDeleted && (record.Flags & FlagDeleted) != 0 {
            continue
        }
        
        records = append(records, record)
    }
    return records, nil
}
```

**复杂度:** O(n)，其中 n = 段中记录数

#### CountRecords

计算记录数 (用于统计)：

```go
func CountRecords(segment Segment, includeDeleted bool) (uint32, error) {
    scanner := NewScanner(segment)
    count := uint32(0)
    
    for {
        record, err := scanner.Next()
        if err == io.EOF {
            break
        }
        if err != nil {
            return 0, err
        }
        
        if !includeDeleted && (record.Flags & FlagDeleted) != 0 {
            continue
        }
        
        count++
    }
    return count, nil
}
```

#### FindRecord

按偏移量检索单条记录：

```go
func FindRecord(segment Segment, offset uint32) (*Record, error) {
    if offset >= segment.Size() {
        return nil, ErrOffsetOutOfBounds
    }
    return segment.Read(offset)
}
```

---

## 核心工作流

### 写入工作流

从事件创建到存储位置的完整序列：

```
事件
  ├─ (pubkey, id, created_at, kind, tags, content, sig)
  │
  ↓ [1] 序列化
  │
TLVSerializer.Serialize(event)
  ├─ EstimateSize() → 决定 FlagContinued
  ├─ EncodeTags() → TLV 格式
  ├─ EncodeContent() → UTF-8
  ├─ 使用头组装记录
  │
  ↓ [2] 请求追加
  │
Store.WriteEvent()
  ├─ 从 SegmentManager 获取当前段
  │
  ↓ [3] 追加到段
  │
Segment.Append(record)
  ├─ 获取写锁
  ├─ 检查空间和记录大小限制
  ├─ 确定对齐 (页边界)
  ├─ 写入记录 + 填充
  ├─ 更新头 (nextOffset, recordCount)
  ├─ Fsync 头页
  ├─ 释放锁
  │
  ↓ [4] 返回位置
  │
RecordLocation {
    SegmentID:  <当前段 id>,
    Offset:     <段内字节偏移量>
}
  │
  ↓ [5] 索引存储
  │
eventstore.index[event.ID] = location
```

**时间:**
- 序列化: ~1-10ms (取决于事件大小)
- 文件 I/O: ~0.5-5ms (取决于磁盘速度)
- 总计: 同步，调用者等待

### 读取工作流

从位置到重组事件的完整序列：

```
RecordLocation { SegmentID, Offset }
  │
  ↓ [1] 获取段
  │
SegmentManager.GetSegment(segmentID)
  ├─ 在内存映射中查找
  ├─ 返回引用 (无磁盘 I/O)
  │
  ↓ [2] 读取记录
  │
Segment.Read(offset)
  ├─ 获取读锁
  ├─ 定位到偏移量
  ├─ 读取 7 字节头
  ├─ 检查 FlagContinued 标志
  │   ├─ 如果设置: 多页重组
  │   │  ├─ 读取所有续页
  │   │  ├─ 验证魔数
  │   │  ├─ 连接块
  │   └─ 如果未设置: 单页读取
  ├─ 释放锁
  │
  ↓ [3] 重组记录
  │
Record {
    Length:        <...>,
    Flags:         <...>,
    Data:          <完整二进制数据>
}
  │
  ↓ [4] 反序列化
  │
TLVSerializer.Deserialize(record)
  ├─ 提取固定字段 (id, pubkey, created_at, kind, sig, reserved)
  ├─ 解码标签 TLV 部分
  ├─ 提取内容
  ├─ 从记录标志填充事件标志
  │
  ↓ [5] 返回事件
  │
types.Event {
    ID:         <32B 哈希>,
    Pubkey:     <32B 密钥>,
    CreatedAt:  <时间戳>,
    Kind:       <种类>,
    Tags:       <反序列化标签>,
    Content:    <UTF-8 字符串>,
    Sig:        <64B 签名>,
    Flags:      <删除/替换状态>
}
```

**时间:**
- 段查找: O(1) 内存
- 文件 I/O: 0.1-5ms/页 (单/多页)
- 反序列化: ~1-10ms
- 总计: 实际异步，调用者等待

### 扫描工作流

用于索引构建或崩溃恢复的批量迭代：

```
段
  │
  ↓ [1] 创建扫描器
  │
scanner := NewScanner(segment)
  ├─ 初始化 offset = 0
  ├─ 设置 eof = false
  │
  ↓ [2] 迭代
  │
for {
    record, offset, err := scanner.Next()
    if err == io.EOF {
        break
    }
    if err != nil {
        handle error
    }
    
    ↓ [3] 处理记录
    │
    event, _ := deserialize(record)
    index[event.ID] = RecordLocation{segmentID, offset}
    
    ↓ [4] 下次迭代
    
    continue
}
  │
  ↓ [5] 完成
  │
索引从段完成填充
```

**复杂度:** O(n) 线性扫描，每条记录最小开销

### 标志更新工作流

标记事件为已删除或已替换：

```
RecordLocation { segmentID, offset }
新标志 (FlagDeleted 或 FlagReplaced)
  │
  ↓ [1] 获取段
  │
segment := manager.GetSegment(segmentID)
  │
  ↓ [2] 更新标志
  │
segment.UpdateEventFlags(offset, newFlags)
  ├─ 获取写锁
  ├─ 定位到偏移量
  ├─ 读取当前标志 (记录第 4 字节)
  ├─ 与 newFlags 按位或 (bitwise)
  ├─ 写回 (相同位置)
  ├─ 释放锁
  │
  ↓ [3] 完成
  │
查询 / 扫描器现在过滤:
  • FlagDeleted 记录 (如果未明确包含)
  • FlagReplaced 记录 (如果启用过滤)
```

**效率:** 仅更新 1 字节，无数据重组

---

## 设计决策与权衡

### 1. 仅追加模型

**决策:** 从不修改历史记录；仅追加新版本或标志。

**权衡:**

| 优势 | 成本 |
|------|------|
| 简单并发性 (读取无锁) | 时间碎片化 |
| 自然崩溃恢复 (部分写明显) | 最终需要压缩 |
| WAL 集成简单 | 空间使用比红黑树高 |
| 反向扫描简单 (无指针追踪) | 需要段轮转策略 |

**理由:** Nostr 工作负载主要是读操作；写集中在索引更新 (标志)。仅追加避免读取阻塞并支持从崩溃轻松恢复。

### 2. 页面对齐

**决策:** 所有数据组织为固定大小页面 (4KB/8KB/16KB)，根据需要填充。

**权衡:**

| 优势 | 成本 |
|------|------|
| 磁盘 I/O 效率 (对齐闪存块) | 小记录空间浪费 |
| CPU 缓存友好 | 处理部分页复杂性 |
| 匹配 OS 页面大小 | 降低段容量 |

**理由:** 闪存通常使用 4KB-8KB 块。对齐最小化设备上的读-修改-写周期。也与 VM 页面对齐以支持潜在的 mmap() 优化。

### 3. 位置元组 (segmentID, Offset)

**决策:** 索引存储 8 字节 (uint32, uint32) 而非指针或文件路径。

**权衡:**

| 优势 | 成本 |
|------|------|
| 索引中轻量级 (降低内存) | 需要 SegmentManager 查询 |
| 平台无关 (可序列化) | 比直接指针慢 |
| 跨进程边界安全 | 间接寻址 |

**理由:** 大型索引 (数十亿事件) 会消耗大量内存。元组即使文件迁移后仍保持固定 (对备份重要)。

### 4. 通过续页的多页记录

**决策:** 大型记录 (>= pageSize) 使用魔数标记分割到多页。

**权衡:**

| 优势 | 成本 |
|------|------|
| 支持任意记录大小 | 重组开销 |
| 避免可变页范例 | 写/读路径复杂性 |
| 向后兼容 (旧读者忽略续页) | 反向扫描更复杂 |

**理由:** Nostr 事件通常 < 1 页，但大型自定义事件或复杂证明可能超过。续页保持格式简单，同时支持处理异常情况。

### 5. 记录头中的持久化标志

**决策:** 将 FlagDeleted 和 FlagReplaced 直接存储在记录中，不在单独索引中。

**权衡:**

| 优势 | 成本 |
|------|------|
| 崩溃安全 (无单独状态) | 每次变更单字节更新 |
| 扫描器可固有过滤删除/替换 | 需要读-修改-写 |
| 无额外内存结构 | 不能通过索引立即变更 |

**理由:** Nostr 协议需要逻辑删除语义。持久化标志充当单一真实来源，支持恢复和无辅助元数据的索引重建。

### 6. 标签的 TLV 编码

**决策:** 标签编码为变长 TLV 格式，而非固定 JSON 或数组。

**权衡:**

| 优势 | 成本 |
|------|------|
| 可扩展 (新标签类型无需重建) | 更复杂编码/解码 |
| 稀疏标签紧凑 | 无法直接在标签类型上索引 |
| 可能过滤反序列化 | 需要自定义标签解析器 |

**理由:** Nostr 标签开放式 (新类型定期添加)。TLV 在协议升级时避免重新序列化事件，同时保持序列化大小合理。

---

## 性能分析

### 复杂度分析表

| 操作 | 时间 | 空间 | 说明 |
|------|------|------|------|
| **序列化** | O(n) | O(n) | n = 标签字节数 + 内容字节数 |
| **反序列化** | O(n) | O(n) | 必须解析所有标签和内容 |
| **SizeHint** | O(n) | O(1) | 估计无分配 |
| **追加 (1 页)** | O(1) | O(1) | 单次磁盘写 + 头 |
| **追加 (多页)** | O(p) | O(1) | p = 页数 |
| **读 (1 页)** | O(1) | O(n) | 单次磁盘读；n = 记录大小 |
| **读 (多页)** | O(p) | O(n) | p 页读；在 RAM 中重组 |
| **扫描全部** | O(n) | O(k) | n = 记录；k = 过滤时保留计数 |
| **反向扫描** | O(n) | O(p) | n = 记录；p = 页数前向看 |

### 典型延迟

**假设:**
- SSD 磁盘，4KB 页，100 µs 随机 I/O
- 事件大小: 500 字节 (典型 Nostr 事件)
- 页面大小: 4KB

| 操作 | 延迟 | 组件 |
|------|------|------|
| 序列化 | 0.1 ms | RAM 中 TLV 编码 |
| 追加 (1 页) | 2-5 ms | 寻道 (?) + 写 (0.5 ms) + fsync (1-2 ms) |
| 读 (1 页) | 0.5-2 ms | 寻道 + 内存读 |
| 读 (多页, 2 页) | 1-4 ms | 2 次寻道 + 2 次读 |
| 扫描 1000 条记录 | 10-50 ms | 200-500 页读 (批处理) |

### 内存占用

**每个段内存:**

```
FileSegment 结构:
  ├─ RWMutex (48 字节)
  ├─ 元数据字段 (64 字节)
  ├─ 文件描述符 (8 字节)
  ├─ 缓冲区缓存 (变、通常 0-1 MB)
  └─ 总计: ~1-2 MB/段

每个 SegmentManager:
  ├─ 段映射 (仅键): N * 8 字节 (N 段)
  ├─ 管理器互斥量 (48 字节)
  └─ 总计: N * 8 + 100 字节

索引大小:
  (uint32, uint32) 元组: 8 字节/事件
  存储开销: 20% → ~10 字节/事件有效
  10 亿事件: ~10 GB 索引在内存中
```

### 吞吐量估计

| 场景 | 吞吐量 | 瓶颈 |
|------|-------|------|
| **顺序写 1000 事件** | 200-500 事件/秒 | Fsync (1-50ms/批) |
| **批处理写 1000 事件 (AppendBatch)** | 5000-10k 事件/秒 | 磁盘带宽 |
| **按位置读取事件** | 1000+ 事件/秒 | 磁盘寻道时间 |
| **扫描完整段** | 100k 事件/秒 | 顺序读带宽 |

---

## 故障排查与调试

### 常见问题与解决方案

#### 问题: "ErrSegmentFull"

**症状:** 写入失败并显示 ErrSegmentFull，但段看起来很小。

**原因:**
1. 由于对齐填充，nextOffset 超过 MaxSegmentSize
2. 管理器未调用 RotateSegment
3. MaxSegmentSize 设置过小

**调试步骤:**

```go
segment := manager.GetSegment(segmentID)
fmt.Printf("Segment %d: nextOffset=%d, size=%d, full=%v\n",
    segmentID, segment.nextOffset, segment.Size(), segment.IsFull())
```

**解决方案:**
- 当 `CurrentSegment().IsFull() == true` 时调用 `manager.RotateSegment()`
- 如果必要增加 MaxSegmentSize
- 检查对齐填充逻辑

#### 问题: "ErrInvalidContinuation"

**症状:** 多页记录读取失败并显示 ErrInvalidContinuation。

**原因:**
1. 续页魔数损坏 (0x434F4E54 != 观察值)
2. 文件在追加时截断 (崩溃时)
3. 磁盘上段文件损坏

**调试步骤:**

```go
record, err := segment.Read(offset)
if err != nil {
    fmt.Printf("Read failed at offset %d: %v\n", offset, err)
    // 检查文件大小和头:
    fmt.Printf("File size: %d\n", segment.Size())
    fmt.Printf("Header: recordCount=%d, nextOffset=%d\n",
        segment.header.RecordCount, segment.header.NextFreeOffset)
}
```

**解决方案:**
- 执行恢复扫描以识别截断记录
- 删除损坏段并从副本/备份重建
- 启用写批处理以减少崩溃窗口

#### 问题: "ErrRecordTruncated"

**症状:** 读取失败，因为记录声称大小超过文件末尾。

**原因:**
1. 段文件截断 (崩溃或磁盘故障)
2. 头过期 (nextOffset 未持久化)
3. 网络存储在写入中断开连接

**解决方案:**
- 检查文件完整性: `ls -l data.*.seg`
- 验证头: `segment.header.NextFreeOffset <= file_size`
- 使用恢复模式仅扫描有效记录

#### 问题: "扫描返回重复记录"

**症状:** ReverseScanner 返回相同记录两次。

**原因:** 续页偏移追踪中的 bug (如果库代码未改变则不太可能)。

**调试:**

```go
seen := map[uint32]bool{}
scanner := NewReverseScanner(segment)
for {
    record, offset, _ := scanner.Next()
    if seen[offset] {
        fmt.Printf("Duplicate offset: %d\n", offset)
    }
    seen[offset] = true
}
```

**解决方案:** 提报问题；更新到最新补丁。

### 调试函数参考

```go
// 验证段文件结构
segment.Validate() error

// 转储头信息
segment.DumpHeader() string

// 扫描并报告所有记录 (带标志)
ScanAllWithFlags(segment, includeDeleted bool) ([]Record, error)

// 计算有效 vs 已删除记录
CountRecords(segment, includeDeleted bool) (uint32, error)

// 查找记录边界问题
FindRecordBoundaries(segment) ([]uint32, error)  // 所有记录的偏移量
```

### 启用调试日志

设置环境变量以启用详细日志：

```bash
export STORAGE_DEBUG=1
export STORAGE_LOG_LEVEL=DEBUG

# 然后运行应用
./app
```

**记录事件:**
- 段轮转
- 多页记录写入
- 续页读取
- Fsync 操作
- 锁获取 (RWMutex 争用)

---

## API 快速参考

### 顶层 Store 接口

```go
// 创建并初始化存储
store, err := storage.NewStore()
err = store.Open(path)

// 写入事件 (返回用于索引的位置)
location, err := store.WriteEvent(event)

// 按位置读取事件
event, err := store.ReadEvent(location)

// 标记为已删除/已替换
err = store.UpdateEventFlags(location, storage.FlagDeleted)

// 状态同步到磁盘
err = store.Flush()

// 清理
err = store.Close()
```

### SegmentManager 方法

```go
manager := storage.NewSegmentManager()
err := manager.Open(path)

// 获取当前可写段
segment, err := manager.CurrentSegment()

// 轮转到新段
oldSegment, err := manager.RotateSegment()

// 访问历史段
segment, err := manager.GetSegment(segmentID)

// 列出所有段 ID
ids, err := manager.ListSegments()

// 删除一个段
err := manager.DeleteSegment(segmentID)

// 持久化所有状态
err := manager.Flush()

// 清理
err := manager.Close()
```

### Segment 方法

```go
// 追加单条记录
offset, err := segment.Append(record)

// 批量追加 (原子)
offsets, err := segment.AppendBatch(records)

// 读取指定偏移处的记录
record, err := segment.Read(offset)

// 检查容量
isFull := segment.IsFull()

// 获取当前大小 (字节)
size := segment.Size()

// 更新标志 (删除/替换标记)
err := segment.UpdateEventFlags(offset, flags)

// 同步头到磁盘
err := segment.Write()

// 清理
err := segment.Close()
```

### Scanner 方法

```go
// 前向扫描器
scanner := storage.NewScanner(segment)
defer scanner.Close()

for {
    record, offset, err := scanner.Next()
    if err == io.EOF {
        break
    }
    // 处理记录
}

// 定位到偏移量
scanner.Seek(offset)

// 重置到开始
scanner.Reset()

// 反向扫描器
revScanner := storage.NewReverseScanner(segment)
for {
    record, offset, err := revScanner.Next()
    if err == io.EOF {
        break
    }
    // 处理记录 (反向顺序)
}
```

### 序列化器方法

```go
serializer := storage.NewTLVSerializer()

// 序列化事件到记录
record, err := serializer.Serialize(event)

// 反序列化记录到事件
event, err := serializer.Deserialize(record)

// 序列化前估计大小
sizeHint := serializer.SizeHint(event)
```

### 关键常数和限制

```go
const (
    MaxRecordSize     = 100 * 1024 * 1024  // 100 MB
    MaxTagCount       = math.MaxUint16      // 65,535
    MaxTagValueLen    = math.MaxUint16      // 65,535 字节
    MaxContentLen     = math.MaxUint32      // ~4 GB

    DefaultPageSize   = 4096                // 4 KB
    DefaultMaxSegSize = 1 * 1024 * 1024 * 1024  // 1 GB

    FlagDeleted   = 0x01
    FlagReplaced  = 0x02
    FlagContinued = 0x80
)
```

### 错误类型

```go
// 常见错误
ErrSegmentNotFound
ErrSegmentFull
ErrSegmentClosed
ErrRecordTooLarge
ErrRecordTruncated
ErrInvalidRecord
ErrInvalidContinuation
ErrOffsetOutOfBounds
ErrInvalidMagic
ErrVersionMismatch
```

---

## 结论

`storage` 包提供了一个强大、可追加的持久存储，针对 Nostr 事件工作负载优化。其关键设计原则—仅追加写、页面对齐、基于位置的寻址和透明多页处理—结合提供：

- **崩溃安全:** 状态在段头中持久化；部分写可恢复
- **并发:** RWMutex 支持无锁读，序列化写
- **效率:** 顺序 I/O、最小分配、缓存友好
- **可扩展性:** 基于接口的设计支持替代实现

对于维护者，理解核心工作流 (写、读、扫描、标志更新) 和权衡决策支持自信的调试和优化。API 快速参考和故障排查部分提供常见任务和问题的立即指导。

---

**文档版本:** v1.0 | 生成: 2026-02-27  
**目标代码:** `src/storage/` 包
