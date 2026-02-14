package index

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ValidateIndexFile checks if an index file is valid and complete
func ValidateIndexFile(path string, indexType uint32, pageSize uint32) (bool, error) {
	// Check if file exists
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("stat index file: %w", err)
	}

	// Empty file needs recovery
	if info.Size() == 0 {
		return false, nil
	}

	// Try to open and validate header
	file, err := openIndexFile(path, indexType, pageSize, false)
	if err != nil {
		return false, err
	}
	defer file.close()

	// Validate magic and basic fields
	if file.header.Magic != indexMagic {
		return false, fmt.Errorf("invalid magic: expected 0x%X, got 0x%X", indexMagic, file.header.Magic)
	}
	if file.header.IndexType != indexType {
		return false, fmt.Errorf("index type mismatch: expected %d, got %d", indexType, file.header.IndexType)
	}
	if file.header.PageSize != pageSize {
		return false, fmt.Errorf("page size mismatch: expected %d, got %d", pageSize, file.header.PageSize)
	}

	// File is valid
	return true, nil
}

// ValidateIndexes checks all index files
// Returns: (allValid, error)
// Does NOT delete files - caller should check allValid and delete if needed
func ValidateIndexes(dir string, cfg Config) (bool, error) {
	indexesDir := dir
	pageSize := cfg.PageSize
	if pageSize == 0 {
		pageSize = 4096
	}

	// If time partitioning is enabled, check for partition directories instead of single files
	if cfg.EnableTimePartitioning {
		return ValidatePartitionedIndexes(indexesDir, cfg)
	}

	// Legacy validation for non-partitioned indexes
	// Check each index file
	primaryPath := filepath.Join(indexesDir, "primary.idx")
	authorTimePath := filepath.Join(indexesDir, "author_time.idx")
	searchPath := filepath.Join(indexesDir, "search.idx")

	primaryValid, err := ValidateIndexFile(primaryPath, indexTypePrimary, pageSize)
	if err != nil {
		fmt.Printf("[index] Warning: Primary index validation error: %v\n", err)
		primaryValid = false
	}

	authorTimeValid, err := ValidateIndexFile(authorTimePath, indexTypeAuthorTime, pageSize)
	if err != nil {
		fmt.Printf("[index] Warning: AuthorTime index validation error: %v\n", err)
		authorTimeValid = false
	}

	searchValid, err := ValidateIndexFile(searchPath, indexTypeSearch, pageSize)
	if err != nil {
		fmt.Printf("[index] Warning: Search index validation error: %v\n", err)
		searchValid = false
	}

	// If all indexes are valid, no recovery needed
	if primaryValid && authorTimeValid && searchValid {
		fmt.Println("[index] All index files validated successfully")
		return true, nil
	}

	// Some indexes are invalid
	fmt.Printf("[index] Indexes validation failed (primary=%v, authorTime=%v, search=%v)\n",
		primaryValid, authorTimeValid, searchValid)

	return false, nil
}

// ValidatePartitionedIndexes checks partitioned index files in the directory
func ValidatePartitionedIndexes(dir string, cfg Config) (bool, error) {
	pageSize := cfg.PageSize
	if pageSize == 0 {
		pageSize = 4096
	}

	// IMPORTANT: Primary index uses legacy mode (single file), not partitioned
	// Check for legacy primary.idx file
	primaryPath := filepath.Join(dir, "primary.idx")
	primaryValid, err := ValidateIndexFile(primaryPath, indexTypePrimary, pageSize)
	if err != nil {
		fmt.Printf("[index] Warning: Primary index validation error: %v\n", err)
		primaryValid = false
	}

	// Author-time and search indexes use partitioned mode
	// Check for partition files: author_time_<timestamp>.idx, search_<timestamp>.idx
	authorTimeValid := validatePartitionFiles(dir, "author_time")
	searchValid := validatePartitionFiles(dir, "search")

	// If all index files exist, indexing is valid
	if primaryValid && authorTimeValid && searchValid {
		fmt.Println("[index] All partitioned index files validated successfully")
		return true, nil
	}

	// Some index files are invalid
	fmt.Printf("[index] Indexes validation failed (primary=%v, authorTime=%v, search=%v)\n",
		primaryValid, authorTimeValid, searchValid)

	return false, nil
}

// validatePartitionFiles checks if partition files exist for the given index name
func validatePartitionFiles(dir, indexName string) bool {
	// Check if the indexes directory exists
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}

	// Look for partition files matching the pattern: <indexName>_*.idx
	// e.g., primary_2026-W07.idx, author_time_2026-02.idx
	prefix := indexName + "_"
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".idx") {
			return true // Found at least one partition file
		}
	}

	return false
}

// DeleteInvalidIndexes deletes corrupted or missing index files
func DeleteInvalidIndexes(dir string, cfg Config) error {
	// If time partitioning is enabled, delete partition directories instead of single files
	if cfg.EnableTimePartitioning {
		return DeleteInvalidPartitionedIndexes(dir)
	}

	// Legacy deletion for non-partitioned indexes
	indexesDir := dir
	pageSize := cfg.PageSize
	if pageSize == 0 {
		pageSize = 4096
	}

	// Check each index file
	primaryPath := filepath.Join(indexesDir, "primary.idx")
	authorTimePath := filepath.Join(indexesDir, "author_time.idx")
	searchPath := filepath.Join(indexesDir, "search.idx")

	primaryValid, _ := ValidateIndexFile(primaryPath, indexTypePrimary, pageSize)
	authorTimeValid, _ := ValidateIndexFile(authorTimePath, indexTypeAuthorTime, pageSize)
	searchValid, _ := ValidateIndexFile(searchPath, indexTypeSearch, pageSize)

	if !primaryValid {
		fmt.Println("[index] Removing invalid primary.idx")
		os.Remove(primaryPath)
	}
	if !authorTimeValid {
		fmt.Println("[index] Removing invalid author_time.idx")
		os.Remove(authorTimePath)
	}
	if !searchValid {
		fmt.Println("[index] Removing invalid search.idx")
		os.Remove(searchPath)
	}

	return nil
}

// DeleteInvalidPartitionedIndexes deletes corrupted or missing partition files
func DeleteInvalidPartitionedIndexes(dir string) error {
	pageSize := uint32(4096)

	// Check legacy primary index (single file)
	primaryPath := filepath.Join(dir, "primary.idx")
	primaryValid, _ := ValidateIndexFile(primaryPath, indexTypePrimary, pageSize)

	// Check partitioned indexes
	authorTimeValid := validatePartitionFiles(dir, "author_time")
	searchValid := validatePartitionFiles(dir, "search")

	// Delete legacy primary.idx if invalid
	if !primaryValid {
		fmt.Println("[index] Removing invalid primary.idx")
		os.Remove(primaryPath)
	}

	// Delete partition files if any partitioned index is invalid
	if !authorTimeValid || !searchValid {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return fmt.Errorf("failed to read index directory: %w", err)
		}

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			// Delete partition files for author_time and search only
			if (strings.HasPrefix(name, "author_time_") ||
				strings.HasPrefix(name, "search_")) &&
				strings.HasSuffix(name, ".idx") {
				filePath := filepath.Join(dir, name)
				fmt.Printf("[index] Removing invalid partition file: %s\n", name)
				if err := os.Remove(filePath); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("failed to remove partition file %s: %w", name, err)
				}
			}
		}
	}

	return nil
}
