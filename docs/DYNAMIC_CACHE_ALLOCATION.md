# Dynamic Cache Allocation - Quick Start

## Overview

The dynamic cache allocation feature automatically distributes cache memory among indexes based on their size and access patterns. This optimization is part of Phase 1 of the scaling strategy for handling 10M+ events.

## Configuration

### Enabling Dynamic Allocation

Add to your configuration file (JSON or YAML):

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

### Configuration Parameters

- **dynamic_allocation** (bool): Enable/disable dynamic cache allocation
  - Default: `false`
  - When enabled, individual cache size settings are ignored

- **total_cache_mb** (int): Total cache pool to distribute among all indexes
  - Default: `200` MB
  - Recommended: 2000-3000 MB for 10M+ events

- **min_cache_per_index_mb** (int): Minimum guaranteed cache per index
  - Default: `20` MB
  - Ensures each index gets at least this amount

- **reallocation_interval_minutes** (int): How often to recalculate allocation
  - Default: `10` minutes
  - Balances adaptiveness vs overhead

### Static Configuration (Traditional)

If dynamic allocation is disabled, use static settings:

```json
{
  "index": {
    "cache": {
      "dynamic_allocation": false,
      "primary_index_cache_mb": 200,
      "author_time_index_cache_mb": 300,
      "search_index_cache_mb": 1500
    }
  }
}
```

## Allocation Strategy

The allocator uses a two-factor strategy:

1. **Size-based (70%)**: Larger indexes get proportionally more cache
   - SearchIndex typically gets the most (due to tag indexing)
   - Example: If SearchIndex is 5.7GB and others are 200MB each, SearchIndex gets ~70% of cache

2. **Access-based (30%)**: Frequently accessed indexes get additional cache
   - Adapts to query patterns
   - Hot indexes automatically receive more resources

3. **Minimum guarantee**: Each index gets at least `min_cache_per_index_mb`
   - Prevents starvation of less-used indexes

## Monitoring

### Check Current Allocation

The allocator automatically:
- Updates index sizes every reallocation interval
- Tracks access patterns (cache hits)
- Recalculates allocation
- Applies changes without restart

### Example Allocation Output

For a system with 3.2M events (1000万 search index entries):

```
Primary Index:      250 MB  (12.5%)
Author+Time Index:  350 MB  (17.5%)
Search Index:      1400 MB  (70.0%)
Total:             2000 MB
```

## Performance Impact

### Expected Improvements

- **Cache hit rate**: +15-25% (from ~50% to 75%+)
- **Query latency**: -20-30% (especially for tag searches)
- **Memory efficiency**: Better utilization of available RAM

### When to Use

✅ **Use dynamic allocation when:**
- You have 1M+ events with varying tag usage
- Query patterns change over time
- You want automatic optimization

❌ **Use static allocation when:**
- You have predictable, stable query patterns
- Very small datasets (<100K events)
- You need precise control over each index

## Migration from Static to Dynamic

1. **Measure current usage**:
   - Note current cache sizes and hit rates
   - Total should be: `primary_index_cache_mb + author_time_index_cache_mb + search_index_cache_mb`

2. **Set total_cache_mb**:
   - Start with current total + 20-30%
   - Example: If currently 200MB total, set `total_cache_mb: 250`

3. **Set minimum guarantee**:
   - Set `min_cache_per_index_mb` to 10-20% of smallest index's current cache
   - Example: If PrimaryIndex has 50MB, set minimum to 10MB

4. **Enable and monitor**:
   ```json
   "dynamic_allocation": true
   ```
   - Restart the store
   - Monitor for 1-2 reallocation cycles (10-20 minutes)

5. **Gradually increase total cache** based on performance:
   - For 5M events: 1000-1500 MB
   - For 10M events: 2000-3000 MB
   - For 20M+ events: 4000-6000 MB

## Troubleshooting

### SearchIndex gets too much cache

This is expected! SearchIndex grows much faster than other indexes due to tag multiplicity (each event creates 3+ search index entries).

If you want to limit it:
- Disable less-used tags in `enabled_tags` configuration
- Reduce `total_cache_mb` if memory is constrained

### Frequent reallocations cause performance dips

Increase `reallocation_interval_minutes`:
```json
"reallocation_interval_minutes": 30
```

### Indexes fight for cache

Increase `min_cache_per_index_mb` to ensure stability:
```json
"min_cache_per_index_mb": 100
```

## Next Steps

After implementing dynamic cache allocation:

1. **Phase 2**: Time-based index partitioning (for 10M-20M events)
2. **Phase 3**: Horizontal sharding (for 35M+ events)

See `OPTIMIZATION_PLAN.md` for the full roadmap.

## Support

- Configuration validation errors? Check `validator.go`
- Runtime issues? Enable debug logging: `"debug": true`
- Performance questions? Run benchmarks in `src/batchtest/`
