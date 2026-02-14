package index

import (
	"os"
	"path/filepath"
	"testing"
)

// TestValidatePartitionedIndexes tests validation of partitioned indexes
func TestValidatePartitionedIndexes(t *testing.T) {
	tmpDir := t.TempDir()

	// Create partition directories with dummy index files
	primaryDir := filepath.Join(tmpDir, "primary")
	authorTimeDir := filepath.Join(tmpDir, "author_time")
	searchDir := filepath.Join(tmpDir, "search")

	os.MkdirAll(primaryDir, 0755)
	os.MkdirAll(authorTimeDir, 0755)
	os.MkdirAll(searchDir, 0755)

	// Create dummy partition files
	primaryFile := filepath.Join(primaryDir, "primary_2025-01.idx")
	authorTimeFile := filepath.Join(authorTimeDir, "author_time_2025-01.idx")
	searchFile := filepath.Join(searchDir, "search_2025-01.idx")

	// Write minimal data to files
	os.WriteFile(primaryFile, []byte("dummy"), 0644)
	os.WriteFile(authorTimeFile, []byte("dummy"), 0644)
	os.WriteFile(searchFile, []byte("dummy"), 0644)

	// Test validation with partitioning enabled
	cfg := Config{
		EnableTimePartitioning: true,
		PageSize:               4096,
	}

	valid, err := ValidateIndexes(tmpDir, cfg)
	if err != nil {
		t.Fatalf("ValidateIndexes failed: %v", err)
	}

	if !valid {
		t.Error("Expected partitioned indexes to be valid")
	}
}

// TestValidatePartitionedIndexesInvalid tests validation fails when directories are missing
func TestValidatePartitionedIndexesInvalid(t *testing.T) {
	tmpDir := t.TempDir()

	// Don't create partition directories

	// Test validation with partitioning enabled
	cfg := Config{
		EnableTimePartitioning: true,
		PageSize:               4096,
	}

	valid, err := ValidateIndexes(tmpDir, cfg)
	if err != nil {
		t.Fatalf("ValidateIndexes failed: %v", err)
	}

	if valid {
		t.Error("Expected partitioned indexes to be invalid when directories are missing")
	}
}

// TestValidateLegacyIndexes tests validation of legacy (non-partitioned) indexes
func TestValidateLegacyIndexes(t *testing.T) {
	tmpDir := t.TempDir()

	// Test with empty directory (no files) - should be invalid
	cfg := Config{
		EnableTimePartitioning: false,
		PageSize:               4096,
	}

	valid, err := ValidateIndexes(tmpDir, cfg)
	if err != nil {
		t.Fatalf("ValidateIndexes failed: %v", err)
	}

	if valid {
		t.Error("Expected legacy indexes to be invalid when files are missing")
	}
}
