# 动态缓存分配 - 快速参考

## 概述

动态缓存分配功能会根据索引大小和访问模式自动在各索引间分配缓存内存。这是应对千万级数据的第一阶段优化。

## 配置示例

### 启用动态分配

```json
{
  "index": {
    "cache": {
      "dynamic_allocation": true,
      "total_cache_mb": 2000,
      "min_cache_per_index_mb": 50,
      "reallocation_interval_minutes": 10
    }
  }
}
```

## 核心参数

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `dynamic_allocation` | `false` | 是否启用动态分配 |
| `total_cache_mb` | `200` | 总缓存池大小（MB） |
| `min_cache_per_index_mb` | `20` | 每个索引最小保障（MB） |
| `reallocation_interval_minutes` | `10` | 重新分配间隔（分钟） |

## 分配策略

**70% 按大小** + **30% 按访问频率** + **最小保障**

### 示例（320万事件）

假设：
- Primary 索引：220 MB
- AuthorTime 索引：260 MB  
- Search 索引：5.7 GB（最大）

分配结果（总计 2000 MB）：
```
Primary:      250 MB  (12.5%)
AuthorTime:   350 MB  (17.5%)
Search:      1400 MB  (70.0%)
```

## 推荐配置

### 当前阶段（320万事件）
```json
{
  "dynamic_allocation": true,
  "total_cache_mb": 1000,
  "min_cache_per_index_mb": 30
}
```

### 目标阶段（1000万事件）
```json
{
  "dynamic_allocation": true,
  "total_cache_mb": 2500,
  "min_cache_per_index_mb": 50
}
```

### 远期规划（3500万事件）
```json
{
  "dynamic_allocation": true,
  "total_cache_mb": 6000,
  "min_cache_per_index_mb": 100
}
```

## 预期效果

- **缓存命中率**：提升 15-25%
- **查询延迟**：降低 20-30%（特别是标签查询）
- **内存利用率**：更高效

## 常见问题

### Q: SearchIndex 占用太多缓存？
A: 这是正常的。SearchIndex 因为标签索引会比其他索引大很多（平均每个事件产生 3+ 个索引条目）。

### Q: 如何限制 SearchIndex 的缓存？
A: 两个方法：
1. 减少 `total_cache_mb`
2. 在配置中禁用不常用的标签类型

### Q: 重新分配会影响性能吗？
A: 影响很小。重新分配是渐进式的，不会中断服务。如果担心，可以增大 `reallocation_interval_minutes`。

### Q: 如何监控分配效果？
A: 启用调试日志：
```json
{
  "debug": true
}
```
查看日志中的缓存命中率和分配变化。

## 迁移步骤

从静态配置迁移到动态分配：

1. **记录当前配置**
   ```json
   "primary_index_cache_mb": 50,
   "author_time_index_cache_mb": 50,
   "search_index_cache_mb": 100
   ```
   总计：200 MB

2. **设置动态配置**（增加 30%）
   ```json
   "dynamic_allocation": true,
   "total_cache_mb": 260,
   "min_cache_per_index_mb": 20
   ```

3. **重启并观察** 10-20 分钟

4. **根据效果调整** `total_cache_mb`

## 下一步

完成动态缓存优化后的演进路径：

✅ **阶段一（已完成）**：动态缓存分配  
⏳ **阶段二（规划中）**：时间分区索引（支持 1000万-2000万事件）  
⏳ **阶段三（未来）**：水平分片架构（支持 3500万+ 事件）

详细方案见项目根目录的优化规划文档。
