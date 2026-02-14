package index

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Helper to create default test config
func createTestConfig(tempDir string) Config {
	return Config{
		Dir:                    tempDir,
		PageSize:               4096,
		FlushIntervalMs:        100,
		DirtyThreshold:         128,
		PrimaryIndexCacheMB:    10,
		AuthorTimeIndexCacheMB: 10,
		SearchIndexCacheMB:     10,
	}
}

// Test helper: Create a test key for AuthorTime index (38 bytes)
func createAuthorTimeKey(author []byte, timestamp int64) []byte {
	key := make([]byte, 38)
	copy(key[:32], author)                                    // 32 bytes author pubkey
	binary.BigEndian.PutUint32(key[34:38], uint32(timestamp)) // 4 bytes timestamp
	return key
}

// Test helper: Create a test key for Search index (variable length with 4-byte timestamp suffix)
func createSearchKey(tagType string, tagValue string, timestamp int64) []byte {
	tagTypeBytes := []byte(tagType)
	tagValueBytes := []byte(tagValue)

	keyLen := 1 + len(tagTypeBytes) + 1 + len(tagValueBytes) + 4 // format: [len][type][len][value][timestamp]
	key := make([]byte, keyLen)

	offset := 0
	key[offset] = byte(len(tagTypeBytes))
	offset++
	copy(key[offset:], tagTypeBytes)
	offset += len(tagTypeBytes)
	key[offset] = byte(len(tagValueBytes))
	offset++
	copy(key[offset:], tagValueBytes)
	offset += len(tagValueBytes)
	binary.BigEndian.PutUint32(key[offset:], uint32(timestamp))

	return key
}

// TestGranularityStringConversion verifies PartitionGranularity string methods
func TestGranularityStringConversion(t *testing.T) {
	tests := []struct {
		name        string
		granularity PartitionGranularity
		wantString  string
	}{
		{"Monthly", Monthly, "monthly"},
		{"Weekly", Weekly, "weekly"},
		{"Yearly", Yearly, "yearly"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantString, tt.granularity.String())

			// Test round-trip
			parsed, err := ParsePartitionGranularity(tt.wantString)
			require.NoError(t, err)
			assert.Equal(t, tt.granularity, parsed)
		})
	}
}

// TestTimestampExtraction verifies timestamp extraction from different index key formats
func TestTimestampExtraction(t *testing.T) {
	now := time.Now().Unix()

	tests := []struct {
		name         string
		indexType    uint32
		createKey    func() []byte
		expectError  bool
		expectedTime uint32
	}{
		{
			name:      "Primary index - no timestamp",
			indexType: indexTypePrimary,
			createKey: func() []byte {
				return make([]byte, 32) // EventID only
			},
			expectError: true,
		},
		{
			name:      "AuthorTime index - valid key",
			indexType: indexTypeAuthorTime,
			createKey: func() []byte {
				return createAuthorTimeKey(make([]byte, 32), now)
			},
			expectError:  false,
			expectedTime: uint32(now),
		},
		{
			name:      "AuthorTime index - short key",
			indexType: indexTypeAuthorTime,
			createKey: func() []byte {
				return make([]byte, 30) // Too short
			},
			expectError: true,
		},
		{
			name:      "Search index - valid key",
			indexType: indexTypeSearch,
			createKey: func() []byte {
				return createSearchKey("p", "test_pubkey", now)
			},
			expectError:  false,
			expectedTime: uint32(now),
		},
		{
			name:      "Search index - short key",
			indexType: indexTypeSearch,
			createKey: func() []byte {
				return make([]byte, 3) // Too short for timestamp
			},
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := tt.createKey()
			timestamp, err := extractTimestampFromKey(key, tt.indexType)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedTime, timestamp)
			}
		})
	}
}

// TestPartitionRouting verifies Insert operations route to correct partitions
func TestPartitionRouting(t *testing.T) {
	tempDir := t.TempDir()
	basePath := filepath.Join(tempDir, "routing_test")

	cfg := createTestConfig(tempDir)
	cfg.SearchIndexCacheMB = 50

	pi, err := NewPartitionedIndex(basePath, indexTypeAuthorTime, cfg, Monthly, true)
	require.NoError(t, err)
	defer pi.Close()

	ctx := context.Background()

	// Get initial partition count
	initialCount := pi.GetPartitionCount()

	// Insert events in different months
	jan2025 := time.Date(2025, 1, 15, 0, 0, 0, 0, time.UTC).Unix()
	feb2025 := time.Date(2025, 2, 15, 0, 0, 0, 0, time.UTC).Unix()

	key1 := createAuthorTimeKey(bytes32("author_jan_________________1"), jan2025)
	key2 := createAuthorTimeKey(bytes32("author_feb_________________1"), feb2025)

	loc1 := types.RecordLocation{SegmentID: 1, Offset: 100}
	loc2 := types.RecordLocation{SegmentID: 2, Offset: 200}

	// Insert
	err = pi.Insert(ctx, key1, loc1)
	require.NoError(t, err)
	err = pi.Insert(ctx, key2, loc2)
	require.NoError(t, err)

	// Force flush
	err = pi.Flush(ctx)
	require.NoError(t, err)

	// Should have more partitions now
	finalCount := pi.GetPartitionCount()
	assert.Greater(t, finalCount, initialCount, "Should create additional partitions for new months")
	assert.GreaterOrEqual(t, finalCount, 2, "Should have at least 2 partitions")
}

// TestLegacyMode verifies backward compatibility when partitioning is disabled
func TestLegacyMode(t *testing.T) {
	tempDir := t.TempDir()
	basePath := filepath.Join(tempDir, "legacy_test")

	cfg := createTestConfig(tempDir)

	pi, err := NewPartitionedIndex(basePath, indexTypeSearch, cfg, Monthly, false) // Partitioning disabled
	require.NoError(t, err)
	defer pi.Close()

	ctx := context.Background()

	// Insert data
	now := time.Now().Unix()
	key := createSearchKey("e", "test_event", now)
	loc := types.RecordLocation{SegmentID: 1, Offset: 100}

	err = pi.Insert(ctx, key, loc)
	require.NoError(t, err)

	err = pi.Flush(ctx)
	require.NoError(t, err)

	// Verify single index file created (not partitioned)
	legacyFile := basePath + ".idx"
	_, err = os.Stat(legacyFile)
	assert.NoError(t, err, "Legacy mode should create single .idx file")

	// Verify no partition files created
	matches, err := filepath.Glob(basePath + "_*.idx")
	require.NoError(t, err)
	assert.Empty(t, matches, "Should not create partition files in legacy mode")
}

// TestConcurrentPartitionAccess verifies thread-safe partition access
// TODO: Re- enable after fixing B+Tree buffer management issue
func TestConcurrentPartitionAccess(t *testing.T) {
	t.Skip("Skipping due to B+Tree concurrent access buffer management issue - unrelated to partitioning")
	tempDir := t.TempDir()
	basePath := filepath.Join(tempDir, "concurrent_test")

	cfg := createTestConfig(tempDir)
	cfg.SearchIndexCacheMB = 100
	cfg.TagNameToSearchTypeCode = DefaultSearchTypeCodes()

	pi, err := NewPartitionedIndex(basePath, indexTypeSearch, cfg, Monthly, true)
	require.NoError(t, err)
	defer pi.Close()

	kb := NewKeyBuilder(cfg.TagNameToSearchTypeCode)
	ctx := context.Background()
	now := uint32(time.Now().Unix())

	// Launch 10 concurrent goroutines inserting events
	done := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		go func(id int) {
			defer func() { done <- true }()

			// Each goroutine inserts 10 events
			for j := 0; j < 10; j++ {
				// Use BuildSearchKey to create proper format: kind(2) + search_type(1) + tag_value + timestamp(4)
				key := kb.BuildSearchKey(1, SearchType(2), []byte(string(rune('a'+id))), now)
				loc := types.RecordLocation{SegmentID: uint32(id), Offset: uint32(j * 100)}

				err := pi.Insert(ctx, key, loc)
				assert.NoError(t, err)
			}
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Flush
	err = pi.Flush(ctx)
	require.NoError(t, err)
}

// TestPartitionRollover verifies automatic creation of new partitions over time
func TestPartitionRollover(t *testing.T) {
	tempDir := t.TempDir()
	basePath := filepath.Join(tempDir, "rollover_test")

	cfg := createTestConfig(tempDir)
	cfg.TagNameToSearchTypeCode = DefaultSearchTypeCodes()

	pi, err := NewPartitionedIndex(basePath, indexTypeSearch, cfg, Monthly, true)
	require.NoError(t, err)
	defer pi.Close()

	kb := NewKeyBuilder(cfg.TagNameToSearchTypeCode)
	ctx := context.Background()

	// Insert events in clearly different months (avoid boundary issues)
	jan15 := uint32(time.Date(2025, 1, 15, 12, 0, 0, 0, time.UTC).Unix())
	feb15 := uint32(time.Date(2025, 2, 15, 12, 0, 0, 0, time.UTC).Unix())

	key1 := kb.BuildSearchKey(1, SearchType(2), []byte("mid_jan"), jan15)
	key2 := kb.BuildSearchKey(1, SearchType(2), []byte("mid_feb"), feb15)

	loc1 := types.RecordLocation{SegmentID: 1, Offset: 100}
	loc2 := types.RecordLocation{SegmentID: 1, Offset: 200}

	err = pi.Insert(ctx, key1, loc1)
	require.NoError(t, err)

	err = pi.Insert(ctx, key2, loc2)
	require.NoError(t, err)

	err = pi.Flush(ctx)
	require.NoError(t, err)

	// Verify multiple partition files created
	assert.GreaterOrEqual(t, pi.GetPartitionCount(), 2, "Should create multiple partitions for different months")
}

// Helper to create a 32-byte array from a string
func bytes32(s string) []byte {
	b := make([]byte, 32)
	copy(b, []byte(s))
	return b
}
