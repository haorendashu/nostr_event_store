package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"nostr_event_store/src/index"
	"nostr_event_store/src/storage"
)

func TestValidateConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    *Config
		wantError bool
	}{
		{
			name:      "valid config - default config",
			config:    DefaultConfig(),
			wantError: false,
		},
		{
			name: "valid config - custom",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        8192,
					MaxSegmentSize:  2147483648,
					EventBufferSize: 33554432,
				},
				IndexConfig: IndexConfig{
					IndexDir:           "/tmp/indexes",
					InitializationMode: "full",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{
							"e": 2,
							"p": 3,
						},
						EnabledTags: []string{"e", "p"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    100,
						AuthorTimeIndexCacheMB: 100,
						SearchIndexCacheMB:     200,
						EvictionPolicy:         "lfu",
						CacheConcurrency:       32,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "always",
					BatchIntervalMs: 200,
					BatchSizeBytes:  20971520,
					MaxSegmentSize:  2147483648,
				},
				CompactionConfig: CompactionConfig{
					Enabled:                  true,
					FragmentationThreshold:   0.5,
					CompactionIntervalMs:     120000,
					MaxConcurrentCompactions: 4,
					PreserveOldSegments:      true,
				},
			},
			wantError: false,
		},
		{
			name: "invalid page size",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        1024, // Invalid
					MaxSegmentSize:  1073741824,
					EventBufferSize: 16777216,
				},
				IndexConfig: IndexConfig{
					IndexDir: "/tmp/indexes",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{"e": 2},
						EnabledTags:             []string{"e"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    50,
						AuthorTimeIndexCacheMB: 50,
						SearchIndexCacheMB:     100,
						CacheConcurrency:       16,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "batch",
					BatchIntervalMs: 100,
					BatchSizeBytes:  10485760,
					MaxSegmentSize:  1073741824,
				},
				CompactionConfig: CompactionConfig{
					FragmentationThreshold:   0.2,
					CompactionIntervalMs:     60000,
					MaxConcurrentCompactions: 2,
				},
			},
			wantError: true,
		},
		{
			name: "invalid cache size - zero primary",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        4096,
					MaxSegmentSize:  1073741824,
					EventBufferSize: 16777216,
				},
				IndexConfig: IndexConfig{
					IndexDir: "/tmp/indexes",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{"e": 2},
						EnabledTags:             []string{"e"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    0, // Invalid
						AuthorTimeIndexCacheMB: 50,
						SearchIndexCacheMB:     100,
						CacheConcurrency:       16,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "batch",
					BatchIntervalMs: 100,
					BatchSizeBytes:  10485760,
					MaxSegmentSize:  1073741824,
				},
				CompactionConfig: CompactionConfig{
					FragmentationThreshold:   0.2,
					CompactionIntervalMs:     60000,
					MaxConcurrentCompactions: 2,
				},
			},
			wantError: true,
		},
		{
			name: "invalid sync mode",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        4096,
					MaxSegmentSize:  1073741824,
					EventBufferSize: 16777216,
				},
				IndexConfig: IndexConfig{
					IndexDir: "/tmp/indexes",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{"e": 2},
						EnabledTags:             []string{"e"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    50,
						AuthorTimeIndexCacheMB: 50,
						SearchIndexCacheMB:     100,
						CacheConcurrency:       16,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "invalid", // Invalid
					BatchIntervalMs: 100,
					BatchSizeBytes:  10485760,
					MaxSegmentSize:  1073741824,
				},
				CompactionConfig: CompactionConfig{
					FragmentationThreshold:   0.2,
					CompactionIntervalMs:     60000,
					MaxConcurrentCompactions: 2,
				},
			},
			wantError: true,
		},
		{
			name: "invalid fragmentation threshold - too low",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        4096,
					MaxSegmentSize:  1073741824,
					EventBufferSize: 16777216,
				},
				IndexConfig: IndexConfig{
					IndexDir: "/tmp/indexes",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{"e": 2},
						EnabledTags:             []string{"e"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    50,
						AuthorTimeIndexCacheMB: 50,
						SearchIndexCacheMB:     100,
						CacheConcurrency:       16,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "batch",
					BatchIntervalMs: 100,
					BatchSizeBytes:  10485760,
					MaxSegmentSize:  1073741824,
				},
				CompactionConfig: CompactionConfig{
					FragmentationThreshold:   -0.1, // Invalid
					CompactionIntervalMs:     60000,
					MaxConcurrentCompactions: 2,
				},
			},
			wantError: true,
		},
		{
			name: "invalid fragmentation threshold - too high",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        4096,
					MaxSegmentSize:  1073741824,
					EventBufferSize: 16777216,
				},
				IndexConfig: IndexConfig{
					IndexDir: "/tmp/indexes",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{"e": 2},
						EnabledTags:             []string{"e"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    50,
						AuthorTimeIndexCacheMB: 50,
						SearchIndexCacheMB:     100,
						CacheConcurrency:       16,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "batch",
					BatchIntervalMs: 100,
					BatchSizeBytes:  10485760,
					MaxSegmentSize:  1073741824,
				},
				CompactionConfig: CompactionConfig{
					FragmentationThreshold:   1.5, // Invalid
					CompactionIntervalMs:     60000,
					MaxConcurrentCompactions: 2,
				},
			},
			wantError: true,
		},
		{
			name: "reserved search type - TIME",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        4096,
					MaxSegmentSize:  1073741824,
					EventBufferSize: 16777216,
				},
				IndexConfig: IndexConfig{
					IndexDir: "/tmp/indexes",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{
							"custom": index.SearchTypeTime, // Reserved
						},
						EnabledTags: []string{"custom"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    50,
						AuthorTimeIndexCacheMB: 50,
						SearchIndexCacheMB:     100,
						CacheConcurrency:       16,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "batch",
					BatchIntervalMs: 100,
					BatchSizeBytes:  10485760,
					MaxSegmentSize:  1073741824,
				},
				CompactionConfig: CompactionConfig{
					FragmentationThreshold:   0.2,
					CompactionIntervalMs:     60000,
					MaxConcurrentCompactions: 2,
				},
			},
			wantError: true,
		},
		{
			name: "valid page sizes - 16384",
			config: &Config{
				StorageConfig: StorageConfig{
					DataDir:         "/tmp/data",
					PageSize:        16384, // Valid
					MaxSegmentSize:  1073741824,
					EventBufferSize: 16777216,
				},
				IndexConfig: IndexConfig{
					IndexDir:           "/tmp/indexes",
					InitializationMode: "standard",
					SearchTypeMapConfig: SearchTypeMapConfig{
						TagNameToSearchTypeCode: map[string]index.SearchType{"e": 2},
						EnabledTags:             []string{"e"},
					},
					CacheConfig: CacheConfig{
						PrimaryIndexCacheMB:    50,
						AuthorTimeIndexCacheMB: 50,
						SearchIndexCacheMB:     100,
						EvictionPolicy:         "lru",
						CacheConcurrency:       16,
					},
				},
				WALConfig: WALConfig{
					WALDir:          "/tmp/wal",
					SyncMode:        "never",
					BatchIntervalMs: 100,
					BatchSizeBytes:  10485760,
					MaxSegmentSize:  1073741824,
				},
				CompactionConfig: CompactionConfig{
					FragmentationThreshold:   0.2,
					CompactionIntervalMs:     60000,
					MaxConcurrentCompactions: 2,
				},
			},
			wantError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfig(tt.config)
			if (err != nil) != tt.wantError {
				t.Errorf("ValidateConfig() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestSetDefaults(t *testing.T) {
	mgr := NewManager()
	cfg := mgr.Get()

	// Check storage defaults
	if cfg.StorageConfig.DataDir == "" {
		t.Error("StorageConfig.DataDir should have a default value")
	}
	if cfg.StorageConfig.PageSize != 4096 {
		t.Errorf("StorageConfig.PageSize = %d, want 4096", cfg.StorageConfig.PageSize)
	}
	if cfg.StorageConfig.MaxSegmentSize <= 0 {
		t.Errorf("StorageConfig.MaxSegmentSize = %d, should be > 0", cfg.StorageConfig.MaxSegmentSize)
	}

	// Check WAL defaults
	if cfg.WALConfig.WALDir == "" {
		t.Error("WALConfig.WALDir should have a default value")
	}
	if cfg.WALConfig.MaxSegmentSize <= 0  {
		t.Errorf("WALConfig.MaxSegmentSize = %d, should be > 0", cfg.WALConfig.MaxSegmentSize)
	}
	if cfg.WALConfig.SyncMode != "batch" {
		t.Errorf("WALConfig.SyncMode = %s, want batch", cfg.WALConfig.SyncMode)
	}

	// Check cache defaults
	if cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB <= 0 {
		t.Errorf("CacheConfig.PrimaryIndexCacheMB = %d, should be > 0", cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB)
	}
	if cfg.IndexConfig.CacheConfig.EvictionPolicy != "lru" {
		t.Errorf("CacheConfig.EvictionPolicy = %s, want lru", cfg.IndexConfig.CacheConfig.EvictionPolicy)
	}

	// Check index defaults
	if cfg.IndexConfig.IndexDir == "" {
		t.Error("IndexConfig.IndexDir should have a default value")
	}
	if len(cfg.IndexConfig.SearchTypeMapConfig.TagNameToSearchTypeCode) == 0 {
		t.Error("IndexConfig.SearchTypeMapConfig should have default tags")
	}

	// Check compaction defaults
	if !cfg.CompactionConfig.Enabled {
		t.Error("CompactionConfig.Enabled should be true by default")
	}
	if cfg.CompactionConfig.FragmentationThreshold <= 0 || cfg.CompactionConfig.FragmentationThreshold >= 1 {
		t.Errorf("CompactionConfig.FragmentationThreshold = %f, should be between 0 and 1", cfg.CompactionConfig.FragmentationThreshold)
	}
}

func TestLoadAndSave(t *testing.T) {
	// Create temp directory for test files
	tempDir := t.TempDir()
	ctx := context.Background()

	tests := []struct {
		name      string
		filename  string
		content   string
		wantError bool
	}{
		{
			name:     "valid JSON",
			filename: "config.json",
			content: `{
				"storage": {
					"data_dir": "/tmp/test",
					"page_size": 4096,
					"max_segment_size": 1073741824,
					"event_buffer_size": 16777216
				},
				"index": {
					"index_dir": "/tmp/indexes",
					"initialization_mode": "standard",
					"search_type_map": {
						"tag_name_to_search_type_code": {
							"e": 2,
							"p": 3
						},
						"enabled_tags": ["e", "p"]
					},
					"cache": {
						"primary_index_cache_mb": 50,
						"author_time_index_cache_mb": 50,
						"search_index_cache_mb": 100,
						"eviction_policy": "lru",
						"cache_concurrency": 16
					}
				},
				"wal": {
					"wal_dir": "/tmp/wal",
					"sync_mode": "batch",
					"batch_interval_ms": 100,
					"batch_size_bytes": 10485760,
					"max_segment_size": 1073741824
				},
				"compaction": {
					"enabled": true,
					"fragmentation_threshold": 0.2,
					"compaction_interval_ms": 60000,
					"max_concurrent_compactions": 2
				}
			}`,
			wantError: false,
		},
		{
			name:     "valid YAML",
			filename: "config.yaml",
			content: `
storage:
  data_dir: /tmp/test
  page_size: 8192
  max_segment_size: 1073741824
  event_buffer_size: 16777216
index:
  index_dir: /tmp/indexes
  initialization_mode: performance
  search_type_map:
    tag_name_to_search_type_code:
      e: 2
      p: 3
    enabled_tags:
      - e
      - p
  cache:
    primary_index_cache_mb: 100
    author_time_index_cache_mb: 100
    search_index_cache_mb: 200
    eviction_policy: lfu
    cache_concurrency: 32
wal:
  wal_dir: /tmp/wal
  sync_mode: always
  batch_interval_ms: 50
  batch_size_bytes: 5242880
  max_segment_size: 1073741824
compaction:
  enabled: false
  fragmentation_threshold: 0.3
  compaction_interval_ms: 120000
  max_concurrent_compactions: 4
`,
			wantError: false,
		},
		{
			name:     "valid YML",
			filename: "config.yml",
			content: `
storage:
  data_dir: /tmp/test2
  page_size: 16384
  max_segment_size: 2147483648
  event_buffer_size: 33554432
index:
  index_dir: /tmp/indexes2
  search_type_map:
    tag_name_to_search_type_code:
      e: 2
    enabled_tags:
      - e
  cache:
    primary_index_cache_mb: 25
    author_time_index_cache_mb: 25
    search_index_cache_mb: 50
    cache_concurrency: 8
wal:
  wal_dir: /tmp/wal2
  sync_mode: never
  batch_interval_ms: 200
  batch_size_bytes: 20971520
  max_segment_size: 2147483648
compaction:
  fragmentation_threshold: 0.4
  compaction_interval_ms: 180000
  max_concurrent_compactions: 1
`,
			wantError: false,
		},
		{
			name:      "invalid JSON",
			filename:  "bad.json",
			content:   `{invalid json`,
			wantError: true,
		},
		{
			name:      "invalid YAML",
			filename:  "bad.yaml",
			content:   ":\ninvalid: : yaml:",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Write test file
			filePath := filepath.Join(tempDir, tt.filename)
			if err := os.WriteFile(filePath, []byte(tt.content), 0644); err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			// Load config
			mgr := NewManager()
			err := mgr.Load(ctx, filePath)
			if (err != nil) != tt.wantError {
				t.Errorf("Load() error = %v, wantError %v", err, tt.wantError)
				return
			}

			// If load succeeded, verify some values
			if !tt.wantError {
				cfg := mgr.Get()
				if cfg.StorageConfig.DataDir == "" {
					t.Error("StorageConfig.DataDir should not be empty after load")
				}
				if cfg.IndexConfig.IndexDir == "" {
					t.Error("IndexConfig.IndexDir should not be empty after load")
				}
			}
		})
	}
}

func TestSave(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "output.json")
	ctx := context.Background()

	mgr := NewManager()
	cfg := mgr.Get()
	cfg.StorageConfig.DataDir = "/test/save"
	cfg.StorageConfig.PageSize = 8192
	cfg.WALConfig.SyncMode = "always"
	cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB = 777

	// Save config
	if err := mgr.Save(ctx, filePath); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	// Load it back
	mgr2 := NewManager()
	if err := mgr2.Load(ctx, filePath); err != nil {
		t.Fatalf("Load() after Save() error = %v", err)
	}

	// Verify values
	cfg2 := mgr2.Get()
	if cfg2.StorageConfig.DataDir != "/test/save" {
		t.Errorf("StorageConfig.DataDir = %s, want /test/save", cfg2.StorageConfig.DataDir)
	}
	if cfg2.StorageConfig.PageSize != 8192 {
		t.Errorf("StorageConfig.PageSize = %d, want 8192", cfg2.StorageConfig.PageSize)
	}
	if cfg2.WALConfig.SyncMode != "always" {
		t.Errorf("WALConfig.SyncMode = %s, want always", cfg2.WALConfig.SyncMode)
	}
	if cfg2.IndexConfig.CacheConfig.PrimaryIndexCacheMB != 777 {
		t.Errorf("CacheConfig.PrimaryIndexCacheMB = %d, want 777", cfg2.IndexConfig.CacheConfig.PrimaryIndexCacheMB)
	}
}

func TestLoadNonExistentFile(t *testing.T) {
	mgr := NewManager()
	ctx := context.Background()
	err := mgr.Load(ctx, "/nonexistent/path/config.json")
	if err == nil {
		t.Error("Load() should error on non-existent file")
	}
}

func TestLoadUnsupportedExtension(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "config.txt")
	ctx := context.Background()
	
	if err := os.WriteFile(filePath, []byte("some content"), 0644); err != nil {
		t.Fatalf("Failed to write test file: %v", err)
	}

	mgr := NewManager()
	err := mgr.Load(ctx, filePath)
	if err == nil {
		t.Error("Load() should error on unsupported file extension")
	}
}

func TestLoadFromEnv(t *testing.T) {
	// Save original env vars
	originalEnv := make(map[string]string)
	testVars := []string{
		"NOSTR_STORE_DATA_DIR",
		"NOSTR_STORE_PAGE_SIZE",
		"NOSTR_STORE_WAL_SYNC_MODE",
		"NOSTR_STORE_PRIMARY_INDEX_CACHE_MB",
		"NOSTR_STORE_COMPACTION_ENABLED",
	}
	for _, v := range testVars {
		originalEnv[v] = os.Getenv(v)
	}
	
	// Cleanup function
	defer func() {
		for k, v := range originalEnv {
			if v == "" {
				os.Unsetenv(k)
			} else {
				os.Setenv(k, v)
			}
		}
	}()

	// Set test environment variables
	os.Setenv("NOSTR_STORE_DATA_DIR", "/env/data")
	os.Setenv("NOSTR_STORE_PAGE_SIZE", "8192")
	os.Setenv("NOSTR_STORE_WAL_SYNC_MODE", "always")
	os.Setenv("NOSTR_STORE_PRIMARY_INDEX_CACHE_MB", "999")
	os.Setenv("NOSTR_STORE_COMPACTION_ENABLED", "false")

	mgr := NewManager()
	ctx := context.Background()
	if err := mgr.LoadFromEnv(ctx); err != nil {
		t.Fatalf("LoadFromEnv() error = %v", err)
	}

	cfg := mgr.Get()

	// Verify values from environment
	if cfg.StorageConfig.DataDir != "/env/data" {
		t.Errorf("StorageConfig.DataDir = %s, want /env/data", cfg.StorageConfig.DataDir)
	}
	if cfg.StorageConfig.PageSize != 8192 {
		t.Errorf("StorageConfig.PageSize = %d, want 8192", cfg.StorageConfig.PageSize)
	}
	if cfg.WALConfig.SyncMode != "always" {
		t.Errorf("WALConfig.SyncMode = %s, want always", cfg.WALConfig.SyncMode)
	}
	if cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB != 999 {
		t.Errorf("CacheConfig.PrimaryIndexCacheMB = %d, want 999", cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB)
	}
	if cfg.CompactionConfig.Enabled {
		t.Error("CompactionConfig.Enabled should be false from env")
	}
}

func TestUpdate(t *testing.T) {
	mgr := NewManager()
	ctx := context.Background()
	mgr.Get().StorageConfig.PageSize = 4096
	mgr.Get().IndexConfig.CacheConfig.PrimaryIndexCacheMB = 100

	// Partial update
	updates := &Config{
		IndexConfig: IndexConfig{
			CacheConfig: CacheConfig{
				PrimaryIndexCacheMB: 500,
			},
		},
		CompactionConfig: CompactionConfig{
			FragmentationThreshold: 0.5,
		},
	}

	if err := mgr.Update(ctx, updates); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	cfg := mgr.Get()
	
	// Original values should remain
	if cfg.StorageConfig.PageSize != 4096 {
		t.Errorf("StorageConfig.PageSize = %d, want 4096 (should not change)", cfg.StorageConfig.PageSize)
	}
	
	// Updated values
	if cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB != 500 {
		t.Errorf("CacheConfig.PrimaryIndexCacheMB = %d, want 500", cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB)
	}
	if cfg.CompactionConfig.FragmentationThreshold != 0.5 {
		t.Errorf("CompactionConfig.FragmentationThreshold = %f, want 0.5", cfg.CompactionConfig.FragmentationThreshold)
	}
}

func TestToIndexConfig(t *testing.T) {
	cfg := &Config{
		IndexConfig: IndexConfig{
			IndexDir:           "/test/indexes",
			InitializationMode: "standard",
			SearchTypeMapConfig: SearchTypeMapConfig{
				TagNameToSearchTypeCode: map[string]index.SearchType{
					"e": 2,
					"p": 3,
					"t": 4,
				},
				EnabledTags: []string{"e", "p", "t"},
			},
			CacheConfig: CacheConfig{
				PrimaryIndexCacheMB:    123,
				AuthorTimeIndexCacheMB: 456,
				SearchIndexCacheMB:     789,
			},
		},
	}

	idxCfg := cfg.ToIndexConfig()

	if idxCfg.Dir != "/test/indexes" {
		t.Errorf("Dir = %s, want /test/indexes", idxCfg.Dir)
	}
	if idxCfg.PrimaryIndexCacheMB != 123 {
		t.Errorf("PrimaryIndexCacheMB = %d, want 123", idxCfg.PrimaryIndexCacheMB)
	}
	if idxCfg.AuthorTimeIndexCacheMB != 456 {
		t.Errorf("AuthorTimeIndexCacheMB = %d, want 456", idxCfg.AuthorTimeIndexCacheMB)
	}
	if idxCfg.SearchIndexCacheMB != 789 {
		t.Errorf("SearchIndexCacheMB = %d, want 789", idxCfg.SearchIndexCacheMB)
	}
	if len(idxCfg.TagNameToSearchTypeCode) != 3 {
		t.Errorf("TagNameToSearchTypeCode length = %d, want 3", len(idxCfg.TagNameToSearchTypeCode))
	}
	if idxCfg.TagNameToSearchTypeCode["e"] != index.SearchType(2) {
		t.Errorf("TagNameToSearchTypeCode[e] = %d, want 2", idxCfg.TagNameToSearchTypeCode["e"])
	}
	// Should include reserved types
	hasTime := false
	for _, st := range idxCfg.EnabledSearchTypes {
		if st == index.SearchTypeTime {
			hasTime = true
			break
		}
	}
	if !hasTime {
		t.Error("EnabledSearchTypes should include SearchTypeTime")
	}
}

func TestToStoragePageSize(t *testing.T) {
	tests := []struct {
		name     string
		pageSize uint32
		want     storage.PageSize
	}{
		{"4KB", 4096, storage.PageSize4KB},
		{"8KB", 8192, storage.PageSize8KB},
		{"16KB", 16384, storage.PageSize16KB},
		{"invalid", 1024, storage.PageSize4KB}, // Default fallback
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{
				StorageConfig: StorageConfig{
					PageSize: tt.pageSize,
				},
			}
			got := cfg.ToStoragePageSize()
			if got != tt.want {
				t.Errorf("ToStoragePageSize() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	mgr := NewManager()
	
	// Should pass validation with defaults
	if err := mgr.Validate(); err != nil {
		t.Errorf("Validate() with defaults should pass, got error: %v", err)
	}

	// Should fail with invalid config
	mgr.Get().StorageConfig.PageSize = 1024 // Invalid
	if err := mgr.Validate(); err == nil {
		t.Error("Validate() should fail with invalid page size")
	}
}

func TestConfigSerialization(t *testing.T) {
	cfg := &Config{
		StorageConfig: StorageConfig{
			DataDir:         "/test/data",
			PageSize:        8192,
			MaxSegmentSize:  2147483648,
			EventBufferSize: 33554432,
		},
		IndexConfig: IndexConfig{
			IndexDir:           "/test/indexes",
			InitializationMode: "full",
			SearchTypeMapConfig: SearchTypeMapConfig{
				TagNameToSearchTypeCode: map[string]index.SearchType{
					"e": 2,
					"p": 3,
				},
				EnabledTags: []string{"e", "p"},
			},
			CacheConfig: CacheConfig{
				PrimaryIndexCacheMB:    111,
				AuthorTimeIndexCacheMB: 222,
				SearchIndexCacheMB:     333,
				EvictionPolicy:         "lfu",
				CacheConcurrency:       32,
			},
		},
		WALConfig: WALConfig{
			WALDir:          "/test/wal",
			SyncMode:        "always",
			BatchIntervalMs: 150,
			BatchSizeBytes:  15728640,
			MaxSegmentSize:  2147483648,
		},
		CompactionConfig: CompactionConfig{
			Enabled:                  false,
			FragmentationThreshold:   0.35,
			CompactionIntervalMs:     90000,
			MaxConcurrentCompactions: 3,
			PreserveOldSegments:      true,
		},
	}

	// Marshal to JSON
	data, err := json.Marshal(cfg)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}

	// Unmarshal back
	var cfg2 Config
	if err := json.Unmarshal(data, &cfg2); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}

	// Verify roundtrip
	if cfg2.StorageConfig.DataDir != cfg.StorageConfig.DataDir {
		t.Errorf("DataDir = %s, want %s", cfg2.StorageConfig.DataDir, cfg.StorageConfig.DataDir)
	}
	if cfg2.StorageConfig.PageSize != cfg.StorageConfig.PageSize {
		t.Errorf("PageSize = %d, want %d", cfg2.StorageConfig.PageSize, cfg.StorageConfig.PageSize)
	}
	if cfg2.WALConfig.SyncMode != cfg.WALConfig.SyncMode {
		t.Errorf("SyncMode = %s, want %s", cfg2.WALConfig.SyncMode, cfg.WALConfig.SyncMode)
	}
	if cfg2.IndexConfig.CacheConfig.PrimaryIndexCacheMB != cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB {
		t.Errorf("PrimaryIndexCacheMB = %d, want %d", cfg2.IndexConfig.CacheConfig.PrimaryIndexCacheMB, cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB)
	}
	if cfg2.CompactionConfig.Enabled != cfg.CompactionConfig.Enabled {
		t.Errorf("Enabled = %t, want %t", cfg2.CompactionConfig.Enabled, cfg.CompactionConfig.Enabled)
	}
}
