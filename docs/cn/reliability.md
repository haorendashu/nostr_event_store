# Nostr 事件存储：可靠性与持久化

## 概览

本文描述系统如何保证一致性、进行崩溃恢复、管理压缩与持久化。系统使用 WAL、批量 fsync 与周期性压缩，在安全与性能之间平衡。

---

## 写入持久化模型

### 持久化保证

系统通过 WAL + fsync 提供**批次级**持久化保证：

1. **客户端视角**：API 返回成功后，事件：
   - 已写入磁盘 WAL（批量，典型延迟 10–100 ms）
   - 已在内存索引中可查询
   - 能抵御进程崩溃（可从 WAL 恢复）
   - **无法抵御硬件故障**，除非有副本

2. **恢复视角**：重启后，所有已提交 WAL 的事件都会恢复。

### 写入路径与持久化

```
1. Client: Insert Event E
   ↓
2. Store (in-memory):
   - Validate event signature
   - Check for duplicate (primary index lookup)
   - Serialize to record buffer
   
3. WAL (in-memory ring buffer):
   - Append WAL entry to ring buffer (100 MB max)
   - Return ACK to client
   
4. Every T ms or when buffer size exceeds B:
   - (Batched fsync)
   - Fsync WAL to disk (durability point 1)
   - Update index B+Trees (in-memory)
   - Snapshot index nodes to disk
   - Fsync index files (durability point 2)
   - Fsync manifest.json
   - Clear ring buffer
   - Advance checkpoint LSN
```

**配置**：
```json
{
  "batch_interval_ms": 100,
  "batch_size_bytes": 10485760,
  "checkpoint_interval_ms": 300000
}
```

**延迟影响**：
- **即时 ACK**（< 1 ms）：已入内存，可查询
- **持久 ACK**（10–100 ms）：WAL fsync 完成，能恢复
- **复制 ACK**（100 ms–1 sec）：已复制到备节点（可选）

### 故障场景

#### 场景 1：fsync 前崩溃（< 100 ms）

**状态**：事件仅在内存 WAL 环形缓冲。

**恢复**：
- 缓冲丢失，事件无法恢复
- 客户端可能收到超时/断开
- 客户端重试，主索引保证幂等

**缓解**：增大 `batch_interval_ms` 可提高持久性但增加延迟。

#### 场景 2：fsync 后崩溃（> 100 ms）

**状态**：WAL 已落盘，索引尚未落盘。

**恢复**：
- 从上次 checkpoint 回放 WAL
- 重建内存索引
- 事件可查询

**恢复时间**：O(自上次 checkpoint 的 WAL 条数)。

#### 场景 3：磁盘故障

**恢复**：无副本则无法恢复。可选方案：
1. 主从复制
2. 周期备份（S3/冷存）

### Fsync 安全等级

| 等级 | 配置 | 安全性 | 延迟 | 说明 |
|-------|---|---|---|---|
| **高** | `batch_interval_ms=1000, batch_size=1 MB` | ~1 sec | ~1 sec | 关键场景 |
| **中** | `batch_interval_ms=100, batch_size=10 MB` | ~100 ms | ~100 ms | 默认 |
| **低** | `batch_interval_ms=5000, batch_size=50 MB` | ~5 sec | ~5 sec | 追求吞吐 |

---

## 崩溃恢复

### 恢复流程

```
1. Load manifest.json
   - Identify active segments
   - Identify last checkpoint LSN
   - Note compaction state
   
2. Reconstruct indexes:
   - For each index file: deserialize root node
   - Rebuild in-memory B+Tree cache (LRU)
   
3. Replay WAL:
   - Open wal.log
   - Start from entry at (last_checkpoint_lsn + 1)
   - For each entry:
       - Decode op_type (INSERT, REPLACE, DELETE)
       - Decode event record
       - Validate checksum (CRC64)
       - If invalid: log and skip
       - If valid: apply to in-memory indexes and index cache
   - Continue until EOF
   
4. Compaction status:
   - If compaction was in-progress: resume or rollback
   - If incomplete: mark as stale and schedule re-compaction
   
5. Signal "ready to serve queries"
```

**恢复时间**：
- 1M 事件：10–30 秒
- 10M 事件：100–300 秒

### Checkpoint

周期性（例如 5 分钟）生成 checkpoint：

```
1. Write manifest snapshot
2. Update last_checkpoint_lsn
3. Fsync manifest
4. Optionally archive old WAL segments
```

---

## 一致性保证

### 不变式

1. **事件唯一性**：同一 `id` 不重复。
2. **可替换语义**：同一替换键仅保留 `created_at` 最大的事件。
3. **Tag 一致性**：配置启用的 tag 必须写入 `search.idx`。
4. **删除一致性**：逻辑删除事件不应出现在查询结果。

### 原子性（有限）

单条事件插入是原子的：
- 事件与索引更新写入同一 WAL 批次
- 要么全部出现，要么全部不存在

**注意**：多事件事务不保证原子性。

### 隔离性（事件级）

查询具有**快照隔离**：
- 查询看到启动时刻一致的索引视图
- 并发写入不影响当前查询

---

## 压缩与垃圾回收

### 压缩目标

1. 清理 `DELETED` 和 `REPLACED` 记录
2. 回收空间，缩小段文件
3. 改善缓存局部性
4. 移除过期索引指针

### 压缩策略

```
1. Monitor: Every minute, check fragmentation ratio
2. Trigger: If fragmentation > threshold (default 20%), start compaction
3. Compaction process:
   - Select oldest segment(s)
   - Copy active records to new segment
   - Update indexes
   - Atomic rename
   - Schedule deletion of old segment
   - Update manifest
```

**并行**：可并行压缩多个段。

### 碎片率

```
fragmentation = (deleted_events_count + replaced_events_count) / total_events_count
```

阈值建议：
- **< 10%**：无需压缩
- **10–20%**：建议后台压缩
- **> 20%**：应尽快压缩

---

## 索引恢复与一致性

### 从 WAL 重建索引

```
1. Delete corrupted index file
2. Create empty in-memory B+Tree
3. Replay WAL:
   - For each INSERT event:
       - Extract event data
       - For each tag type: add to corresponding index
4. Flush rebuilt index
```

---

## 复制（可选）

### 主从复制

```
Primary writes WAL entry
   ↓
Send to Standby (network)
   ↓
Standby applies to local WAL
   ↓
Standby ACKs (Successful fsync)
   ↓
Primary considers event "replicated durable"
```

**RPO**：约 1 条事件
**RTO**：~35 秒

---

## 一致性验证

### 周期校验

```
1. Checksum validation
2. Index validation
3. Replaceable event validation
4. Tag index validation
```

---

## 运维与维护

### 备份

```bash
1. Stop server
2. tar -czf backup-$(date +%s).tar.gz data/
3. Upload to S3 / NFS
4. Start server
```

### 恢复

```bash
1. Decompress backup
2. Point store to new directory
3. Replay WAL if needed
```

### 旧数据清理

```
1. Scan segments
2. Identify old events
3. Mark as DELETED
4. Trigger compaction
```

---

## 性能影响

| 策略 | 吞吐 | 延迟 | 持久性 | 场景 |
|---|---|---|---|---|
| 每次写 fsync | 1K/s | 1 ms | 最高 | 关键数据 |
| 100 ms 批量 fsync | 100K/s | 50 ms | 高 | 典型 Nostr |
| 5 sec 异步 fsync | 1M/s | 10 ms | 低 | 归档 |

---

## 部署检查清单

```
Safety:
- [ ] batch_interval_ms 100–1000 ms
- [ ] WAL enabled
- [ ] Checkpoints enabled
- [ ] Replication configured

Performance:
- [ ] Index caches sized
- [ ] Segment size 1–10 GB
- [ ] Compaction ratio > 20%
- [ ] Bloom filter enabled

Monitoring:
- [ ] Latency metrics
- [ ] Fragmentation ratio
- [ ] WAL fsync rate
- [ ] Disk space alerts

Maintenance:
- [ ] Backup schedule
- [ ] Retention period
- [ ] Compaction schedule
```

---

## 下一步

- **Go 实现**：存储层、WAL、索引结构
- **测试**：恢复场景、崩溃安全
- **运维**：部署、监控、故障排查
