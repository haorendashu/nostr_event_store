// Package types defines core data structures for the Nostr event store.
package types

import (
	"encoding/hex"
	"time"
)

// Event represents a Nostr event according to NIP-01.
// All fields except Tags and Content are fixed-size for efficient serialization.
type Event struct {
	// ID is the SHA-256 hash of the event, serving as primary key (32 bytes).
	ID [32]byte

	// Pubkey is the author's public key (32 bytes), used for author-based queries.
	Pubkey [32]byte

	// CreatedAt is the UNIX timestamp in seconds when the event was created (4 bytes).
	// Critical for time-ordered range scans and replaceable event semantics.
	CreatedAt uint32

	// Kind is the event type (2 bytes). Determines replaceable vs non-replaceable semantics.
	// Ranges: 0-3 (replaceable), 10000-19999 (replaceable), 30000-39999 (parameterized replaceable).
	Kind uint16

	// Tags is a variable-length array of tags, each tag is an array of strings.
	// Common tags: "e" (event ref), "p" (person ref), "t" (hashtag), "d" (identifier),
	// "a" (addressable ref), "r" (URL), "subject", etc.
	// Serialized as TLV or JSON array format.
	Tags [][]string

	// Content is the event payload (variable length).
	// Can be plain text, JSON, or other formats depending on kind.
	Content string

	// Sig is the Ed25519 signature (64 bytes), proving the event's authenticity.
	Sig [64]byte
}

// RecordLocation represents the physical location of a serialized event record
// in a segment file (data.0, data.1, etc).
// Used as the value in all index B+Trees to locate events.
type RecordLocation struct {
	// SegmentID uniquely identifies a segment file (e.g., 0 for data.0, 1 for data.1).
	SegmentID uint32

	// Offset is the byte offset within the segment where the record starts.
	// Must be page-aligned for efficient reading.
	Offset uint32
}

// Tag represents a single Nostr tag within an event.
// Each tag is a variable-length array of strings (the first element is the tag name).
//
// Common Nostr tags:
// - "e": Event reference (threading), format: ["e", event_id, relay_url, marker]
// - "p": Person reference (user mention), format: ["p", pubkey, relay_url]
// - "t": Hashtag, format: ["t", hashtag_name]
// - "a": Addressable event reference, format: ["a", "kind:pubkey:d-identifier"]
// - "d": Identifier (used for parameterized replaceable events), format: ["d", identifier]
// - "r": URL reference, format: ["r", url]
// - "subject": Thread subject/title, format: ["subject", title]
// - "relay": Relay recommendation, format: ["relay", relay_url]
// - "amount": LN amount, format: ["amount", sats]
// - "bolt11": LN invoice, format: ["bolt11", invoice_string]
type Tag struct {
	// Name is the first element of the tag (e.g., "e", "p", "t").
	Name string

	// Values contains the remaining elements of the tag.
	Values []string
}

// NewTag creates a new Tag with the given name and values.
// This is a convenience constructor for building tags during event creation.
func NewTag(name string, values ...string) Tag {
	return Tag{
		Name:   name,
		Values: values,
	}
}

// EventFlags represents the mutable state flags of a stored event record.
// These flags are updated during event lifecycle (insertion → replacement → deletion → compaction).
type EventFlags uint8

const (
	// FlagDeleted indicates the event has been logically deleted and should be skipped in queries.
	FlagDeleted EventFlags = 1 << iota

	// FlagReplaced indicates the event has been superseded by a newer version (for replaceable kinds).
	// Marked for physical deletion during compaction.
	FlagReplaced

	// FlagReserved reserved for future use.
	FlagReserved

	// Bit 3-6: Reserved for future flags
	// ...

	// FlagContinued (bit 7) indicates the record spans multiple consecutive pages.
	// When set, the record uses continuation pages with CONT magic.
	// First page contains: record_len (total), record_flags, continuation_count, partial_data
	// Continuation pages contain: magic (0x434F4E54), chunk_len, chunk_data
	FlagContinued EventFlags = 1 << 7
)

// IsDeleted returns true if the DELETED flag is set.
func (f EventFlags) IsDeleted() bool {
	return f&FlagDeleted != 0
}

// IsReplaced returns true if the REPLACED flag is set.
func (f EventFlags) IsReplaced() bool {
	return f&FlagReplaced != 0
}

// SetDeleted sets the DELETED flag.
func (f *EventFlags) SetDeleted(deleted bool) {
	if deleted {
		*f |= FlagDeleted
	} else {
		*f &= ^FlagDeleted
	}
}

// SetReplaced sets the REPLACED flag.
func (f *EventFlags) SetReplaced(replaced bool) {
	if replaced {
		*f |= FlagReplaced
	} else {
		*f &= ^FlagReplaced
	}
}

// IsContinued returns true if the CONTINUED flag is set (multi-page record).
func (f EventFlags) IsContinued() bool {
	return f&FlagContinued != 0
}

// SetContinued sets the CONTINUED flag (for multi-page records).
func (f *EventFlags) SetContinued(continued bool) {
	if continued {
		*f |= FlagContinued
	} else {
		*f &= ^FlagContinued
	}
}

// ReplacementKey represents the key used to determine if an event is replaceable.
// For replaceable events (kind 0, 3, 10000-19999, 30000-39999),
// only the event with the highest created_at (or smallest id as tiebreaker) is kept.
type ReplacementKey struct {
	// Pubkey is the event author's public key.
	Pubkey [32]byte

	// Kind is the event kind (must be in replaceable range).
	Kind uint16

	// DTag is the "d" tag value for parameterized replaceable events (kind 30000-39999).
	// For non-parameterized replaceable (kind 0, 3, 10000-19999), this is empty.
	DTag string
}

// IsReplaceable returns true if the given kind is replaceable according to Nostr spec.
// Replaceable kinds: 0, 3, 10000-19999, 30000-39999.
func IsReplaceable(kind uint16) bool {
	switch {
	case kind == 0 || kind == 3:
		return true
	case kind >= 10000 && kind < 20000:
		return true
	case kind >= 30000 && kind < 40000:
		return true
	default:
		return false
	}
}

// IsParameterizedReplaceable returns true if the kind is parameterized replaceable (30000-39999).
func IsParameterizedReplaceable(kind uint16) bool {
	return kind >= 30000 && kind < 40000
}

// QueryFilter represents filtering criteria for event queries.
// Used to build complex queries across multiple indexes.
type QueryFilter struct {
	// Kinds is a list of event kinds to filter by. If nil/empty, all kinds accepted.
	Kinds []uint16

	// Authors is a list of pubkeys to filter by. If nil/empty, all authors accepted.
	Authors [][32]byte

	// Since is the minimum created_at timestamp (inclusive). If 0, no minimum.
	Since uint32

	// Until is the maximum created_at timestamp (inclusive). If 0, no maximum.
	Until uint32

	// Limit is the maximum number of events to return. If 0, no limit.
	Limit int

	// Tags is a generic map for filtering by any tag type.
	// Key is the tag name (e.g., "e", "p", "t", "a", "d", etc.)
	// Value is a list of tag values to match (any match satisfies the filter).
	// Example: Tags["e"] = []string{"event_id_hex1", "event_id_hex2"}
	// This is the recommended way to query tags in new code.
	Tags map[string][]string

	// Search is a free-form search string. Filtering by search is optional and may not be supported.
	Search string
}

// AddTag adds tag values to the Tags map. Creates the map if nil.
// This is a convenience method for building filters programmatically.
func (f *QueryFilter) AddTag(tagName string, values ...string) {
	if f.Tags == nil {
		f.Tags = make(map[string][]string)
	}
	f.Tags[tagName] = append(f.Tags[tagName], values...)
}

// GetTag retrieves tag values from the Tags map.
// Returns nil if the tag is not present.
func (f *QueryFilter) GetTag(tagName string) []string {
	if f.Tags == nil {
		return nil
	}
	return f.Tags[tagName]
}

// SetETags sets "e" tag values from a legacy ETags byte array.
// Deprecated: Direct use of Tags["e"] is preferred.
func (f *QueryFilter) SetETags(etags [][32]byte) {
	if len(etags) == 0 {
		return
	}
	if f.Tags == nil {
		f.Tags = make(map[string][]string)
	}
	f.Tags["e"] = make([]string, len(etags))
	for i, etag := range etags {
		f.Tags["e"][i] = hex.EncodeToString(etag[:])
	}
}

// SetPTags sets "p" tag values from a legacy PTags byte array.
// Deprecated: Direct use of Tags["p"] is preferred.
func (f *QueryFilter) SetPTags(ptags [][32]byte) {
	if len(ptags) == 0 {
		return
	}
	if f.Tags == nil {
		f.Tags = make(map[string][]string)
	}
	f.Tags["p"] = make([]string, len(ptags))
	for i, ptag := range ptags {
		f.Tags["p"][i] = hex.EncodeToString(ptag[:])
	}
}

// SetHashtags sets "t" tag values from a legacy Hashtags string array.
// Deprecated: Direct use of Tags["t"] is preferred.
func (f *QueryFilter) SetHashtags(hashtags []string) {
	if len(hashtags) == 0 {
		return
	}
	if f.Tags == nil {
		f.Tags = make(map[string][]string)
	}
	f.Tags["t"] = append([]string{}, hashtags...)
}

// Timestamp is a convenience type alias for UNIX timestamps used in events.
type Timestamp = uint64

// Now returns the current time as a Timestamp (seconds since epoch).
func Now() Timestamp {
	return Timestamp(time.Now().Unix())
}
