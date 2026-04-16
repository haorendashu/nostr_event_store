package eventstore

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// makeTestEvent creates a test event with a unique ID derived from the seed.
func makeTestEvent(seed byte, kind uint16, ts uint32) *types.Event {
	var id [32]byte
	id[0] = seed
	id[1] = byte(kind >> 8)
	id[2] = byte(kind)
	id[3] = byte(ts >> 24)
	id[4] = byte(ts >> 16)
	id[5] = byte(ts >> 8)
	id[6] = byte(ts)

	var pubkey [32]byte
	pubkey[0] = seed

	return &types.Event{
		ID:        id,
		Pubkey:    pubkey,
		CreatedAt: ts,
		Kind:      kind,
		Content:   fmt.Sprintf("test event %d", seed),
		Tags:      [][]string{{"e", fmt.Sprintf("ref%d", seed)}, {"p", fmt.Sprintf("pk%d", seed)}},
	}
}

// assertIndexCountsEqual checks that all non-search indexes have the same entry count.
func assertIndexCountsEqual(t *testing.T, store EventStore, msg string) {
	t.Helper()
	impl, ok := store.(*eventStoreImpl)
	if !ok {
		t.Fatalf("store is not *eventStoreImpl")
	}
	stats := impl.indexMgr.AllStats()
	primaryCount := stats["primary"].EntryCount
	authorTimeCount := stats["author_time"].EntryCount
	kindTimeCount := stats["kind_time"].EntryCount

	if authorTimeCount != primaryCount {
		t.Errorf("[%s] author_time count (%d) != primary count (%d), diff=%d",
			msg, authorTimeCount, primaryCount, int64(authorTimeCount)-int64(primaryCount))
	}
	if kindTimeCount != primaryCount {
		t.Errorf("[%s] kind_time count (%d) != primary count (%d), diff=%d",
			msg, kindTimeCount, primaryCount, int64(kindTimeCount)-int64(primaryCount))
	}
}

func openTestStoreForIndexTest(t *testing.T) (EventStore, context.Context) {
	t.Helper()
	tempDir := t.TempDir()
	ctx := context.Background()

	cfg := config.DefaultConfig()
	cfg.StorageConfig.DataDir = filepath.Join(tempDir, "data")
	cfg.WALConfig.WALDir = filepath.Join(tempDir, "wal")
	cfg.IndexConfig.IndexDir = filepath.Join(tempDir, "indexes")

	store := New(&Options{
		Config:       cfg,
		RecoveryMode: "skip",
	})

	if err := store.Open(ctx, tempDir, true); err != nil {
		t.Fatalf("Open() failed: %v", err)
	}

	return store, ctx
}

// TestIndexCountConsistency_SingleWrites verifies that single writes
// produce identical entry counts across all non-search indexes.
func TestIndexCountConsistency_SingleWrites(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())

	// Write 100 unique events
	for i := byte(0); i < 100; i++ {
		event := makeTestEvent(i, 1, now+uint32(i))
		if _, err := store.WriteEvent(ctx, event); err != nil {
			t.Fatalf("WriteEvent(%d) failed: %v", i, err)
		}
	}

	assertIndexCountsEqual(t, store, "after 100 single writes")
}

// TestIndexCountConsistency_BatchWrites verifies batch writes
// produce identical entry counts across all non-search indexes.
func TestIndexCountConsistency_BatchWrites(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())

	// Write 200 unique events in a batch
	events := make([]*types.Event, 200)
	for i := 0; i < 200; i++ {
		events[i] = makeTestEvent(byte(i), uint16(i%5+1), now+uint32(i))
	}

	if _, err := store.WriteEvents(ctx, events); err != nil {
		t.Fatalf("WriteEvents() failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "after batch of 200")
}

// TestIndexCountConsistency_BatchWithIntraDuplicates verifies that
// within-batch duplicate event IDs are correctly deduplicated so that
// all indexes have consistent entry counts.
func TestIndexCountConsistency_BatchWithIntraDuplicates(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())

	e1 := makeTestEvent(1, 1, now)
	e2 := makeTestEvent(2, 1, now+1)
	e3 := makeTestEvent(3, 1, now+2)

	// Batch contains e1 TWICE (within-batch duplicate)
	batch := []*types.Event{e1, e2, e1, e3, e2}

	if _, err := store.WriteEvents(ctx, batch); err != nil {
		t.Fatalf("WriteEvents() failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "after batch with intra-duplicates")

	// Verify exactly 3 unique events were stored
	impl := store.(*eventStoreImpl)
	stats := impl.indexMgr.AllStats()
	if stats["primary"].EntryCount != 3 {
		t.Errorf("Expected 3 primary entries, got %d", stats["primary"].EntryCount)
	}
}

// TestIndexCountConsistency_WriteAndDelete verifies that delete operations
// maintain index count consistency.
func TestIndexCountConsistency_WriteAndDelete(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())

	// Write 10 events
	events := make([]*types.Event, 10)
	for i := 0; i < 10; i++ {
		events[i] = makeTestEvent(byte(i+1), 1, now+uint32(i))
	}
	if _, err := store.WriteEvents(ctx, events); err != nil {
		t.Fatalf("WriteEvents() failed: %v", err)
	}
	assertIndexCountsEqual(t, store, "after writing 10")

	// Delete 5 events
	for i := 0; i < 5; i++ {
		if err := store.DeleteEvent(ctx, events[i].ID); err != nil {
			t.Fatalf("DeleteEvent(%d) failed: %v", i, err)
		}
	}
	assertIndexCountsEqual(t, store, "after deleting 5 of 10")

	// Verify 5 remain
	impl := store.(*eventStoreImpl)
	stats := impl.indexMgr.AllStats()
	if stats["primary"].EntryCount != 5 {
		t.Errorf("Expected 5 primary entries after delete, got %d", stats["primary"].EntryCount)
	}
}

// TestIndexCountConsistency_BatchDelete verifies batch delete maintains consistency.
func TestIndexCountConsistency_BatchDelete(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())

	events := make([]*types.Event, 20)
	for i := 0; i < 20; i++ {
		events[i] = makeTestEvent(byte(i+1), uint16(i%3+1), now+uint32(i))
	}
	if _, err := store.WriteEvents(ctx, events); err != nil {
		t.Fatalf("WriteEvents() failed: %v", err)
	}
	assertIndexCountsEqual(t, store, "after writing 20")

	// Batch delete 10 events
	ids := make([][32]byte, 10)
	for i := 0; i < 10; i++ {
		ids[i] = events[i].ID
	}
	if err := store.DeleteEvents(ctx, ids); err != nil {
		t.Fatalf("DeleteEvents() failed: %v", err)
	}
	assertIndexCountsEqual(t, store, "after batch deleting 10 of 20")
}

// TestIndexCountConsistency_DuplicateTagsInEvent verifies that events with
// duplicate tags don't cause extra entries in the search index.
func TestIndexCountConsistency_DuplicateTagsInEvent(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())

	// Event with duplicate tags
	event := &types.Event{
		ID:        [32]byte{99},
		Pubkey:    [32]byte{1},
		CreatedAt: now,
		Kind:      1,
		Content:   "test with dup tags",
		Tags: [][]string{
			{"p", "pubkey123"},
			{"p", "pubkey123"}, // duplicate!
			{"e", "ref456"},
			{"e", "ref456"}, // duplicate!
			{"e", "ref789"},
		},
	}

	if _, err := store.WriteEvent(ctx, event); err != nil {
		t.Fatalf("WriteEvent() failed: %v", err)
	}

	impl := store.(*eventStoreImpl)
	stats := impl.indexMgr.AllStats()

	// search index should have 3 entries (not 5), since duplicates are now deduped
	if stats["search"].EntryCount != 3 {
		t.Errorf("Expected 3 search entries (deduped), got %d", stats["search"].EntryCount)
	}

	assertIndexCountsEqual(t, store, "after event with dup tags")
}

// TestIndexCountConsistency_MultipleOperations runs a sequence of mixed operations
// and verifies count consistency throughout.
func TestIndexCountConsistency_MultipleOperations(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())

	// Phase 1: Single writes
	for i := byte(0); i < 50; i++ {
		event := makeTestEvent(i, uint16(i%3+1), now+uint32(i))
		if _, err := store.WriteEvent(ctx, event); err != nil {
			t.Fatalf("Phase 1 WriteEvent(%d) failed: %v", i, err)
		}
	}
	assertIndexCountsEqual(t, store, "phase 1: 50 single writes")

	// Phase 2: Batch writes with some duplicates from phase 1
	batchEvents := make([]*types.Event, 30)
	for i := 0; i < 30; i++ {
		// First 10 are duplicates of phase 1 events
		if i < 10 {
			batchEvents[i] = makeTestEvent(byte(i), uint16(i%3+1), now+uint32(i))
		} else {
			batchEvents[i] = makeTestEvent(byte(50+i), uint16(i%3+1), now+uint32(50+i))
		}
	}
	if _, err := store.WriteEvents(ctx, batchEvents); err != nil {
		t.Fatalf("Phase 2 WriteEvents() failed: %v", err)
	}
	assertIndexCountsEqual(t, store, "phase 2: batch with cross-batch duplicates")

	// Phase 3: Delete some events
	for i := byte(0); i < 10; i++ {
		if err := store.DeleteEvent(ctx, makeTestEvent(i, uint16(i%3+1), now+uint32(i)).ID); err != nil {
			t.Fatalf("Phase 3 DeleteEvent(%d) failed: %v", i, err)
		}
	}
	assertIndexCountsEqual(t, store, "phase 3: after 10 deletes")

	// Phase 4: More batch writes
	moreEvents := make([]*types.Event, 40)
	for i := 0; i < 40; i++ {
		moreEvents[i] = makeTestEvent(byte(100+i), uint16(i%4+1), now+uint32(100+i))
	}
	if _, err := store.WriteEvents(ctx, moreEvents); err != nil {
		t.Fatalf("Phase 4 WriteEvents() failed: %v", err)
	}
	assertIndexCountsEqual(t, store, "phase 4: final state after mixed ops")
}

func TestDeleteByFilterMaintainsIndexCountsUnderSecondaryKeyCollisions(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())
	targetAuthor := [32]byte{0xAA}
	keepAuthor := [32]byte{0xBB}
	const perAuthor = 160

	events := make([]*types.Event, 0, perAuthor*2)
	for i := 0; i < perAuthor; i++ {
		events = append(events,
			&types.Event{
				ID:        [32]byte{0x10, byte(i), byte(i >> 8)},
				Pubkey:    targetAuthor,
				CreatedAt: now,
				Kind:      1,
				Content:   fmt.Sprintf("target-%d", i),
				Tags:      [][]string{{"p", "shared"}, {"e", fmt.Sprintf("target-ref-%d", i)}},
			},
			&types.Event{
				ID:        [32]byte{0x20, byte(i), byte(i >> 8)},
				Pubkey:    keepAuthor,
				CreatedAt: now,
				Kind:      1,
				Content:   fmt.Sprintf("keep-%d", i),
				Tags:      [][]string{{"p", "shared"}, {"e", fmt.Sprintf("keep-ref-%d", i)}},
			},
		)
	}

	if _, err := store.WriteEvents(ctx, events); err != nil {
		t.Fatalf("WriteEvents() failed: %v", err)
	}
	assertIndexCountsEqual(t, store, "after colliding writes")

	deleted, err := store.DeleteByFilter(ctx, &types.QueryFilter{Authors: [][32]byte{targetAuthor}})
	if err != nil {
		t.Fatalf("DeleteByFilter() failed: %v", err)
	}
	if deleted != perAuthor {
		t.Fatalf("DeleteByFilter() deleted %d events, want %d", deleted, perAuthor)
	}

	assertIndexCountsEqual(t, store, "after DeleteByFilter on colliding secondary keys")

	impl := store.(*eventStoreImpl)
	stats := impl.indexMgr.AllStats()
	if stats["primary"].EntryCount != perAuthor {
		t.Fatalf("Expected %d primary entries remaining, got %d", perAuthor, stats["primary"].EntryCount)
	}

	if err := store.Close(ctx); err != nil {
		t.Fatalf("Close() failed: %v", err)
	}

	reopened := New(&Options{
		Config:       impl.config.Get(),
		RecoveryMode: "auto",
	})
	if err := reopened.Open(ctx, filepath.Dir(impl.indexDir), false); err != nil {
		t.Fatalf("Re-open failed: %v", err)
	}
	defer reopened.Close(ctx)
	assertIndexCountsEqual(t, reopened, "after reopen following DeleteByFilter")

	reopenedStats := reopened.(*eventStoreImpl).indexMgr.AllStats()
	if reopenedStats["primary"].EntryCount != perAuthor {
		t.Fatalf("Expected %d primary entries after reopen, got %d", perAuthor, reopenedStats["primary"].EntryCount)
	}
}

func TestInsertRecoveryBatchSkipsExistingPrimaryEntries(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	now := uint32(time.Now().Unix())
	events := make([]*types.Event, 40)
	for i := range events {
		events[i] = makeTestEvent(byte(i+1), uint16(i%3+1), now+uint32(i))
	}

	locs, err := store.WriteEvents(ctx, events)
	if err != nil {
		t.Fatalf("WriteEvents() failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "before InsertRecoveryBatch replay")

	impl := store.(*eventStoreImpl)
	if err := impl.indexMgr.InsertRecoveryBatch(ctx, events, locs, false); err != nil {
	}

	assertIndexCountsEqual(t, store, "after InsertRecoveryBatch replay of existing events")

	stats := impl.indexMgr.AllStats()
	if stats["primary"].EntryCount != uint64(len(events)) {
		t.Fatalf("Expected %d primary entries after replay dedup, got %d", len(events), stats["primary"].EntryCount)
	}

	dupEvents := []*types.Event{events[0], events[1], events[0], events[2]}
	dupLocs := []types.RecordLocation{locs[0], locs[1], locs[0], locs[2]}
	if err := impl.indexMgr.InsertRecoveryBatch(ctx, dupEvents, dupLocs, false); err != nil {
		t.Fatalf("InsertRecoveryBatch() with duplicate IDs failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "after InsertRecoveryBatch with duplicate IDs")
	stats = impl.indexMgr.AllStats()
	if stats["primary"].EntryCount != uint64(len(events)) {
		t.Fatalf("Expected %d primary entries after duplicate replay, got %d", len(events), stats["primary"].EntryCount)
	}

	// A changed location for the same event ID should replace the old location
	// rather than creating a second primary/secondary entry.
	relocated := *events[0]
	relocatedLoc := types.RecordLocation{SegmentID: locs[0].SegmentID + 99, Offset: locs[0].Offset + 123}
	if err := impl.indexMgr.InsertRecoveryBatch(ctx, []*types.Event{&relocated}, []types.RecordLocation{relocatedLoc}, false); err != nil {
		t.Fatalf("InsertRecoveryBatch() with relocated event failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "after InsertRecoveryBatch relocation upsert")
	storedLoc, exists, err := impl.indexMgr.PrimaryIndex().Get(ctx, impl.keyBuilder.BuildPrimaryKey(relocated.ID))
	if err != nil {
		t.Fatalf("PrimaryIndex().Get() failed: %v", err)
	}
	if !exists {
		t.Fatal("Expected relocated event to exist in primary index")
	}
	if storedLoc != relocatedLoc {
		t.Fatalf("Expected relocated location %+v, got %+v", relocatedLoc, storedLoc)
	}
}

// TestInsertRecoveryBatch_RepairsKindTimeGaps verifies that InsertRecoveryBatch
// detects and repairs kind_time entries that are missing despite the primary
// index having the correct location (sameLocationSkip self-healing fix).
// This reproduces the production scenario where kind_time.count > primary.count
// was observed after DeleteByFilter: some events had their kind_time entries
// silently never inserted due to a non-fatal write failure.
func TestInsertRecoveryBatch_RepairsKindTimeGaps(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	impl := store.(*eventStoreImpl)
	now := uint32(time.Now().Unix())

	// Two events share the same kind_time key (kind=1, created_at=now).
	// A third event uses a distinct key to ensure the repair is scoped correctly.
	events := []*types.Event{
		makeTestEvent(1, 1, now),
		makeTestEvent(2, 1, now),   // same kind+time → shares kind_time key
		makeTestEvent(3, 1, now+1), // distinct kind_time key
	}

	locs, err := store.WriteEvents(ctx, events)
	if err != nil {
		t.Fatalf("WriteEvents failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "before gap creation")

	// Artificially delete kind_time entries for events[0] and events[1] to simulate
	// a prior silent write failure that left permanent gaps.
	kb := impl.indexMgr.KeyBuilder()
	kindTimeIdx := impl.indexMgr.KindTimeIndex()
	sharedKey := kb.BuildKindTimeKey(events[0].Kind, events[0].CreatedAt)
	for i := 0; i < 2; i++ {
		if err := kindTimeIdx.Delete(ctx, sharedKey, &locs[i]); err != nil {
			t.Fatalf("Delete kind_time[%d] failed: %v", i, err)
		}
	}

	statsGap := impl.indexMgr.AllStats()
	primaryCount := statsGap["primary"].EntryCount
	kindTimeCount := statsGap["kind_time"].EntryCount
	if kindTimeCount != primaryCount-2 {
		t.Fatalf("expected kind_time gap of 2 (primary=%d kind_time=%d)", primaryCount, kindTimeCount)
	}

	// InsertRecoveryBatch must detect the missing kind_time entries (via range scan
	// over the exact key) and insert them, restoring consistency.
	if err := impl.indexMgr.InsertRecoveryBatch(ctx, events, locs, false); err != nil {
		t.Fatalf("InsertRecoveryBatch failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "after InsertRecoveryBatch repairs kind_time gaps")

	// Ensure no duplicate entries were created by the repair (count must not exceed primary).
	statsRepaired := impl.indexMgr.AllStats()
	if statsRepaired["kind_time"].EntryCount != statsRepaired["primary"].EntryCount {
		t.Errorf("kind_time count (%d) != primary count (%d) after repair",
			statsRepaired["kind_time"].EntryCount, statsRepaired["primary"].EntryCount)
	}
}

// TestInsertRecoveryBatch_RepairsAuthorTimeGaps verifies that InsertRecoveryBatch
// repairs author_time entries that are missing despite the primary index having
// the correct location.
func TestInsertRecoveryBatch_RepairsAuthorTimeGaps(t *testing.T) {
	store, ctx := openTestStoreForIndexTest(t)
	defer store.Close(ctx)

	impl := store.(*eventStoreImpl)
	now := uint32(time.Now().Unix())

	event := makeTestEvent(7, 3, now)
	loc, err := store.WriteEvent(ctx, event)
	if err != nil {
		t.Fatalf("WriteEvent failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "before gap creation")

	// Delete the author_time entry to simulate a prior silent write failure.
	kb := impl.indexMgr.KeyBuilder()
	authorTimeIdx := impl.indexMgr.AuthorTimeIndex()
	atKey := kb.BuildAuthorTimeKey(event.Pubkey, event.Kind, event.CreatedAt)
	if err := authorTimeIdx.Delete(ctx, atKey, &loc); err != nil {
		t.Fatalf("Delete author_time failed: %v", err)
	}

	statsGap := impl.indexMgr.AllStats()
	if statsGap["author_time"].EntryCount >= statsGap["primary"].EntryCount {
		t.Fatalf("expected author_time gap, primary=%d author_time=%d",
			statsGap["primary"].EntryCount, statsGap["author_time"].EntryCount)
	}

	// InsertRecoveryBatch must repair the author_time gap.
	if err := impl.indexMgr.InsertRecoveryBatch(ctx, []*types.Event{event}, []types.RecordLocation{loc}, false); err != nil {
		t.Fatalf("InsertRecoveryBatch failed: %v", err)
	}

	assertIndexCountsEqual(t, store, "after InsertRecoveryBatch repairs author_time gap")
}
