package remote

import (
	"context"
	"testing"

	pb "github.com/haorendashu/nostr_event_store/protos"
	"github.com/haorendashu/nostr_event_store/src/query"
	"github.com/haorendashu/nostr_event_store/src/types"
)

type mockStatsStore struct{}

type mockStatsPayload struct {
	TotalEvents         uint64
	TotalDataSizeBytes  uint64
	SegmentCount        uint64
	TotalIndexSizeBytes uint64
	TotalWALSizeBytes   uint64
}

func (m *mockStatsStore) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	return types.RecordLocation{}, nil
}

func (m *mockStatsStore) WriteEvents(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error) {
	return nil, nil
}

func (m *mockStatsStore) GetEvent(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	return nil, nil
}

func (m *mockStatsStore) DeleteEvent(ctx context.Context, eventID [32]byte) error {
	return nil
}

func (m *mockStatsStore) DeleteEvents(ctx context.Context, eventIDs [][32]byte) error {
	return nil
}

func (m *mockStatsStore) DeleteByFilter(ctx context.Context, filter *types.QueryFilter) (int, error) {
	return 0, nil
}

func (m *mockStatsStore) Query(ctx context.Context, filter *types.QueryFilter) (query.ResultIterator, error) {
	return nil, nil
}

func (m *mockStatsStore) QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error) {
	return 0, nil
}

func (m *mockStatsStore) QueryAggregation(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error) {
	return nil, nil
}

func (m *mockStatsStore) Stats() interface{} {
	return mockStatsPayload{
		TotalEvents:         123,
		TotalDataSizeBytes:  456,
		SegmentCount:        7,
		TotalIndexSizeBytes: 89,
		TotalWALSizeBytes:   10,
	}
}

func (m *mockStatsStore) Flush(ctx context.Context) error {
	return nil
}

func (m *mockStatsStore) Close(ctx context.Context) error {
	return nil
}

func TestServerStatsIncludesSegmentCount(t *testing.T) {
	s := &Server{
		store:       &mockStatsStore{},
		requireAuth: false,
	}

	resp, err := s.Stats(context.Background(), &pb.StatsRequest{})
	if err != nil {
		t.Fatalf("Stats() failed: %v", err)
	}

	stats := resp.GetStats()
	if stats == nil {
		t.Fatal("expected stats payload, got nil")
	}

	if stats.SegmentCount != 7 {
		t.Fatalf("expected segment_count=7, got %d", stats.SegmentCount)
	}
}
