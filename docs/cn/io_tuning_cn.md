# Nostr Relay I/O 瓶颈诊断与调优指南

**目标读者:** SRE、运维人员与后端开发者  
**最后更新:** 2026年3月3日  
**语言:** 中文

## 目录

1. [概述](#概述)
2. [症状与含义](#症状与含义)
3. [案例解读（真实指标）](#案例解读真实指标)
4. [快速诊断命令](#快速诊断命令)
5. [常见根因模式](#常见根因模式)
6. [推荐优化顺序](#推荐优化顺序)
7. [生产可用配置建议](#生产可用配置建议)
8. [验证清单](#验证清单)
9. [附录：实用阈值](#附录实用阈值)

---

## 概述

本文说明如何识别并缓解 `nostr-relay` 在当前事件存储架构下的 I/O 瓶颈。

在该系统中，即使总写入带宽不高，只要写调用频率高且写入块很小，也会把磁盘服务时间打满，典型表现为：

- 一个或多个 CPU 核出现较高 `%iowait`
- `iostat -x` 中设备 `%util` 接近 100%
- `wkB/s` 不高但 `w/s` 很高
- ext4 设备上 `jbd2` 活跃

要点区分：

- **带宽瓶颈：** `wkB/s` 很高，属于吞吐上限问题
- **IOPS/时延瓶颈：** 小写入很多、吞吐不高，但设备持续忙碌

---

## 症状与含义

### 主机侧典型模式

- `top`：1~2 个 CPU 核 `wa` 很高，其余核心大多空闲
- `load average`：不一定很高（相对总核数）
- `nostr-relay`：`%CPU` 不高但请求延迟上升

含义：工作线程主要阻塞在存储完成上，而不是算力不足。

### 块设备典型模式

在 `iostat -x 1` 中常见：

- `%util` 持续 > 85%（常接近 100%）
- `w/s` 高（每秒数百次）
- `wareq-sz` 小（例如约 4–8KB）
- `wkB/s` 不高（例如仅数 MB/s）

含义：磁盘是被“小写入高频率”打满，而不是被大吞吐压满。

---

## 案例解读（真实指标）

你这次的观测特征：

- CPU `iowait` 约 10–12%
- `sda` `%util` 约 99–100%
- 写入速率约 450–470 次/秒
- `wareq-sz` 约 5.3KB
- `wkB/s` 约 2.4–2.5MB/s

结论：

1. 存储路径是当前主要瓶颈。
2. 这不是吞吐（MB/s）上限问题，而是小写入服务时间/IOPS 问题。
3. 即使缓存很大，也无法消除写路径的同步压力。

---

## 快速诊断命令

建议在 relay 主机执行：

```bash
# 1) 看设备饱和度与时延
iostat -x 1

# 2) 看进程级 I/O 行为（按秒采样）
pidstat -d -p <nostr-relay-pid> 1

# 3) 看进程累计 I/O 计数
cat /proc/<nostr-relay-pid>/io

# 4) 看实时 I/O 占用者
iotop -oPa

# 5) 看虚拟内存与 iowait 趋势
vmstat 1

# 6) 看挂载参数（atime、journal 相关）
mount | grep ' / '
```

注意：`pidstat -d -p <pid>` 若不带采样间隔，常是进程生命周期均值，容易误判。

---

## 常见根因模式

### 1) 小同步写放大

写调用频繁且写入块小，ext4 journaling 和 flush 行为会放大时延成本。

### 2) 写密集场景下关闭 WAL

关闭 WAL 后，直接写更新模式更可能出现非顺序写，不利于磁盘。

### 3) 单盘争用

若系统、relay 数据、索引与日志同在一块 SATA 盘，队列争用会很快显现。

---

## 推荐优化顺序

### 优先级 1：恢复 WAL-first 写路径

- 开启 WAL（`WALConfig.Disabled = false`）
- 生产环境使用 `SyncMode = "batch"`
- 在运行手册中明确崩溃一致性语义

理由：把大量小随机写尽量转为 append 型顺序写。

### 优先级 2：优化存储布局

- 优先迁移到 NVMe
- 硬件允许时将 `wal` 与 `data/indexes` 分盘
- 避免应用重写入与系统盘混用

### 优先级 3：文件系统与回写参数

- 使用 `noatime`
- 业务允许时评估 ext4 `commit=30`
- 结合内存规模谨慎调优 Linux dirty page 阈值

### 优先级 4：应用侧批处理

- 批量索引更新与批量 flush
- 减少每条事件的即时同步点

---

## 生产可用配置建议

以下是写密集 relay 的保守基线：

```go
cfg := config.DefaultConfig()

cfg.WALConfig.Disabled = false
cfg.WALConfig.SyncMode = "batch"

cfg.StorageConfig.DataDir = "/data/nostr/data"
cfg.WALConfig.WALDir = "/data/nostr/wal"
cfg.IndexConfig.IndexDir = "/data/nostr/indexes"

cfg.IndexConfig.EnableTimePartitioning = true
cfg.IndexConfig.PartitionGranularity = "monthly"
```

现有索引缓存大小可先保持不变；缓存主要改善读路径，本次问题以写路径为主。

---

## 验证清单

调优后在真实流量下验证：

- `iostat -x`：`%util` 明显下降（例如从 ~100% 降到 <70–80%）
- `iostat -x`：`w/s` 可仍较高，但 `await` 与队列压力应更稳定
- `top/vmstat`：`%iowait` 下降
- relay 的 p99 写入/发布延迟下降
- 崩溃重启测试无一致性回退

---

## 附录：实用阈值

以下为运维经验阈值（非绝对硬限制）：

- `%util` 持续 >85%：应立即排查
- `%util` 约 100% 且 `wkB/s` 低：大概率是 IOPS/小写瓶颈
- `wareq-sz` 单位数 KB 且 `w/s` 很高：存在写放大风险
- `jbd2` 活跃时间上升：journal 压力可能是关键因素

建议以 5–30 分钟趋势判断，不要只看单次采样。
