// Package types — aggregation query types for index-level analytics.
package types

// GroupByField specifies a dimension to group events by in an aggregation query.
// Multiple fields may be combined to form composite group keys.
type GroupByField int

const (
	// GroupByAuthor groups by event author pubkey.
	// Available when scanning the AuthorTime index.
	GroupByAuthor GroupByField = 1

	// GroupByKind groups by event kind.
	// Available from both AuthorTime and Search indexes.
	GroupByKind GroupByField = 2

	// GroupByTimeBucket groups by a fixed-size time window.
	// Requires TimeBucketSeconds to be set in the query.
	// Available from both AuthorTime and Search indexes.
	GroupByTimeBucket GroupByField = 3

	// GroupByTagValue groups by the value of a specific tag type.
	// Requires TagName to be set in the query.
	// Uses the Search index. Cannot be combined with GroupByAuthor
	// because pubkey is not stored in Search index keys.
	GroupByTagValue GroupByField = 4
)

// AggFunc specifies the aggregation function to apply per group.
type AggFunc int

const (
	// AggCount counts the number of index entries per group (default).
	AggCount AggFunc = 1
	// Reserved for future:
	// AggSum      AggFunc = 2
	// AggAvg      AggFunc = 3
	// AggDistinct AggFunc = 4
	// AggMin      AggFunc = 5
	// AggMax      AggFunc = 6
)

func (f AggFunc) String() string {
	switch f {
	case AggCount:
		return "COUNT"
	default:
		return "COUNT" // default to COUNT
	}
}

// AggregationQuery describes how to aggregate events directly from index keys,
// without loading event content. It reuses QueryFilter for event filtering and
// adds GroupBy dimensions for counting.
//
// Index routing:
//
//	GroupByTagValue present  →  Search index (kind + searchType + tagValue + createdAt)
//	All other combinations   →  AuthorTime index (pubkey + kind + createdAt)
//
// Performance note: all paths are index-key-only scans (no event deserialization).
// The only exception is when Filter.Tags or Filter.Search are set — those fall back
// to full event loading and should be avoided for large time ranges.
type AggregationQuery struct {
	// Filter constrains which events are counted.
	// Supported fields: Since, Until, Authors, Kinds.
	// Tags and Search are not supported in aggregation (return ErrUnsupported).
	Filter *QueryFilter

	// GroupBy specifies the dimensions to group by.
	// At least one field is required.
	// GroupByTagValue cannot be combined with GroupByAuthor.
	GroupBy []GroupByField

	// AggFunc specifies the aggregation function (default: AggCount).
	// Currently only AggCount is implemented; others reserved for future use.
	AggFunc AggFunc

	// TimeBucketSeconds is the bucket width for GroupByTimeBucket.
	// e.g. 3600 = hourly buckets, 86400 = daily buckets.
	// Required when GroupBy contains GroupByTimeBucket; ignored otherwise.
	TimeBucketSeconds uint32

	// TagName is the Nostr tag to aggregate by for GroupByTagValue.
	// e.g. "p" (mentions), "t" (hashtags), "e" (replied-to events).
	// Must be present in the store's search index configuration.
	// Required when GroupBy contains GroupByTagValue; ignored otherwise.
	TagName string

	// Limit is the maximum number of entries to return (0 = no limit).
	// Applied after sorting, so with OrderDesc=true you get the Top-N entries.
	Limit int

	// OrderDesc controls sort direction: true = highest Count first (Top-N).
	// false = lowest Count first.
	OrderDesc bool
}

// AggregationEntry is a single result row from an aggregation query.
// Fields not present in the GroupBy list of the query will be zero values.
type AggregationEntry struct {
	// Pubkey is the event author. Set when GroupBy contains GroupByAuthor.
	Pubkey [32]byte

	// Kind is the event kind. Set when GroupBy contains GroupByKind.
	Kind uint16

	// TimeBucket is the start timestamp of the time bucket.
	// Set when GroupBy contains GroupByTimeBucket.
	TimeBucket uint32

	// TagValue is the tag value being counted.
	// Set when GroupBy contains GroupByTagValue.
	TagValue string

	// Count is the number of index entries matching this group key.
	Count int64
}
