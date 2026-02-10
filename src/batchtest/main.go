package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"time"

	"nostr_event_store/src/config"
	"nostr_event_store/src/eventstore"
	"nostr_event_store/src/types"
)

// EventDTO represents the JSON structure of a Nostr event
type EventDTO struct {
	ID        string     `json:"id"`
	Pubkey    string     `json:"pubkey"`
	CreatedAt uint32     `json:"created_at"`
	Kind      uint16     `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// CommandLineFlags holds parsed command-line arguments
type CommandLineFlags struct {
	EventCount     int
	BatchSize      int
	DataDir        string
	VerifyCount    int
	UseSearchIndex bool
	UseAuthorIndex bool
}

// ProgressTracker tracks writing progress and performance metrics
type ProgressTracker struct {
	startTime    time.Time
	lastTime     time.Time
	totalWritten int
	batchCount   int
}

// loadSeedEvents reads and parses the seed events JSON file
func loadSeedEvents(path string) ([]*EventDTO, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("failed to open seed events file: %w", err)
	}
	defer file.Close()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, fmt.Errorf("failed to read seed events file: %w", err)
	}

	var events []*EventDTO

	// Try to parse as array first
	err = json.Unmarshal(data, &events)
	if err != nil {
		// Fallback: try parsing as single event
		var single EventDTO
		err = json.Unmarshal(data, &single)
		if err == nil {
			events = []*EventDTO{&single}
		} else {
			return nil, fmt.Errorf("failed to parse seed events: %w", err)
		}
	}

	if len(events) == 0 {
		return nil, fmt.Errorf("no events found in seed file")
	}

	return events, nil
}

// hexToBytes converts a hex string to a byte array of specified length
func hexToBytes(hexStr string, length int) ([32]byte, error) {
	var result [32]byte
	if len(result) > length {
		result = [32]byte{}
	}

	bytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return result, fmt.Errorf("failed to decode hex string: %w", err)
	}
	if len(bytes) != length {
		return result, fmt.Errorf("hex string has invalid length: expected %d, got %d", length, len(bytes))
	}
	copy(result[:], bytes)
	return result, nil
}

// hexToBytes64 converts a hex string to a 64-byte array
func hexToBytes64(hexStr string) ([64]byte, error) {
	var result [64]byte

	bytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return result, fmt.Errorf("failed to decode hex string: %w", err)
	}
	if len(bytes) != 64 {
		return result, fmt.Errorf("hex string has invalid length: expected 64, got %d", len(bytes))
	}
	copy(result[:], bytes)
	return result, nil
}

// generateEvent creates a unique event based on a seed event template
func generateEvent(seed *EventDTO, index int) (*types.Event, error) {
	event := &types.Event{
		CreatedAt: seed.CreatedAt + uint32(index),
		Kind:      seed.Kind,
		Content:   seed.Content,
		Tags:      seed.Tags,
	}

	// Decode pubkey
	pubkeyBytes, err := hex.DecodeString(seed.Pubkey)
	if err != nil || len(pubkeyBytes) != 32 {
		return nil, fmt.Errorf("invalid pubkey: %w", err)
	}
	copy(event.Pubkey[:], pubkeyBytes)

	// Generate unique ID: hash(pubkey + timestamp + kind + content + index)
	h := sha256.New()
	h.Write(pubkeyBytes)
	h.Write([]byte(fmt.Sprintf("%d", event.CreatedAt)))
	h.Write([]byte(fmt.Sprintf("%d", event.Kind)))
	h.Write([]byte(event.Content))
	h.Write([]byte(fmt.Sprintf("%d", index)))
	idBytes := h.Sum(nil)
	copy(event.ID[:], idBytes)

	// Generate signature: hash(ID + index) for testing purposes
	sigHash := sha256.New()
	sigHash.Write(idBytes)
	sigHash.Write([]byte(fmt.Sprintf("%d", index)))
	sigBytes := sigHash.Sum(nil)
	// Expand to 64 bytes by hashing again
	sigFullHash := sha256.New()
	sigFullHash.Write(sigBytes)
	sigFullHash.Write([]byte(seed.Pubkey))
	sigFullBytes := sigFullHash.Sum(nil)
	copy(event.Sig[:32], sigFullBytes)
	sigFullHash.Write(sigBytes)
	sigFullHash.Write([]byte(event.Content))
	sigFullBytes2 := sigFullHash.Sum(nil)
	copy(event.Sig[32:], sigFullBytes2)

	return event, nil
}

// initStore creates and initializes the event store
func initStore(dir string) (eventstore.EventStore, error) {
	// Ensure directory exists
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create data directory: %w", err)
	}

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(dir, "data")
	cfg.WALConfig.WALDir = filepath.Join(dir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(dir, "indexes")

	// Increase cache sizes for large datasets (especially search index)
	// For 100K events, search index needs ~150MB (37K nodes * 4KB/node)
	cfg.IndexConfig.CacheConfig.PrimaryIndexCacheMB = 100
	cfg.IndexConfig.CacheConfig.AuthorTimeIndexCacheMB = 100
	cfg.IndexConfig.CacheConfig.SearchIndexCacheMB = 200 // Increased from default 100MB

	store := eventstore.New(&eventstore.Options{
		Config: cfg,
	})

	ctx := context.Background()
	if err := store.Open(ctx, dir, true); err != nil {
		return nil, fmt.Errorf("failed to open event store: %w", err)
	}

	return store, nil
}

// writeEventsInBatches writes generated events to the store in batches
func writeEventsInBatches(ctx context.Context, store eventstore.EventStore, seedEvents []*EventDTO, totalCount int, batchSize int) ([]types.RecordLocation, error) {
	var allLocations []types.RecordLocation
	tracker := &ProgressTracker{
		startTime: time.Now(),
		lastTime:  time.Now(),
	}

	numBatches := (totalCount + batchSize - 1) / batchSize

	for batch := 0; batch < numBatches; batch++ {
		batchStart := batch * batchSize
		batchEnd := batchStart + batchSize
		if batchEnd > totalCount {
			batchEnd = totalCount
		}
		actualBatchSize := batchEnd - batchStart

		// Generate events for this batch
		events := make([]*types.Event, actualBatchSize)
		for i := 0; i < actualBatchSize; i++ {
			eventIndex := batchStart + i
			seedEvent := seedEvents[eventIndex%len(seedEvents)]
			event, err := generateEvent(seedEvent, eventIndex)
			if err != nil {
				return nil, fmt.Errorf("failed to generate event %d: %w", eventIndex, err)
			}
			events[i] = event
		}

		// Write batch
		batchStartTime := time.Now()
		locations, err := store.WriteEvents(ctx, events)
		if err != nil {
			return nil, fmt.Errorf("failed to write batch %d: %w", batch, err)
		}
		batchDuration := time.Since(batchStartTime)

		allLocations = append(allLocations, locations...)
		tracker.totalWritten += actualBatchSize
		tracker.batchCount++

		// Display progress
		progressPercent := float64(tracker.totalWritten) * 100 / float64(totalCount)
		eventsPerSec := float64(actualBatchSize) / batchDuration.Seconds()
		totalElapsed := time.Since(tracker.startTime)
		avgEventsPerSec := float64(tracker.totalWritten) / totalElapsed.Seconds()

		fmt.Printf("Progress: %d/%d (%.1f%%) | Batch time: %.2fs | Current rate: %.0f events/s | Avg rate: %.0f events/s\n",
			tracker.totalWritten, totalCount, progressPercent, batchDuration.Seconds(), eventsPerSec, avgEventsPerSec)
	}

	return allLocations, nil
}

// verifyRandomEvents randomly reads some events to verify they were stored correctly
// If useSearchIndex is true and the event has suitable tags, it will also verify search index queries
func verifyRandomEvents(ctx context.Context, store eventstore.EventStore, locations []types.RecordLocation, seedEvents []*EventDTO, totalCount int, verifyCount int, useSearchIndex bool, useAuthorIndex bool) error {
	if verifyCount <= 0 || verifyCount > len(locations) {
		return nil
	}

	fmt.Printf("\nVerifying %d random events (useSearchIndex: %v, useAuthorIndex: %v)...\n", verifyCount, useSearchIndex, useAuthorIndex)
	startTime := time.Now()

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	successCount := 0
	failCount := 0
	searchQueryCount := 0
	searchTotalQueryEventNum := 0
	pubkeyMap := make(map[string]int) // For author index verification
	authorTotalQueryEventNum := 0

	for i := 0; i < verifyCount; i++ {
		// Pick a random event index
		randomIdx := r.Intn(totalCount)
		seedEvent := seedEvents[randomIdx%len(seedEvents)]

		// Regenerate the event to get expected ID
		expectedEvent, err := generateEvent(seedEvent, randomIdx)
		if err != nil {
			fmt.Printf("  Error regenerating event %d for verification: %v\n", randomIdx, err)
			failCount++
			continue
		}
		pubkeyMap[hex.EncodeToString(expectedEvent.Pubkey[:])] = 1

		// Read from store
		storedEvent, err := store.GetEvent(ctx, expectedEvent.ID)
		if err != nil {
			fmt.Printf("  Error reading event %d: %v\n", randomIdx, err)
			failCount++
			continue
		}

		// Verify key fields
		if storedEvent == nil {
			fmt.Printf("  Event %d not found\n", randomIdx)
			failCount++
			continue
		}

		if storedEvent.ID != expectedEvent.ID {
			fmt.Printf("  Event %d: ID mismatch\n", randomIdx)
			failCount++
			continue
		}

		if storedEvent.Pubkey != expectedEvent.Pubkey {
			fmt.Printf("  Event %d: Pubkey mismatch\n", randomIdx)
			failCount++
			continue
		}

		if storedEvent.Kind != expectedEvent.Kind {
			fmt.Printf("  Event %d: Kind mismatch\n", randomIdx)
			failCount++
			continue
		}

		if storedEvent.CreatedAt != expectedEvent.CreatedAt {
			fmt.Printf("  Event %d: CreatedAt mismatch\n", randomIdx)
			failCount++
			continue
		}

		// If useSearchIndex is enabled and event has tags, verify search index query
		// CRITICAL: Use expectedEvent.Tags (original tags before serialization) instead of storedEvent.Tags
		// because serialization truncates tag names to first character only ("emoji" -> "e")
		if useSearchIndex && len(expectedEvent.Tags) > 0 {
			// Collect all indexable tags (including multiple values for same tag name)
			indexableTags := make(map[string][]string)

			for _, tag := range expectedEvent.Tags {
				if len(tag) >= 2 {
					tagName := tag[0]
					tagValue := tag[1]
					// Check if this tag should be indexed (based on default config tags)
					switch tagName {
					case "t", "p", "e", "a", "d", "P", "E", "A", "g", "h", "i", "I", "k", "K":
						indexableTags[tagName] = append(indexableTags[tagName], tagValue)
					}
				}
			}

			// Try all indexable tag values
			foundInSearch := false
			failedTag := ""
			failedValue := ""
			failedResultCount := 0
			failedEventID := ""
			for tagName, tagValues := range indexableTags {
				for _, tagValue := range tagValues {
					// Create a query filter for this tag, including the event kind
					filter := &types.QueryFilter{
						Kinds: []uint16{storedEvent.Kind},
						Tags:  map[string][]string{tagName: {tagValue}},
					}

					// Execute the query
					results, err := store.QueryAll(ctx, filter)
					if err != nil {
						fmt.Printf("  Event %d: Query error for tag %s=%s: %v\n", randomIdx, tagName, tagValue, err)
						failedTag = tagName
						failedValue = tagValue
						failedResultCount = 0
						failedEventID = fmt.Sprintf("%x", expectedEvent.ID)
						continue
					}

					// Verify that our event is in the results
					// fmt.Printf("  Event %d: Search index query for tag %s=%q returned %d results\n", randomIdx, tagName, tagValue, len(results))
					found := false
					searchTotalQueryEventNum += len(results)
					for _, result := range results {
						if result.ID == expectedEvent.ID {
							found = true
							break
						}
					}

					if found {
						foundInSearch = true
						searchQueryCount++
						break
					} else {
						// This tag didn't return our event
						failedTag = tagName
						failedValue = tagValue
						failedResultCount = len(results)
						failedEventID = fmt.Sprintf("%x", expectedEvent.ID[:8])
					}
				}
				if foundInSearch {
					break
				}
			}

			// If no indexable tags found in search index, count as skipped
			if len(indexableTags) > 0 && !foundInSearch && useSearchIndex {
				fmt.Printf("  Event %d (ID %s...): Not found in search index (tried %d tags with kind %d, failed: %s=%q, returned %d results)\n",
					randomIdx, failedEventID, len(indexableTags), storedEvent.Kind, failedTag, failedValue, failedResultCount)
				failCount++ // Count as failure when search index is enabled
				continue
			}
		}

		successCount++
	}

	if useAuthorIndex && len(pubkeyMap) > 0 {
		for pubkey := range pubkeyMap {
			pubkeyBytes, err := hexToBytes(pubkey, 32)
			if err != nil {
				fmt.Println("hex decode pubkey fail")
				continue
			}
			filter := &types.QueryFilter{
				Authors: [][32]byte{pubkeyBytes},
				Limit:   10,
			}

			authorEventResults, err := store.QueryAll(ctx, filter)
			if len(authorEventResults) == 0 || err != nil {
				fmt.Printf("Author index query for pubkey %s returned no results, err: %v\n", pubkey, err)
				continue
			}

			authorTotalQueryEventNum += len(authorEventResults)
		}
	}

	// Calculate verification performance metrics
	verifyDuration := time.Since(startTime)
	readRate := float64(successCount) / verifyDuration.Seconds()

	fmt.Printf("=== Verification Statistics ===\n")
	fmt.Printf("Total verified: %d\n", successCount+failCount)
	fmt.Printf("Successful: %d\n", successCount)
	fmt.Printf("Failed: %d\n", failCount)
	if useSearchIndex {
		fmt.Printf("Search index queries: %d\n", searchQueryCount)
	}
	fmt.Printf("Time taken: %.2fs\n", verifyDuration.Seconds())
	fmt.Printf("Read rate: %.0f events/s\n", readRate)
	fmt.Printf("searchTotalQueryEventNum: %d\n", searchTotalQueryEventNum)
	fmt.Printf("authorTotalQueryEventNum: %d\n", authorTotalQueryEventNum)

	if failCount > 0 {
		return fmt.Errorf("verification failed for %d events", failCount)
	}
	return nil
}

func main() {
	// Parse command-line flags
	flags := &CommandLineFlags{}
	flag.IntVar(&flags.EventCount, "count", 1000000, "Total number of events to generate and write")
	flag.IntVar(&flags.BatchSize, "batch", 10000, "Batch size for writing events")
	flag.StringVar(&flags.DataDir, "dir", "./testdata", "Data directory for event store")
	flag.IntVar(&flags.VerifyCount, "verify", 100, "Number of events to verify after writing (0 to skip)")
	flag.BoolVar(&flags.UseSearchIndex, "search", false, "Use search index for verification (queries by tags if available)")
	flag.BoolVar(&flags.UseAuthorIndex, "useAuthorIndex", false, "Use author-time index for verification (queries by pubkey and time range if available)")
	flag.Parse()

	// Debug modes are archived - they're available in the archive/ directory if needed

	fmt.Printf("=== Nostr Event Store Batch Test ===\n")
	fmt.Printf("Events to generate: %d\n", flags.EventCount)
	fmt.Printf("Batch size: %d\n", flags.BatchSize)
	fmt.Printf("Data directory: %s\n", flags.DataDir)
	fmt.Printf("Verification count: %d\n", flags.VerifyCount)
	fmt.Printf("Use search index: %v\n", flags.UseSearchIndex)
	fmt.Println()

	ctx := context.Background()

	// Step 1: Load seed events
	fmt.Println("Loading seed events...")
	seedPath := filepath.Join("testevents", "seed", "events.json")
	seedEvents, err := loadSeedEvents(seedPath)
	if err != nil {
		fmt.Printf("Error loading seed events: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d seed event(s)\n\n", len(seedEvents))

	// Step 2: Initialize event store
	fmt.Println("Initializing event store...")
	store, err := initStore(flags.DataDir)
	if err != nil {
		fmt.Printf("Error initializing store: %v\n", err)
		os.Exit(1)
	}
	defer func() {
		if err := store.Close(ctx); err != nil {
			fmt.Printf("Error closing store: %v\n", err)
		}
	}()
	fmt.Println("Event store initialized")

	// Step 3: Write events in batches
	fmt.Println("Writing events...")
	startTime := time.Now()
	locations, err := writeEventsInBatches(ctx, store, seedEvents, flags.EventCount, flags.BatchSize)
	if err != nil {
		fmt.Printf("Error writing events: %v\n", err)
		os.Exit(1)
	}

	// Step 4: Flush to ensure persistence
	fmt.Println("\nFlushing to disk...")
	if err := store.Flush(ctx); err != nil {
		fmt.Printf("Error flushing: %v\n", err)
		os.Exit(1)
	}

	// Step 5: Print statistics
	totalDuration := time.Since(startTime)
	overallRate := float64(flags.EventCount) / totalDuration.Seconds()

	fmt.Printf("\n=== Write Statistics ===\n")
	fmt.Printf("Total events written: %d\n", len(locations))
	fmt.Printf("Total time: %.2fs\n", totalDuration.Seconds())
	fmt.Printf("Overall rate: %.0f events/s\n", overallRate)
	fmt.Printf("Store stats: %+v\n", store.Stats())
	fmt.Println()

	// Step 6: Verify if requested
	if flags.VerifyCount > 0 {
		// Flush again before verification to ensure all hot data is persisted
		fmt.Println("\nFlushing again before verification...")
		if err := store.Flush(ctx); err != nil {
			fmt.Printf("Error flushing before verification: %v\n", err)
			os.Exit(1)
		}

		if err := verifyRandomEvents(ctx, store, locations, seedEvents, flags.EventCount, flags.VerifyCount, flags.UseSearchIndex, flags.UseAuthorIndex); err != nil {
			fmt.Printf("Verification error: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Println("\n=== Test Complete ===")
}
