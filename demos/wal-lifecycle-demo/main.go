package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/haorendashu/nostr_event_store/src/config"
	"github.com/haorendashu/nostr_event_store/src/eventstore"
	"github.com/haorendashu/nostr_event_store/src/types"
	"github.com/haorendashu/nostr_event_store/src/wal"
)

type walFileInfo struct {
	Name string
	Size int64
}

func main() {
	var (
		rootDir         = flag.String("dir", "./demo_data", "store root directory")
		eventCount      = flag.Int("events", 300, "number of events to write")
		contentBytes    = flag.Int("content-bytes", 2048, "payload size per event in bytes")
		segmentSizeKB   = flag.Int("segment-size-kb", 64, "WAL max segment size (KB), low value to force rotation")
		batchSyncMode   = flag.String("sync-mode", "batch", "WAL sync mode: always|batch|never")
		applyDelete     = flag.Bool("apply-delete", false, "delete candidate WAL segments on real WAL directory")
		keepData        = flag.Bool("keep-data", true, "keep demo data after run")
		skipWrite       = flag.Bool("skip-write", false, "skip event writes and only inspect existing WAL")
		dryRunWorkDir   = flag.String("dryrun-dir", "", "optional dry-run working dir (default auto temp under root dir)")
		checkpointAfter = flag.Bool("checkpoint", true, "create an explicit checkpoint before analysis")
	)
	flag.Parse()

	ctx := context.Background()
	logger := log.New(os.Stdout, "[wal-demo] ", log.LstdFlags)

	cfg := config.DefaultConfig()
	cfg.WALConfig.Disabled = false
	cfg.WALConfig.SyncMode = *batchSyncMode
	cfg.WALConfig.MaxSegmentSize = uint64(*segmentSizeKB) * 1024
	cfg.WALConfig.CheckpointIntervalMs = 60 * 60 * 1000
	cfg.WALConfig.CheckpointEventCount = 1 << 30
	cfg.IndexConfig.IndexDir = filepath.Join(*rootDir, "indexes")

	logger.Printf("Open store at %s", *rootDir)
	store := eventstore.New(&eventstore.Options{Config: cfg})
	if err := store.Open(ctx, *rootDir, true); err != nil {
		log.Fatalf("open store failed: %v", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}()

	writtenIDs := make([][32]byte, 0, *eventCount)
	if !*skipWrite {
		logger.Printf("Write events: count=%d content=%dB", *eventCount, *contentBytes)
		for i := 0; i < *eventCount; i++ {
			evt := demoEvent(i, *contentBytes)
			if _, err := store.WriteEvent(ctx, evt); err != nil {
				log.Fatalf("write event %d failed: %v", i, err)
			}
			writtenIDs = append(writtenIDs, evt.ID)
		}
	}

	if err := store.Flush(ctx); err != nil {
		log.Fatalf("flush failed: %v", err)
	}

	var checkpointLSN uint64
	if *checkpointAfter {
		lsn, err := store.WAL().Writer().CreateCheckpoint(ctx)
		if err != nil {
			log.Fatalf("create checkpoint failed: %v", err)
		}
		checkpointLSN = lsn
		logger.Printf("Checkpoint created at LSN=%d", checkpointLSN)
	} else {
		cp, err := store.WAL().LastCheckpoint()
		if err == nil {
			checkpointLSN = cp.LSN
		}
		logger.Printf("Use latest checkpoint LSN=%d", checkpointLSN)
	}

	stats, err := store.WAL().Stats(ctx)
	if err != nil {
		log.Fatalf("wal stats failed: %v", err)
	}
	fmt.Printf("\n=== WAL Stats ===\n")
	fmt.Printf("CurrentLSN: %d\n", stats.CurrentLSN)
	fmt.Printf("FirstLSN: %d\n", stats.FirstLSN)
	fmt.Printf("LastCheckpointLSN: %d\n", stats.LastCheckpointLSN)
	fmt.Printf("TotalSegmentSize: %d bytes\n", stats.TotalSegmentSize)

	walDir := filepath.Join(*rootDir, "wal")
	printWalFiles(walDir)
	printValidatorReport(walDir)

	candidateFiles, err := dryRunDeletableFiles(ctx, walDir, cfg.WALConfig, checkpointLSN, *dryRunWorkDir)
	if err != nil {
		log.Fatalf("dry-run delete analysis failed: %v", err)
	}

	fmt.Printf("\n=== Safe-delete Candidates (Dry-run) ===\n")
	fmt.Printf("Safety line: checkpoint LSN = %d\n", checkpointLSN)
	if len(candidateFiles) == 0 {
		fmt.Println("No segment is deletable now.")
	} else {
		for _, f := range candidateFiles {
			fmt.Printf("- %s\n", f)
		}
	}

	if *applyDelete {
		if checkpointLSN == 0 {
			log.Fatalf("apply-delete requires a valid checkpoint LSN > 0")
		}
		logger.Printf("Apply delete on real WAL: before LSN=%d", checkpointLSN)
		if err := store.WAL().DeleteSegmentsBefore(ctx, checkpointLSN); err != nil {
			log.Fatalf("apply delete failed: %v", err)
		}
		fmt.Printf("\n=== WAL Files After Delete ===\n")
		printWalFiles(walDir)
	} else {
		fmt.Println("\nDelete not applied (dry-run only). Use -apply-delete=true to execute.")
	}

	closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := store.Close(closeCtx); err != nil {
		log.Fatalf("close failed: %v", err)
	}

	if err := verifyReopen(*rootDir, cfg, writtenIDs); err != nil {
		log.Fatalf("reopen verification failed: %v", err)
	}
	fmt.Println("\n✅ Reopen verification passed.")

	if !*keepData {
		if err := os.RemoveAll(*rootDir); err != nil {
			log.Fatalf("cleanup failed: %v", err)
		}
		fmt.Printf("Cleaned: %s\n", *rootDir)
	}
}

func demoEvent(seq int, contentSize int) *types.Event {
	seed := fmt.Sprintf("demo-event-%d", seq)
	id := sha256.Sum256([]byte(seed))
	pub := sha256.Sum256([]byte("demo-pubkey"))
	payload := strings.Repeat("x", max(1, contentSize))
	return &types.Event{
		ID:        id,
		Pubkey:    pub,
		CreatedAt: uint32(time.Now().Unix()) + uint32(seq),
		Kind:      1,
		Tags: [][]string{
			{"t", "wal-demo"},
			{"source", "wal-lifecycle-demo"},
		},
		Content: fmt.Sprintf("%s-%s", seed, payload),
	}
}

func printWalFiles(walDir string) {
	files, err := listWalFiles(walDir)
	if err != nil {
		fmt.Printf("list WAL files failed: %v\n", err)
		return
	}
	fmt.Printf("\n=== WAL Files (%s) ===\n", walDir)
	if len(files) == 0 {
		fmt.Println("No WAL files")
		return
	}
	var total int64
	for _, f := range files {
		total += f.Size
		fmt.Printf("- %s: %d bytes\n", f.Name, f.Size)
	}
	fmt.Printf("Total: %d bytes\n", total)
}

func printValidatorReport(walDir string) {
	results, err := wal.ValidateWALDirectory(walDir)
	if err != nil {
		fmt.Printf("validate WAL directory failed: %v\n", err)
		return
	}
	fmt.Printf("\n=== WAL Validator Summary ===\n")
	if len(results) == 0 {
		fmt.Println("No WAL .log files")
		return
	}
	sort.Slice(results, func(i, j int) bool {
		return filepath.Base(results[i].FilePath) < filepath.Base(results[j].FilePath)
	})
	for _, r := range results {
		fmt.Printf("- %s | size=%d | valid=%d/%d | checkpoint=%d | header=%v\n",
			filepath.Base(r.FilePath), r.FileSize, r.ValidEntries, r.TotalEntries, r.LastCheckpointLSN, r.HeaderValid)
	}
}

func dryRunDeletableFiles(ctx context.Context, walDir string, walCfg config.WALConfig, checkpointLSN uint64, dryRunWorkDir string) ([]string, error) {
	if checkpointLSN == 0 {
		return nil, nil
	}

	srcFiles, err := listWalFileNames(walDir)
	if err != nil {
		return nil, err
	}

	if len(srcFiles) == 0 {
		return nil, nil
	}

	var workRoot string
	if dryRunWorkDir != "" {
		workRoot = dryRunWorkDir
		if err := os.RemoveAll(workRoot); err != nil {
			return nil, fmt.Errorf("cleanup dryrun dir: %w", err)
		}
		if err := os.MkdirAll(workRoot, 0755); err != nil {
			return nil, fmt.Errorf("create dryrun dir: %w", err)
		}
	} else {
		workRoot, err = os.MkdirTemp(filepath.Dir(walDir), "wal-dryrun-*")
		if err != nil {
			return nil, fmt.Errorf("create temp dryrun dir: %w", err)
		}
		defer os.RemoveAll(workRoot)
	}

	dryWalDir := filepath.Join(workRoot, "wal")
	if err := copyDir(walDir, dryWalDir); err != nil {
		return nil, fmt.Errorf("copy wal dir: %w", err)
	}

	mgr := wal.NewManager()
	if err := mgr.Open(ctx, wal.Config{
		Dir:             dryWalDir,
		MaxSegmentSize:  walCfg.MaxSegmentSize,
		SyncMode:        walCfg.SyncMode,
		BatchIntervalMs: walCfg.BatchIntervalMs,
		BatchSizeBytes:  walCfg.BatchSizeBytes,
	}); err != nil {
		return nil, fmt.Errorf("open dryrun wal manager: %w", err)
	}
	defer mgr.Close()

	if err := mgr.DeleteSegmentsBefore(ctx, checkpointLSN); err != nil {
		return nil, fmt.Errorf("dryrun delete segments: %w", err)
	}

	dstFiles, err := listWalFileNames(dryWalDir)
	if err != nil {
		return nil, err
	}

	beforeSet := make(map[string]struct{}, len(srcFiles))
	for _, n := range srcFiles {
		beforeSet[n] = struct{}{}
	}
	for _, n := range dstFiles {
		delete(beforeSet, n)
	}

	deleted := make([]string, 0, len(beforeSet))
	for n := range beforeSet {
		deleted = append(deleted, n)
	}
	sort.Strings(deleted)
	return deleted, nil
}

func verifyReopen(rootDir string, cfg *config.Config, ids [][32]byte) error {
	ctx := context.Background()
	store := eventstore.New(&eventstore.Options{Config: cfg})
	if err := store.Open(ctx, rootDir, false); err != nil {
		return fmt.Errorf("reopen store: %w", err)
	}
	defer func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = store.Close(closeCtx)
	}()

	if len(ids) == 0 {
		return nil
	}
	check := min(len(ids), 10)
	for i := 0; i < check; i++ {
		if _, err := store.GetEvent(ctx, ids[i]); err != nil {
			return fmt.Errorf("event[%d] not found after reopen: %w", i, err)
		}
	}
	return nil
}

func listWalFiles(walDir string) ([]walFileInfo, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	files := make([]walFileInfo, 0)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".log" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		files = append(files, walFileInfo{Name: e.Name(), Size: info.Size()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func listWalFileNames(walDir string) ([]string, error) {
	files, err := listWalFiles(walDir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(files))
	for _, f := range files {
		names = append(names, f.Name)
	}
	return names, nil
}

func copyDir(src, dst string) error {
	if err := os.MkdirAll(dst, 0755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := filepath.Join(src, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyDir(srcPath, dstPath); err != nil {
				return err
			}
			continue
		}
		data, err := os.ReadFile(srcPath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(dstPath, data, 0644); err != nil {
			return err
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
