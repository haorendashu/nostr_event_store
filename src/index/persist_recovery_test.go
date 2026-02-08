package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManagerOpenRemovesCorruptIndexes(t *testing.T) {
	tmpDir := t.TempDir()
	indexesDir := filepath.Join(tmpDir, "indexes")
	if err := os.MkdirAll(indexesDir, 0755); err != nil {
		t.Fatalf("Failed to create indexes dir: %v", err)
	}

	// Create a corrupt primary index file (non-empty, invalid header)
	primaryPath := filepath.Join(indexesDir, "primary.idx")
	if err := os.WriteFile(primaryPath, []byte{0x01, 0x02, 0x03}, 0644); err != nil {
		t.Fatalf("Failed to write corrupt primary index: %v", err)
	}

	cfg := Config{
		PageSize:                4096,
		PrimaryIndexCacheMB:     1,
		AuthorTimeIndexCacheMB:  1,
		SearchIndexCacheMB:      1,
		FlushIntervalMs:         50,
		DirtyThreshold:          1,
		TagNameToSearchTypeCode: DefaultSearchTypeCodes(),
	}

	mgr := NewManager()
	if err := mgr.Open(context.Background(), tmpDir, cfg); err != nil {
		t.Fatalf("Open failed with corrupt index present: %v", err)
	}
	defer mgr.Close()

	primaryValid, err := ValidateIndexFile(primaryPath, indexTypePrimary, cfg.PageSize)
	if err != nil {
		t.Fatalf("Primary validation error: %v", err)
	}
	if !primaryValid {
		t.Fatal("Primary index should be valid after open")
	}

	authorTimePath := filepath.Join(indexesDir, "author_time.idx")
	authorTimeValid, err := ValidateIndexFile(authorTimePath, indexTypeAuthorTime, cfg.PageSize)
	if err != nil {
		t.Fatalf("AuthorTime validation error: %v", err)
	}
	if !authorTimeValid {
		t.Fatal("AuthorTime index should be valid after open")
	}

	searchPath := filepath.Join(indexesDir, "search.idx")
	searchValid, err := ValidateIndexFile(searchPath, indexTypeSearch, cfg.PageSize)
	if err != nil {
		t.Fatalf("Search validation error: %v", err)
	}
	if !searchValid {
		t.Fatal("Search index should be valid after open")
	}
}
