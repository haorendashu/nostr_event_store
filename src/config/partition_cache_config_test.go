package config

import (
	"context"
	"testing"
)

// TestPartitionCacheCoordinatorConfig tests the partition cache coordinator configuration.
func TestPartitionCacheCoordinatorConfig(t *testing.T) {
	tests := []struct {
		name                       string
		configPath                 string
		expectedCoordinatorEnabled bool
		expectedActivePct          int
		expectedRecentPct          int
		expectedActiveCount        int
		expectedRecentCount        int
	}{
		{
			name:                       "Direct Cache (Coordinator Disabled)",
			configPath:                 "../../config.example.partition_direct_cache.json",
			expectedCoordinatorEnabled: false,
			expectedActivePct:          60,
			expectedRecentPct:          30,
			expectedActiveCount:        2,
			expectedRecentCount:        4,
		},
		{
			name:                       "With Coordinator Enabled",
			configPath:                 "../../config.example.partition_with_coordinator.json",
			expectedCoordinatorEnabled: true,
			expectedActivePct:          60,
			expectedRecentPct:          30,
			expectedActiveCount:        2,
			expectedRecentCount:        4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mgr := NewManager()
			err := mgr.Load(context.Background(), tt.configPath)
			if err != nil {
				t.Fatalf("Failed to load config from %s: %v", tt.configPath, err)
			}

			cfg := mgr.Get()
			if cfg == nil {
				t.Fatal("Config is nil")
			}

			// Check partition cache coordinator setting
			if cfg.IndexConfig.EnablePartitionCacheCoordinator != tt.expectedCoordinatorEnabled {
				t.Errorf("Expected EnablePartitionCacheCoordinator=%v, got %v",
					tt.expectedCoordinatorEnabled, cfg.IndexConfig.EnablePartitionCacheCoordinator)
			}

			// Check other partition settings
			if cfg.IndexConfig.PartitionCacheActivePct != tt.expectedActivePct {
				t.Errorf("Expected PartitionCacheActivePct=%d, got %d",
					tt.expectedActivePct, cfg.IndexConfig.PartitionCacheActivePct)
			}

			if cfg.IndexConfig.PartitionCacheRecentPct != tt.expectedRecentPct {
				t.Errorf("Expected PartitionCacheRecentPct=%d, got %d",
					tt.expectedRecentPct, cfg.IndexConfig.PartitionCacheRecentPct)
			}

			if cfg.IndexConfig.PartitionActiveCount != tt.expectedActiveCount {
				t.Errorf("Expected PartitionActiveCount=%d, got %d",
					tt.expectedActiveCount, cfg.IndexConfig.PartitionActiveCount)
			}

			if cfg.IndexConfig.PartitionRecentCount != tt.expectedRecentCount {
				t.Errorf("Expected PartitionRecentCount=%d, got %d",
					tt.expectedRecentCount, cfg.IndexConfig.PartitionRecentCount)
			}

			// Convert to index.Config and verify mapping
			indexCfg := cfg.ToIndexConfig()
			if indexCfg.EnablePartitionCacheCoordinator != tt.expectedCoordinatorEnabled {
				t.Errorf("Expected index.Config.EnablePartitionCacheCoordinator=%v, got %v",
					tt.expectedCoordinatorEnabled, indexCfg.EnablePartitionCacheCoordinator)
			}

			if indexCfg.PartitionCacheActivePct != tt.expectedActivePct {
				t.Errorf("Expected index.Config.PartitionCacheActivePct=%d, got %d",
					tt.expectedActivePct, indexCfg.PartitionCacheActivePct)
			}

			t.Logf("Config loaded successfully: EnablePartitionCacheCoordinator=%v",
				cfg.IndexConfig.EnablePartitionCacheCoordinator)
		})
	}
}

// TestDefaultPartitionCacheCoordinator tests that default config has coordinator enabled.
func TestDefaultPartitionCacheCoordinator(t *testing.T) {
	cfg := DefaultConfig()
	if cfg == nil {
		t.Fatal("Default config is nil")
	}

	// Default should have coordinator enabled
	if !cfg.IndexConfig.EnablePartitionCacheCoordinator {
		t.Error("Expected default EnablePartitionCacheCoordinator=true")
	}

	if cfg.IndexConfig.PartitionCacheActivePct != 60 {
		t.Errorf("Expected default PartitionCacheActivePct=60, got %d", cfg.IndexConfig.PartitionCacheActivePct)
	}

	if cfg.IndexConfig.PartitionCacheRecentPct != 30 {
		t.Errorf("Expected default PartitionCacheRecentPct=30, got %d", cfg.IndexConfig.PartitionCacheRecentPct)
	}

	t.Log("Default config has correct partition cache coordinator settings")
}
