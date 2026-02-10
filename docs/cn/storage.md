# Nostr 事件存储：存储布局与格式

## 概览

本文说明事件如何序列化与落盘。存储采用 B+Tree 风格的段式架构、分页文件、WAL 与周期性压缩。

---

## 页面文件格式

### 段文件结构

每个段文件（如 `data.0`、`data.1`）组织如下：

```
Byte Range       | Content
─────────────────┼──────────────────────────────────
0–4095           | Header Page (Page 0)
4096–8191        | Data/Index Node Page 1
8192–12287       | Data/Index Node Page 2
...              | ...
```

**页面大小**：4096 字节（默认 4 KB，可配置）。

### 头页（Page 0）

```
Offset | Size | Field                | Type   | Notes
───────┼──────┼──────────────────────┼────────┼──────────────────────────
0      | 4    | magic                | uint32 | 0x4E535452 ('NSTR' in ASCII)
4      | 4    | page_size            | uint32 | Typically 4096
8      | 8    | created_at           | uint64 | UNIX timestamp when segment created
16     | 4    | segment_id           | uint32 | Sequential segment number
20     | 4    | num_records          | uint32 | Total events written to this segment
24     | 4    | next_free_offset     | uint32 | Byte offset of next writable position
28     | 4    | version              | uint32 | Schema version (e.g., 1)
32     | 8    | compaction_marker    | uint64 | Last compaction GC timestamp
40     | 4056 | reserved / padding   | —      | Future expansion
```

### 数据页（Page 1+）

数据页存放可变长度事件记录。小记录（< page_size）尽量保持在单页内；**大记录（>= page_size）跨多个连续页面存储**，使用延续机制。

#### 事件记录布局

**单页记录（典型情况，record_len < page_size）**：

```
Offset | Size    | Field          | Type        | Notes
───────┼─────────┼────────────────┼─────────────┼────────────────────────
0      | 4       | record_len     | uint32      | 记录总长度（包含此字段）
4      | 1       | record_flags   | uint8       | CONTINUED=0（单页），DELETED，REPLACED
5      | 32      | id             | [32]byte    | 事件 ID（SHA-256）
37     | 32      | pubkey         | [32]byte    | 作者公钥
69     | 4       | created_at     | uint32      | UNIX 时间戳（秒）
73     | 2       | kind           | uint16      | 事件类型
75     | 4       | tags_len       | uint32      | 序列化 tags 总字节数
79     | tags_len| tags_data      | []byte      | 序列化 tags（TLV 或 JSON）
       | 4       | content_len    | uint32      | content 字节长度
       | content_len | content    | []byte      | 事件内容（UTF-8）
       | 64      | sig            | [64]byte    | Ed25519 签名
       | 1       | reserved       | uint8       | 预留字段

总计: 4 + 1 + 32 + 32 + 4 + 2 + 4 + tags_len + 4 + content_len + 64 + 1 = 148 + tags_len + content_len 字节
```

**多页记录（record_len >= page_size，如长文、大关注列表）**：

**首页**：
```
Offset | Size    | Field          | Type        | Notes
───────┼─────────┼────────────────┼─────────────┼────────────────────────
0      | 4       | record_len     | uint32      | 跨所有页面的总长度
4      | 1       | record_flags   | uint8       | CONTINUED=1（bit 7），DELETED，REPLACED
5      | 2       | continuation_count | uint16  | 后续延续页数量（N）
7      | N bytes | partial_data   | []byte      | 记录数据的第一部分
```

**延续页**（紧随其后的页面）：
```
Offset | Size    | Field          | Type        | Notes
───────┼─────────┼────────────────┼─────────────┼────────────────────────
0      | 4       | magic          | uint32      | 0x434F4E54（'CONT' 标记）
4      | 2       | chunk_len      | uint16      | 此延续页中的字节数
6      | chunk_len | chunk_data   | []byte      | 记录数据的延续部分
```

**Record Flags**：
- Bit 7：`CONTINUED` – 记录跨多页（1=是，0=否）
- Bit 1：`REPLACED` – 已被更新版本替换
- Bit 0：`DELETED` – 逻辑删除（GC 时设置）
- Bit 2–6：保留

### Tags 序列化

Tags 使用 TLV 编码：

```
Offset | Size | Format
───────┼──────┼──────────────────────
0      | 1    | tag_count (uint8, max 255 tags per event)
1      | 1    | tag_0_type (e.g., 'e'=101, 'p'=112, 't'=116, 'd'=100 as ASCII)
2      | 2    | tag_0_len (uint16, big-endian)
4      | N0   | tag_0_data (N0 bytes)
4+N0   | 1    | tag_1_type
7+N0   | 2    | tag_1_len
...
```

**原因**：TLV 更紧凑，且支持扫描特定 tag 类型。

### 示例记录

**示例 1：小事件（单页）**

短文本事件，少量 tag：

```
record_len:        250 字节
record_flags:      0x00（CONTINUED=0，未删除/替换）
id:                [32 bytes] 
pubkey:            [32 bytes]
created_at:        1655000000
kind:              1（短文本笔记）
tags_len:          20 bytes
  count:           2
  tag_0: 'e'       [16 bytes data]
  tag_1: 'p'       [4 bytes data]
content_len:       50 bytes
content:           "Hello, Nostr!" + ... [50 bytes]
sig:               [64 bytes]
reserved:          0x00
───────────────
总计:              ~250 字节（单个 4KB 页面可容纳）
```

**示例 2：大事件（多页，长文）**

长文事件（kind 30023），内容 12KB：

```
--- 页 N（首页）---
record_len:        12450 字节（所有页面总计）
record_flags:      0x80（CONTINUED=1，bit 7 置位）
continuation_count: 2（需要 2 个额外的延续页）
partial_data:      [约 4080 字节的事件数据：id, pubkey, created_at, kind, 部分 tags/content]

--- 页 N+1（延续 1）---
magic:             0x434F4E54（'CONT'）
chunk_len:         4090 字节
chunk_data:        [4090 字节的 tags/content 延续]

--- 页 N+2（延续 2）---
magic:             0x434F4E54（'CONT'）
chunk_len:         4280 字节（最后一块）
chunk_data:        [剩余 content + sig]
───────────────
总计:              约 12450 字节跨 3 页
```

**示例 3：大关注列表（kind 3）**

用户关注 5000+ 个 pubkey：

```
record_len:        160450 字节（5000 个 pubkey × 约 32 字节/tag）
record_flags:      0x80（CONTINUED=1）
continuation_count: 39（约需 40 页总计，存储 160KB）
...
```

---

## 写前日志（WAL）

### WAL 文件结构

WAL 以**追加写段文件**保存，所有写操作先落 WAL 再更新索引。

```
段 0: wal.log
段 N: wal.000001.log, wal.000002.log, ...

[4 B: WAL magic 0x574C414F 'WLAO']
[8 B: WAL version (uint64)]
[8 B: last_checkpoint_lsn (log sequence number)]
[4 B: reserved]

[WAL entry 1]
[WAL entry 2]
...
[WAL entry N]
```

### WAL 条目格式

```
Offset | Size | Field           | Type   | Notes
───────┼──────┼─────────────────┼────────┼──────────────────────
0      | 1    | op_type         | uint8  | 1=INSERT, 2=UPDATE_FLAGS, 3=INDEX_UPDATE, 4=CHECKPOINT
1      | 8    | lsn             | uint64 | 日志序列号
9      | 8    | timestamp       | uint64 | UNIX 秒
17     | 4    | data_len        | uint32 | data 字节数
21     | var  | data            | []byte | op_type 对应的数据
  | 8    | checksum        | uint64 | CRC64（覆盖前面所有字段）
```

**Entry Length**：`1 + 8 + 8 + 4 + data_len + 8` 字节。

### 批量与 fsync

- 写操作先进入内存 buffer
- 每 T ms（默认 100 ms）或 buffer 达到 B（默认 10 MB）时刷盘：
  1. 追加 WAL 条目到当前段
  2. fsync 段文件
  3. 清空 buffer
- 创建 checkpoint 时会更新段头的 `last_checkpoint_lsn`

**恢复**：重启从 `last_checkpoint_lsn` 回放 WAL 段。

---

## 空间管理

### 页内碎片

段内记录只追加、不原地更新，删除为逻辑删除。

### 压缩策略

周期性压缩（如每小时或碎片 > 20%）：

1. 读取活跃记录
2. 写入新段
3. 更新索引
4. 重命名旧段并删除
5. 更新 manifest

**收益**：回收空间、提升局部性、保持索引性能。

---

## manifest.json

记录存储元数据：

```json
{
  "version": 1,
  "created_at": 1655000000,
  "last_checkpoint_lsn": 1024,
  "next_segment_id": 3,
  "active_segments": [
    {
      "id": 0,
      "created_at": 1655000000,
      "size_bytes": 4194304,
      "record_count": 10000,
      "is_compacted": false
    }
  ],
  "last_compaction_time": 1655025000,
  "total_records": 17500,
  "total_deleted": 1200,
  "total_replaced": 350,
  "page_size": 4096,
  "wal_config": {
    "batch_interval_ms": 100,
    "batch_size_bytes": 10485760,
    "checkpoint_interval_ms": 300000
  }
}
```

---

## 段轮转

段达到阈值后轮转：

1. `data.0` 达到 ~1 GB
2. 创建 `data.1`
3. 新写入进入 `data.1`
4. 老段异步压缩

---

## 内存映射 I/O（可选）

```
segment := mmap.Open("data.0")
recordPtr := (*Record)(unsafe.Pointer(uintptr(segment.Data) + offset))
```

---

## 记录扫描

### 正向扫描

扫描段内所有事件（处理单页与多页记录）：

```
offset := 4096  // 从头页之后开始
for offset < segment_size:
    record_len := uint32(segment[offset:offset+4])
    record_flags := uint8(segment[offset+4])
    
    if record_flags & 0x80 != 0:  // CONTINUED bit 置位（多页记录）
        continuation_count := uint16(segment[offset+5:offset+7])
        
        // 分配完整记录缓冲
        full_record := make([]byte, record_len)
        
        // 读取首页块
        first_chunk_len := 4096 - 7  // 首页头部后可用空间
        copy(full_record[0:first_chunk_len], segment[offset+7:offset+4096])
        
        bytes_read := first_chunk_len
        current_offset := offset + 4096
        
        // 读取延续页
        for i := 0; i < continuation_count; i++ {
            magic := uint32(segment[current_offset:current_offset+4])
            if magic != 0x434F4E54:  // 'CONT'
                return Error("无效的延续页")
            
            chunk_len := uint16(segment[current_offset+4:current_offset+6])
            copy(full_record[bytes_read:bytes_read+chunk_len], 
                 segment[current_offset+6:current_offset+6+chunk_len])
            
            bytes_read += chunk_len
            current_offset += 4096  // 下一页
        }
        
        // 处理完整记录
        if record_flags & 0x01 == 0:  // 未 DELETED
            process(full_record)
        }
        
        offset = current_offset  // 跳过延续页
    } else {
        // 单页记录（典型情况）
        record := segment[offset : offset + record_len]
        if record_flags & 0x01 != 0:  // DELETED
            continue  // 跳过已删除
        process(record)
        
        offset += record_len
        // 对齐到下一页（如需要）
        if offset % 4096 != 0:
            offset = ((offset / 4096) + 1) * 4096
    }
}
```

### 反向扫描

```
offset := segment_size - 4096
while offset >= 4096:
    offset -= 4096
```

---

## 校验与完整性

每条记录与 WAL 使用 CRC64 校验。

---

## 配置参数

| 参数 | 默认值 | 范围 | 说明 |
|-----------|---------|-------|-------|
| `page_size` | 4096 | 1024–65536 | 页小开销大，页大碎片多 |
| `segment_size` | 1 GB | 10 MB–10 GB | 大段少文件，小段易压缩 |
| `batch_interval_ms` | 100 | 10–1000 | 越大延迟越高 |
| `batch_size_bytes` | 10 MB | 1–100 MB | 超过则强制 fsync |
| `compaction_ratio` | 0.2 | 0.1–0.5 | 碎片率阈值 |
| `wal_segment_size_bytes` | 1 GB | 10 MB–10 GB | WAL 段大小达到阈值则轮转 |

---

## 示例：写入事件

**示例 1：小事件（单页）**

```
Event: 
  id = 0xabcd... (32 bytes)
  pubkey = 0xfedc... (32 bytes)
  created_at = 1655000000
  kind = 1
  tags = [["p", "0x1234..."], ["t", "bitcoin"]]
  content = "Hello!"
  sig = 0x...

序列化:
  record_len: 4 bytes = 191
  record_flags: 1 byte = 0x00（未延续、未删除）
  id: 32 bytes
  pubkey: 32 bytes
  created_at: 8 bytes
  kind: 4 bytes
  tags_len: 4 bytes
  tags_data: 约 30 bytes（TLV 编码）
  content_len: 4 bytes = 6
  content: 6 bytes
  sig: 64 bytes
  reserved: 1 byte
  ─────────────────
  总计: 约 191 字节

位置: Segment 0, page 1, offset 100
绝对地址: (segment_id=0, offset=4096 + 100)

WAL Entry:
  op_type: 1 (INSERT)
  lsn: 1
  timestamp: 1655000000
  data_len: 191
  data: [191 bytes of record]
  checksum: CRC64(...) = 0x...
```

**示例 2：大事件（多页，长文 kind 30023）**

```
Event:
  id = 0x1234...
  pubkey = 0x5678...
  created_at = 1655000000
  kind = 30023（长文）
  tags = [["d", "my-article"], ["title", "Nostr 的历史"], ...]
  content = "<12KB markdown 文章>..."
  sig = 0x...

序列化:
  record_len: 4 bytes = 12450
  record_flags: 1 byte = 0x80（CONTINUED bit 置位）
  continuation_count: 2 bytes = 2（需要 2 个额外页面）
  
  首页（4096 字节）：
    头部（7 字节）+ partial_data（4089 字节）
  
  延续页 1（4096 字节）：
    magic: 0x434F4E54（'CONT'）
    chunk_len: 4090 字节
    chunk_data: [4090 字节]
  
  延续页 2（4096 字节）：
    magic: 0x434F4E54
    chunk_len: 4264 字节（最后一块）
    chunk_data: [剩余数据 + sig]
  ─────────────────
  总计: 约 12450 字节跨 3 页

位置: Segment 0, pages 10-12
绝对地址: (segment_id=0, offset=40960)

WAL Entry:
  op_type: 1 (INSERT)
  lsn: 2
  timestamp: 1655000000
  data_len: 12450
  data: [12450 bytes]
  checksum: CRC64(...) = 0x...
```

---

## 灾难恢复示例

```
1. 读取 manifest.json
2. 打开 WAL 段（wal.log, wal.000001.log, ...）从 checkpoint 回放
3. 更新内存索引
4. 下一次 fsync 写盘
```

---

## 下一步

- **索引**（`indexes.md`）
- **查询模型**（`query-models.md`）
- **可靠性**（`reliability.md`）
