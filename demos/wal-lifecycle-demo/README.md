# WAL Lifecycle Demo

这个 demo 用来演示你关心的 WAL 运维操作：

1. 开启 WAL 并写入事件
2. 查看 WAL 文件大小
3. 基于 checkpoint 计算“可删除候选 WAL 文件”（默认 dry-run）
4. 可选执行真实删除
5. 重启并校验读取是否正常（验证删除后是否报错）

## 运行

在仓库根目录执行：

```bash
go run ./demos/wal-lifecycle-demo -dir ./demos/wal-lifecycle-demo/demo_data
```

默认行为：
- 开启 WAL
- 写入 300 条事件
- 生成 checkpoint
- 只做 dry-run，不删除真实文件
- 最后重启校验

## 常用参数

- `-events=500`：写入事件数
- `-content-bytes=4096`：每条事件内容大小
- `-segment-size-kb=64`：WAL 段大小（设置小一点更容易触发轮转）
- `-sync-mode=batch|always|never`：WAL 同步策略
- `-apply-delete=true`：对真实 WAL 执行删除
- `-checkpoint=false`：不强制创建 checkpoint，使用最近 checkpoint
- `-skip-write=true`：不写新事件，只检查当前 WAL 状态
- `-keep-data=false`：演练结束后自动清理目录

示例（执行真实删除）：

```bash
go run ./demos/wal-lifecycle-demo -dir ./demos/wal-lifecycle-demo/demo_data -apply-delete=true
```

## 输出解读

- `WAL Stats`：来自 `WAL().Stats()`，包含总大小、LSN 等
- `WAL Files`：每个 `.log` 文件的字节数
- `WAL Validator Summary`：每个文件头和记录校验信息
- `Safe-delete Candidates (Dry-run)`：在“拷贝 WAL 目录”上调用 `DeleteSegmentsBefore(checkpointLSN)` 得到的候选删除文件列表

> 注意：这里的候选是“按当前实现可删”的工程判据，不等同于“无限制地手工删任何 WAL 文件都安全”。

## 为什么这样判定“可删”

当前实现里，删除逻辑由 `DeleteSegmentsBefore(beforeLSN)` 执行，它会删除满足条件的旧段（且不会删当前写入段）。
这个 demo 用 dry-run 模式在副本目录先跑一遍，得到“将被删除的文件名”，从而避免直接改动真实数据。

## 删除后会不会报错？

- 如果删除的是“候选可删段”，通常不会影响正常启动，demo 会在删除后自动重启并验证读取。
- 如果手工删错（例如删当前段或删到仍需回放的段），可能导致恢复不完整或报错。

建议生产上先 `dry-run`，再小批量执行真实删除。
