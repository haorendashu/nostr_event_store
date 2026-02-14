package index

import (
	"encoding/binary"
	"hash/crc64"
	"os"
	"path/filepath"
	"testing"
)

// createMinimalIndexFile creates a minimal valid index file with proper header
func createMinimalIndexFile(path string, indexType uint32, pageSize uint32) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	// Create header buffer
	buf := make([]byte, pageSize)

	// Write header fields
	binary.BigEndian.PutUint32(buf[0:4], 0x494E4458) // indexMagic
	binary.BigEndian.PutUint32(buf[4:8], indexType)
	binary.BigEndian.PutUint64(buf[8:16], 2)  // indexVersion
	binary.BigEndian.PutUint64(buf[16:24], 0) // RootOffset
	binary.BigEndian.PutUint64(buf[24:32], 0) // NodeCount
	binary.BigEndian.PutUint32(buf[32:36], pageSize)
	binary.BigEndian.PutUint32(buf[36:40], 1) // indexHeaderFormat
	binary.BigEndian.PutUint64(buf[40:48], 0) // EntryCount

	// Calculate and write checksum
	var crc64Table = crc64.MakeTable(crc64.ECMA)
	checksum := crc64.Checksum(buf[:pageSize-8], crc64Table)
	binary.BigEndian.PutUint64(buf[pageSize-8:], checksum)

	// Write the header
	_, err = file.Write(buf)
	return err
}

// TestValidatePartitionedIndexes tests validation of partitioned indexes
func TestValidatePartitionedIndexes(t *testing.T) {
	tmpDir := t.TempDir()

	// Create dummy index files directly in the root directory
	primaryFile := filepath.Join(tmpDir, "primary.idx")
	authorTimeFile := filepath.Join(tmpDir, "author_time_2025-01.idx")
	searchFile := filepath.Join(tmpDir, "search_2025-01.idx")

	// Create a minimal valid primary index file with proper header
	createMinimalIndexFile(primaryFile, indexTypePrimary, 4096)
	// For partition files, just create empty files (validation only checks existence)
	os.WriteFile(authorTimeFile, []byte{}, 0644)
	os.WriteFile(searchFile, []byte{}, 0644)

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
