package index

import (
	"fmt"
	"os"
	"path/filepath"
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

// DeleteInvalidIndexes deletes corrupted or missing index files
func DeleteInvalidIndexes(dir string, cfg Config) error {
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
