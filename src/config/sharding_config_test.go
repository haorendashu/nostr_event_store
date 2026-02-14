package config

import (
	"testing"
)

func TestShardingConfigDefaults(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ShardingConfig.Enabled {
		t.Errorf("Expected Enabled=false, got %v", cfg.ShardingConfig.Enabled)
	}

	if cfg.ShardingConfig.ShardCount != 4 {
		t.Errorf("Expected ShardCount=4, got %d", cfg.ShardingConfig.ShardCount)
	}

	if cfg.ShardingConfig.VirtualNodes != 150 {
		t.Errorf("Expected VirtualNodes=150, got %d", cfg.ShardingConfig.VirtualNodes)
	}

	if cfg.ShardingConfig.HashFunction != "fnv1a" {
		t.Errorf("Expected HashFunction=fnv1a, got %s", cfg.ShardingConfig.HashFunction)
	}

	if cfg.ShardingConfig.DataDir != "./data/shards" {
		t.Errorf("Expected DataDir=./data/shards, got %s", cfg.ShardingConfig.DataDir)
	}

	if cfg.ShardingConfig.MaxConcurrentQueries != 32 {
		t.Errorf("Expected MaxConcurrentQueries=32, got %d", cfg.ShardingConfig.MaxConcurrentQueries)
	}

	if cfg.ShardingConfig.QueryTimeoutSeconds != 30 {
		t.Errorf("Expected QueryTimeoutSeconds=30, got %d", cfg.ShardingConfig.QueryTimeoutSeconds)
	}

	if !cfg.ShardingConfig.EnableDeduplication {
		t.Errorf("Expected EnableDeduplication=true, got %v", cfg.ShardingConfig.EnableDeduplication)
	}
}

func TestShardingConfigSetDefaults(t *testing.T) {
	mgr := NewManager()
	cfg := &Config{
		ShardingConfig: ShardingConfig{
			Enabled: true,
			// Leave other fields unset
		},
	}
	mgr.(*ManagerImpl).config = cfg
	mgr.SetDefaults()

	if cfg.ShardingConfig.ShardCount != 4 {
		t.Errorf("Expected ShardCount=4, got %d", cfg.ShardingConfig.ShardCount)
	}

	if cfg.ShardingConfig.VirtualNodes != 150 {
		t.Errorf("Expected VirtualNodes=150, got %d", cfg.ShardingConfig.VirtualNodes)
	}

	if cfg.ShardingConfig.HashFunction != "fnv1a" {
		t.Errorf("Expected HashFunction=fnv1a, got %s", cfg.ShardingConfig.HashFunction)
	}
}
