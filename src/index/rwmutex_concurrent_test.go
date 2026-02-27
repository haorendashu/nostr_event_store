package index

import (
"context"
"math/rand"
"sync"
"sync/atomic"
"testing"
"time"

"github.com/haorendashu/nostr_event_store/src/types"
)

func makeTestKey(id int) []byte {
key := make([]byte, 32)
for i := 0; i < 4; i++ {
key[i] = byte(id >> (24 - i*8))
}
return key
}

func TestRWMutexConcurrentReadWrite(t *testing.T) {
dir := t.TempDir()
cfg := &Config{
PageSize:                4096,
PrimaryIndexCacheMB:     16,
AuthorTimeIndexCacheMB:  8,
SearchIndexCacheMB:      8,
KindTimeIndexCacheMB:    8,
TagNameToSearchTypeCode: make(map[string]SearchType),
}

indexPath := dir + "/test.idx"
idx, err := NewPersistentBTreeIndexWithType(indexPath, *cfg, IndexTypePrimary)
if err != nil {
t.Fatalf("Failed to create index: %v", err)
}
defer idx.Close()

ctx := context.Background()

numInitialRecords := 1000
for i := 0; i < numInitialRecords; i++ {
key := makeTestKey(i)
loc := types.RecordLocation{SegmentID: uint32(i), Offset: uint32(i * 100)}
if err := idx.Insert(ctx, key, loc); err != nil {
t.Fatalf("Failed to insert: %v", err)
}
}

if err := idx.Flush(ctx); err != nil {
t.Fatalf("Failed to flush: %v", err)
}

const (
numReaders   = 50
numWriters   = 5
testDuration = 3 * time.Second
)

var wg sync.WaitGroup
var readOps, writeOps, readErrors, writeErrors uint64
stopChan := make(chan struct{})

go func() {
time.Sleep(testDuration)
close(stopChan)
}()

startTime := time.Now()

for i := 0; i < numReaders; i++ {
wg.Add(1)
go func() {
defer wg.Done()
localOps := 0
for {
select {
case <-stopChan:
atomic.AddUint64(&readOps, uint64(localOps))
return
default:
keyID := rand.Intn(numInitialRecords)
key := makeTestKey(keyID)
_, _, err := idx.Get(ctx, key)
if err != nil {
atomic.AddUint64(&readErrors, 1)
return
}
localOps++
}
}
}()
}

for i := 0; i < numWriters; i++ {
wg.Add(1)
go func(writerID int) {
defer wg.Done()
localOps := 0
baseKeyID := numInitialRecords + writerID*10000
for {
select {
case <-stopChan:
atomic.AddUint64(&writeOps, uint64(localOps))
return
default:
keyID := baseKeyID + localOps%1000
key := makeTestKey(keyID)
loc := types.RecordLocation{
SegmentID: uint32(writerID),
Offset:    uint32(localOps),
}
if err := idx.Insert(ctx, key, loc); err != nil {
atomic.AddUint64(&writeErrors, 1)
return
}
localOps++
time.Sleep(time.Microsecond * 100)
}
}
}(i)
}

wg.Wait()
elapsed := time.Since(startTime)

totalOps := readOps + writeOps
t.Logf("=== Concurrent Test Results (RWMutex) ===")
t.Logf("Duration: %v", elapsed)
t.Logf("Read operations: %d (errors: %d)", readOps, readErrors)
t.Logf("Write operations: %d (errors: %d)", writeOps, writeErrors)
t.Logf("Total operations: %d", totalOps)
t.Logf("Operations per second: %.2f", float64(totalOps)/elapsed.Seconds())

if readErrors+writeErrors > 0 {
t.Errorf("Test had errors: %d read, %d write", readErrors, writeErrors)
}

stats := idx.Stats()
t.Logf("Final stats - Entries: %d, Nodes: %d, Depth: %d",
stats.EntryCount, stats.NodeCount, stats.Depth)
}
