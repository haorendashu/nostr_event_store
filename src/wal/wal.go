// Package wal implements write-ahead logging (WAL) for durability.
// All modifications to events and indexes are logged before being applied in-memory,
// enabling recovery from crashes without data loss.
package wal

import (
	"context"

	"nostr_event_store/src/types"
)

// OpType represents the type of operation logged in the WAL.
type OpType uint8

const (
	// OpTypeInsert represents an event insertion operation.
	OpTypeInsert OpType = iota + 1

	// OpTypeUpdateFlags represents an event flags update (deleted/replaced).
	OpTypeUpdateFlags

	// OpTypeIndexUpdate represents an index node update.
	OpTypeIndexUpdate

	// OpTypeCheckpoint represents a checkpoint marker (for recovery optimization).
	OpTypeCheckpoint
)

// Entry represents a single WAL entry.
type Entry struct {
	// Type is the operation type (insert, update_flags, index_update, checkpoint).
	Type OpType

	// LSN is the log sequence number, assigned by the WAL writer.
	// LSN uniquely identifies entries and enables crash recovery from a known point.
	LSN uint64

	// Timestamp is the creation time of the entry (for debugging and monitoring).
	Timestamp uint64

	// EventDataOrMetadata contains:
	// - For OpTypeInsert: serialized event data
	// - For OpTypeUpdateFlags: flags and location
	// - For OpTypeIndexUpdate: index key and value update
	// - For OpTypeCheckpoint: empty
	EventDataOrMetadata []byte

	// Checksum is a CRC64 checksum for detecting corruption.
	Checksum uint64
}

// LSN is a log sequence number - a monotonically increasing identifier for WAL entries.
// Used to determine recovery starting points and coordinate durability points.
type LSN = uint64

// Config holds WAL configuration parameters.
type Config struct {
	// Dir is the directory where WAL files are stored.
	Dir string

	// MaxSegmentSize is the maximum size of a WAL segment file.
	// Once exceeded, a new segment is created.
	// Default: 1 GB
	MaxSegmentSize uint64

	// SyncMode determines durability:
	// - "always": fsync after every entry (safest, slowest)
	// - "batch": fsync periodically (default, balanced)
	// - "never": rely on OS cache (fastest, least safe)
	SyncMode string

	// BatchIntervalMs is the interval in milliseconds between batch fsyncs (for SyncMode="batch").
	// Default: 100 ms
	BatchIntervalMs int

	// BatchSizeBytes is the size of the batch buffer before forcing an fsync.
	// Default: 10 MB
	BatchSizeBytes uint32
}

// Writer is the interface for writing entries to the WAL.
// Must be used sequentially (not thread-safe); wrap with sync.Mutex if needed in concurrent access.
type Writer interface {
	// Open initializes the WAL for writing.
	// Creates the WAL directory and files if they don't exist.
	// ctx is used for cancellation and timeouts.
	Open(ctx context.Context, cfg Config) error

	// Write appends an entry to the WAL and returns its LSN.
	// The entry is first written to a buffer; actual disk persistence depends on SyncMode.
	// ctx is used for cancellation and timeouts.
	Write(ctx context.Context, entry *Entry) (LSN, error)

	// Flush commits all buffered entries to disk (calls fsync).
	// After Flush returns, all previous entries are crash-safe.
	// ctx is used for cancellation and timeouts.
	Flush(ctx context.Context) error

	// CreateCheckpoint creates a checkpoint marker at the current LSN.
	// Used to allow recovery to skip entries before the checkpoint.
	// ctx is used for cancellation and timeouts.
	CreateCheckpoint(ctx context.Context) (LSN, error)

	// LastLSN returns the LSN of the last written entry (whether flushed or not).
	// Returns 0 if no entries have been written.
	LastLSN() LSN

	// Close closes the writer and releases resources.
	Close() error
}

// Reader is the interface for sequentially reading entries from the WAL.
// Used for recovery and replay.
type Reader interface {
	// Open initializes the WAL reader at a given starting LSN.
	// If startLSN is 0, reads from the beginning.
	// If startLSN > 0, seeks to that LSN and reads from there.
	// ctx is used for cancellation and timeouts.
	Open(ctx context.Context, dir string, startLSN LSN) error

	// Read returns the next entry in the WAL, or io.EOF if at the end.
	// Entries are returned in order of increasing LSN.
	// Returns error if reading or validation fails (e.g., checksum mismatch).
	// ctx is used for cancellation and timeouts.
	Read(ctx context.Context) (*Entry, error)

	// LastValidLSN returns the LSN of the last successfully read entry.
	// If no entries have been read, returns 0.
	LastValidLSN() LSN

	// Close closes the reader and releases resources.
	Close() error
}

// Checkpoint represents a recovery checkpoint.
type Checkpoint struct {
	// LSN is the log sequence number where this checkpoint was created.
	LSN LSN

	// Timestamp is when the checkpoint was created.
	Timestamp uint64

	// LastSegmentID is the ID of the last segment that was closed before this checkpoint.
	LastSegmentID uint32

	// CompactionState is opaque data about any in-progress compaction at checkpoint time.
	// Used to determine which compaction snapshots are valid.
	CompactionState []byte
}

// Manager manages the WAL lifecycle including multiple segments, checkpoints, and cleanup.
type Manager interface {
	// Open initializes the WAL manager.
	// ctx is used for cancellation and timeouts.
	Open(ctx context.Context, cfg Config) error

	// Writer returns a Writer for appending entries.
	// Entry writes are sequenced through the returned Writer.
	Writer() Writer

	// Reader returns a Reader for crash recovery.
	// The reader is positioned at the most recent checkpoint (or beginning if no checkpoints).
	// ctx is used for cancellation and timeouts.
	Reader(ctx context.Context) (Reader, error)

	// LastCheckpoint returns the most recent checkpoint.
	// Returns error if no checkpoints exist.
	LastCheckpoint() (Checkpoint, error)

	// Checkpoints returns all available checkpoints in ascending order by LSN.
	// Returns empty list if no checkpoints exist.
	Checkpoints() []Checkpoint

	// DeleteSegmentsBefore deletes all segments created before the given LSN.
	// Used after checkpointing to reclaim disk space (old WAL segments are no longer needed for recovery).
	// ctx is used for cancellation and timeouts.
	DeleteSegmentsBefore(ctx context.Context, beforeLSN LSN) error

	// Stats returns WAL statistics (size, checkpoint count, latest LSN).
	Stats(ctx context.Context) (Stats, error)

	// Close closes the manager and all resources.
	Close() error
}

// Stats captures WAL statistics for monitoring.
type Stats struct {
	// CurrentLSN is the LSN of the last written entry.
	CurrentLSN LSN

	// CheckpointCount is the number of available checkpoints.
	CheckpointCount int

	// TotalSegmentSize is the total size of all WAL segments in bytes.
	TotalSegmentSize uint64

	// FirstLSN is the LSN of the oldest available entry (after cleanup).
	FirstLSN LSN

	// LastCheckpointLSN is the LSN of the most recent checkpoint.
	LastCheckpointLSN LSN
}

// Replayer replays WAL entries to rebuild in-memory state after a crash.
// It applies each entry to the appropriate component (indexes, storage, etc).
type Replayer interface {
	// OnInsert is called for each OpTypeInsert entry during replay.
	// location is where the event was stored; event is the deserialized content.
	// Implementations should update indexes as if the event was just inserted.
	// Return error if replay should abort.
	OnInsert(ctx context.Context, event *types.Event, location types.RecordLocation) error

	// OnUpdateFlags is called for each OpTypeUpdateFlags entry.
	// location is the event's location; flags are its new flags.
	// Implementations should update index entries to mark events as deleted/replaced.
	// Return error if replay should abort.
	OnUpdateFlags(ctx context.Context, location types.RecordLocation, flags types.EventFlags) error

	// OnIndexUpdate is called for each OpTypeIndexUpdate entry.
	// key and value are raw index metadata (format depends on index type).
	// Implementations should apply the update to the appropriate index.
	// Return error if replay should abort.
	OnIndexUpdate(ctx context.Context, key []byte, value []byte) error

	// OnCheckpoint is called for each OpTypeCheckpoint entry.
	// checkpoint contains metadata about the checkpoint.
	// Implementations may use this to optimize recovery.
	OnCheckpoint(ctx context.Context, checkpoint Checkpoint) error
}

// NewWriter creates a new WAL writer.
func NewWriter() Writer {
	panic("not implemented")
}

// NewReader creates a new WAL reader.
func NewReader() Reader {
	panic("not implemented")
}

// NewManager creates a new WAL manager.
func NewManager() Manager {
	panic("not implemented")
}
