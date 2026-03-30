// Package remote provides gRPC server implementation for distributed EventStore access.
package remote

import (
	"context"
	"fmt"
	"log"
	"reflect"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	pb "github.com/haorendashu/nostr_event_store/protos"
	"github.com/haorendashu/nostr_event_store/src/query"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// EventStore interface defines the methods needed by the gRPC server.
// This local interface avoids circular imports with the eventstore package.
type EventStore interface {
	WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error)
	WriteEvents(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error)
	GetEvent(ctx context.Context, eventID [32]byte) (*types.Event, error)
	DeleteEvent(ctx context.Context, eventID [32]byte) error
	DeleteEvents(ctx context.Context, eventIDs [][32]byte) error
	Query(ctx context.Context, filter *types.QueryFilter) (query.ResultIterator, error)
	QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error)
	QueryAggregation(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error)
	Stats() interface{} // Returns eventstore.Stats
	Flush(ctx context.Context) error
	Close(ctx context.Context) error
}

// storeAdapter adapts an eventstore.EventStore to remote.EventStore interface.
type storeAdapter struct {
	store interface{} // Holds eventstore.EventStore
}

func (a *storeAdapter) WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error) {
	type writeEventMethod interface {
		WriteEvent(ctx context.Context, event *types.Event) (types.RecordLocation, error)
	}
	return a.store.(writeEventMethod).WriteEvent(ctx, event)
}

func (a *storeAdapter) WriteEvents(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error) {
	type writeEventsMethod interface {
		WriteEvents(ctx context.Context, events []*types.Event) ([]types.RecordLocation, error)
	}
	return a.store.(writeEventsMethod).WriteEvents(ctx, events)
}

func (a *storeAdapter) GetEvent(ctx context.Context, eventID [32]byte) (*types.Event, error) {
	type getEventMethod interface {
		GetEvent(ctx context.Context, eventID [32]byte) (*types.Event, error)
	}
	return a.store.(getEventMethod).GetEvent(ctx, eventID)
}

func (a *storeAdapter) DeleteEvent(ctx context.Context, eventID [32]byte) error {
	type deleteEventMethod interface {
		DeleteEvent(ctx context.Context, eventID [32]byte) error
	}
	return a.store.(deleteEventMethod).DeleteEvent(ctx, eventID)
}

func (a *storeAdapter) DeleteEvents(ctx context.Context, eventIDs [][32]byte) error {
	type deleteEventsMethod interface {
		DeleteEvents(ctx context.Context, eventIDs [][32]byte) error
	}
	return a.store.(deleteEventsMethod).DeleteEvents(ctx, eventIDs)
}

func (a *storeAdapter) Query(ctx context.Context, filter *types.QueryFilter) (query.ResultIterator, error) {
	type queryMethod interface {
		Query(ctx context.Context, filter *types.QueryFilter) (query.ResultIterator, error)
	}
	return a.store.(queryMethod).Query(ctx, filter)
}

func (a *storeAdapter) QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error) {
	type queryCountMethod interface {
		QueryCount(ctx context.Context, filter *types.QueryFilter) (int, error)
	}
	return a.store.(queryCountMethod).QueryCount(ctx, filter)
}

func (a *storeAdapter) QueryAggregation(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error) {
	type queryAggregationMethod interface {
		QueryAggregation(ctx context.Context, q *types.AggregationQuery) ([]types.AggregationEntry, error)
	}
	return a.store.(queryAggregationMethod).QueryAggregation(ctx, q)
}

func (a *storeAdapter) Stats() interface{} {
	type statsMethod interface {
		Stats() interface{}
	}
	// Try direct call, or wrap it
	if s, ok := a.store.(statsMethod); ok {
		return s.Stats()
	}
	// Fallback: use reflection to call Stats() with any return type
	type anyStatsMethod interface {
		Stats() interface{ any() }
	}
	// Use a generic approach
	v := reflect.ValueOf(a.store)
	m := v.MethodByName("Stats")
	if m.IsValid() {
		results := m.Call(nil)
		if len(results) > 0 {
			return results[0].Interface()
		}
	}
	return nil
}

func (a *storeAdapter) Flush(ctx context.Context) error {
	type flushMethod interface {
		Flush(ctx context.Context) error
	}
	return a.store.(flushMethod).Flush(ctx)
}

func (a *storeAdapter) Close(ctx context.Context) error {
	type closeMethod interface {
		Close(ctx context.Context) error
	}
	return a.store.(closeMethod).Close(ctx)
}

// Server implements the EventStoreServer interface using a local EventStore.
type Server struct {
	pb.UnimplementedEventStoreServer
	store       EventStore
	apiKey      string // Single API key for authentication
	requireAuth bool   // Whether to require API key authentication
}

// NewServer creates a new gRPC EventStore server.
func NewServer(store interface{}, apiKey string) *Server {
	return &Server{
		store:       &storeAdapter{store: store},
		apiKey:      apiKey,
		requireAuth: apiKey != "",
	}
}

// validateAPIKey checks the API key from request metadata.
func (s *Server) validateAPIKey(ctx context.Context) error {
	if !s.requireAuth {
		return nil
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}

	auth := md.Get("authorization")
	if len(auth) == 0 {
		return status.Error(codes.Unauthenticated, "missing authorization header")
	}

	// Expect format: "Bearer <api-key>"
	if len(auth[0]) < 7 || auth[0][:7] != "Bearer " {
		return status.Error(codes.Unauthenticated, "invalid authorization format")
	}

	apiKey := auth[0][7:]
	if apiKey != s.apiKey {
		return status.Error(codes.PermissionDenied, "invalid API key")
	}

	return nil
}

// WriteEvent writes a single event.
func (s *Server) WriteEvent(ctx context.Context, req *pb.WriteEventRequest) (*pb.WriteEventResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	// Convert protobuf Event to Go Event
	event, err := ConvertEventFromProto(req.Event)
	if err != nil {
		return &pb.WriteEventResponse{
			Result: &pb.WriteEventResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    "INVALID_ARGUMENT",
					Message: err.Error(),
				},
			},
		}, nil
	}

	// Write to store
	loc, err := s.store.WriteEvent(ctx, event)
	if err != nil {
		log.Printf("WriteEvent failed: %v", err)
		return &pb.WriteEventResponse{
			Result: &pb.WriteEventResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    convertErrorToCode(err),
					Message: err.Error(),
				},
			},
		}, nil
	}

	return &pb.WriteEventResponse{
		Result: &pb.WriteEventResponse_Location{
			Location: &pb.RecordLocation{
				SegmentId: uint32(loc.SegmentID),
				Offset:    uint32(loc.Offset),
			},
		},
	}, nil
}

// WriteEvents writes multiple events in batch.
func (s *Server) WriteEvents(ctx context.Context, req *pb.WriteEventsRequest) (*pb.WriteEventsResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	if len(req.Events) == 0 {
		return &pb.WriteEventsResponse{
			Result: &pb.WriteEventsResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    "INVALID_ARGUMENT",
					Message: "no events provided",
				},
			},
		}, nil
	}

	// Convert protobuf Events to Go Events
	events := make([]*types.Event, len(req.Events))
	for i, pbEvent := range req.Events {
		event, err := ConvertEventFromProto(pbEvent)
		if err != nil {
			return &pb.WriteEventsResponse{
				Result: &pb.WriteEventsResponse_Error{
					Error: &pb.ErrorResponse{
						Code:    "INVALID_ARGUMENT",
						Message: fmt.Sprintf("invalid event at index %d: %v", i, err),
					},
				},
			}, nil
		}
		events[i] = event
	}

	// Write batch to store
	locs, err := s.store.WriteEvents(ctx, events)
	if err != nil {
		log.Printf("WriteEvents failed: %v", err)
		return &pb.WriteEventsResponse{
			Result: &pb.WriteEventsResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    convertErrorToCode(err),
					Message: err.Error(),
				},
			},
		}, nil
	}

	// Convert locations to protobuf
	pbLocs := make([]*pb.RecordLocation, len(locs))
	for i, loc := range locs {
		pbLocs[i] = &pb.RecordLocation{
			SegmentId: uint32(loc.SegmentID),
			Offset:    uint32(loc.Offset),
		}
	}

	return &pb.WriteEventsResponse{
		Result: &pb.WriteEventsResponse_Success{
			Success: &pb.WriteEventsResult{
				Locations: pbLocs,
			},
		},
	}, nil
}

// GetEvent retrieves an event by ID.
func (s *Server) GetEvent(ctx context.Context, req *pb.GetEventRequest) (*pb.GetEventResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	if len(req.EventId) != 32 {
		return &pb.GetEventResponse{
			Result: &pb.GetEventResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    "INVALID_ARGUMENT",
					Message: "invalid event ID length",
				},
			},
		}, nil
	}

	var eventID [32]byte
	copy(eventID[:], req.EventId)

	// Get from store
	event, err := s.store.GetEvent(ctx, eventID)
	if err != nil {
		log.Printf("GetEvent failed: %v", err)
		return &pb.GetEventResponse{
			Result: &pb.GetEventResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    convertErrorToCode(err),
					Message: err.Error(),
				},
			},
		}, nil
	}

	// Convert to protobuf
	pbEvent := ConvertEventToProto(event)

	return &pb.GetEventResponse{
		Result: &pb.GetEventResponse_Event{
			Event: pbEvent,
		},
	}, nil
}

// DeleteEvent deletes a single event by ID.
func (s *Server) DeleteEvent(ctx context.Context, req *pb.DeleteEventRequest) (*pb.DeleteEventResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	if len(req.EventId) != 32 {
		return &pb.DeleteEventResponse{
			Result: &pb.DeleteEventResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    "INVALID_ARGUMENT",
					Message: "invalid event ID length",
				},
			},
		}, nil
	}

	var eventID [32]byte
	copy(eventID[:], req.EventId)

	// Delete from store
	err := s.store.DeleteEvent(ctx, eventID)
	if err != nil {
		log.Printf("DeleteEvent failed: %v", err)
		return &pb.DeleteEventResponse{
			Result: &pb.DeleteEventResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    convertErrorToCode(err),
					Message: err.Error(),
				},
			},
		}, nil
	}

	return &pb.DeleteEventResponse{
		Result: &pb.DeleteEventResponse_Success{
			Success: &pb.DeleteEventResult{Deleted: true},
		},
	}, nil
}

// DeleteEvents deletes multiple events by ID.
func (s *Server) DeleteEvents(ctx context.Context, req *pb.DeleteEventsRequest) (*pb.DeleteEventsResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	if len(req.EventIds) == 0 {
		return &pb.DeleteEventsResponse{
			Result: &pb.DeleteEventsResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    "INVALID_ARGUMENT",
					Message: "no event IDs provided",
				},
			},
		}, nil
	}

	// Convert protobuf IDs to Go format
	eventIDs := make([][32]byte, len(req.EventIds))
	for i, id := range req.EventIds {
		if len(id) != 32 {
			return &pb.DeleteEventsResponse{
				Result: &pb.DeleteEventsResponse_Error{
					Error: &pb.ErrorResponse{
						Code:    "INVALID_ARGUMENT",
						Message: fmt.Sprintf("invalid event ID length at index %d", i),
					},
				},
			}, nil
		}
		copy(eventIDs[i][:], id)
	}

	// Delete batch from store
	err := s.store.DeleteEvents(ctx, eventIDs)
	if err != nil {
		log.Printf("DeleteEvents failed: %v", err)
		return &pb.DeleteEventsResponse{
			Result: &pb.DeleteEventsResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    convertErrorToCode(err),
					Message: err.Error(),
				},
			},
		}, nil
	}

	return &pb.DeleteEventsResponse{
		Result: &pb.DeleteEventsResponse_Success{
			Success: &pb.DeleteEventsResult{DeletedCount: int32(len(eventIDs))},
		},
	}, nil
}

// Query returns events matching filters as a stream.
func (s *Server) Query(req *pb.QueryRequest, stream grpc.ServerStreamingServer[pb.QueryResponse]) error {
	ctx := stream.Context()

	if err := s.validateAPIKey(ctx); err != nil {
		return err
	}

	// Convert protobuf QueryFilter to Go QueryFilter
	filter, err := ConvertQueryFilterFromProto(req.Filter)
	if err != nil {
		return status.Error(codes.InvalidArgument, err.Error())
	}

	// Query from store - returns a lazy iterator; events are read on demand.
	iter, err := s.store.Query(ctx, filter)
	if err != nil {
		log.Printf("Query failed: %v", err)
		return convertErrorToGRPC(err)
	}
	defer iter.Close()

	// Stream events to client one by one; context cancellation stops iteration promptly.
	for iter.Valid() {
		if err := ctx.Err(); err != nil {
			return convertErrorToGRPC(err)
		}
		pbEvent := ConvertEventToProto(iter.Event())
		if err := stream.Send(&pb.QueryResponse{
			Result: &pb.QueryResponse_Event{
				Event: pbEvent,
			},
		}); err != nil {
			log.Printf("stream send failed: %v", err)
			return status.Error(codes.Internal, "error sending response")
		}
		if err := iter.Next(ctx); err != nil {
			log.Printf("Query iterator error: %v", err)
			return convertErrorToGRPC(err)
		}
	}

	return nil
}

// QueryCount returns the count of events matching filters.
func (s *Server) QueryCount(ctx context.Context, req *pb.QueryCountRequest) (*pb.QueryCountResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	// Convert protobuf QueryFilter to Go QueryFilter
	filter, err := ConvertQueryFilterFromProto(req.Filter)
	if err != nil {
		return &pb.QueryCountResponse{
			Result: &pb.QueryCountResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    "INVALID_ARGUMENT",
					Message: err.Error(),
				},
			},
		}, nil
	}

	// Get count from store
	count, err := s.store.QueryCount(ctx, filter)
	if err != nil {
		log.Printf("QueryCount failed: %v", err)
		return &pb.QueryCountResponse{
			Result: &pb.QueryCountResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    convertErrorToCode(err),
					Message: err.Error(),
				},
			},
		}, nil
	}

	return &pb.QueryCountResponse{
		Result: &pb.QueryCountResponse_Success{
			Success: &pb.QueryCountResult{Count: int64(count)},
		},
	}, nil
}

// Stats returns storage statistics.
func (s *Server) Stats(ctx context.Context, req *pb.StatsRequest) (*pb.StatsResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	// Get stats from store
	statsObj := s.store.Stats()

	// Extract field values using reflection to avoid circular imports
	var eventCount, totalSize, indexSize, walSize uint64

	if statsObj != nil {
		v := reflect.ValueOf(statsObj)
		if v.Kind() == reflect.Struct {
			if fv := v.FieldByName("TotalEvents"); fv.IsValid() {
				eventCount = fv.Uint()
			}
			if fv := v.FieldByName("TotalDataSizeBytes"); fv.IsValid() {
				totalSize = fv.Uint()
			}
			if fv := v.FieldByName("TotalIndexSizeBytes"); fv.IsValid() {
				indexSize = fv.Uint()
			}
			if fv := v.FieldByName("TotalWALSizeBytes"); fv.IsValid() {
				walSize = fv.Uint()
			}
		}
	}

	return &pb.StatsResponse{
		Result: &pb.StatsResponse_Stats{
			Stats: &pb.StorageStats{
				EventCount:   eventCount,
				TotalSize:    totalSize,
				SegmentCount: uint64(0), // TODO: get actual segment count
				IndexSize:    indexSize,
				WalSize:      walSize,
			},
		},
	}, nil
}

// Flush flushes pending writes to disk.
func (s *Server) Flush(ctx context.Context, req *pb.FlushRequest) (*pb.FlushResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	// Flush store
	err := s.store.Flush(ctx)
	if err != nil {
		return &pb.FlushResponse{
			Result: &pb.FlushResponse_Error{
				Error: &pb.ErrorResponse{
					Code:    convertErrorToCode(err),
					Message: err.Error(),
				},
			},
		}, nil
	}

	return &pb.FlushResponse{
		Result: &pb.FlushResponse_Success{
			Success: &pb.FlushResult{Flushed: true},
		},
	}, nil
}

// HealthCheck performs a health check on the EventStore.
func (s *Server) HealthCheck(ctx context.Context, req *pb.HealthCheckRequest) (*pb.HealthCheckResponse, error) {
	// Health check doesn't require authentication (allows monitoring without API key)

	return &pb.HealthCheckResponse{
		Healthy: true,
		Status:  "operational",
	}, nil
}

// QueryAggregation aggregates events by one or more dimensions using index-key-only scans.
func (s *Server) QueryAggregation(ctx context.Context, req *pb.QueryAggregationRequest) (*pb.QueryAggregationResponse, error) {
	if err := s.validateAPIKey(ctx); err != nil {
		return nil, err
	}

	q, err := ConvertAggregationQueryFromProto(req)
	if err != nil {
		return &pb.QueryAggregationResponse{
			Result: &pb.QueryAggregationResponse_Error{
				Error: &pb.ErrorResponse{Code: "INVALID_ARGUMENT", Message: err.Error()},
			},
		}, nil
	}

	entries, err := s.store.QueryAggregation(ctx, q)
	if err != nil {
		log.Printf("QueryAggregation failed: %v", err)
		return &pb.QueryAggregationResponse{
			Result: &pb.QueryAggregationResponse_Error{
				Error: &pb.ErrorResponse{Code: convertErrorToCode(err), Message: err.Error()},
			},
		}, nil
	}

	return &pb.QueryAggregationResponse{
		Result: &pb.QueryAggregationResponse_Success{
			Success: &pb.QueryAggregationResult{
				Entries: ConvertAggregationEntriesToProto(entries),
			},
		},
	}, nil
}

// convertErrorToCode converts Go errors to error codes for protobuf responses.
func convertErrorToCode(err error) string {
	if err == nil {
		return "OK"
	}

	errStr := err.Error()

	// Check for specific error patterns
	switch {
	case errStr == "event not found" || errStr == "ErrEventNotFound":
		return "NOT_FOUND"
	case errStr == "invalid event" || errStr == "ErrInvalidEvent":
		return "INVALID_ARGUMENT"
	case errStr == "storage error" || errStr == "ErrStorageError":
		return "INTERNAL"
	case errStr == "index error" || errStr == "ErrIndexError":
		return "INTERNAL"
	case errStr == "query error" || errStr == "ErrQueryError":
		return "INTERNAL"
	default:
		return "INTERNAL"
	}
}

// convertErrorToGRPC converts Go errors to gRPC status errors.
func convertErrorToGRPC(err error) error {
	if err == nil {
		return nil
	}

	errStr := err.Error()

	// Check for specific error patterns
	switch {
	case errStr == "event not found" || errStr == "ErrEventNotFound":
		return status.Error(codes.NotFound, "event not found")
	case errStr == "invalid event" || errStr == "ErrInvalidEvent":
		return status.Error(codes.InvalidArgument, "invalid event")
	case errStr == "storage error" || errStr == "ErrStorageError":
		return status.Error(codes.Internal, "storage error")
	case errStr == "index error" || errStr == "ErrIndexError":
		return status.Error(codes.Internal, "index error")
	case errStr == "query error" || errStr == "ErrQueryError":
		return status.Error(codes.Internal, "query error")
	default:
		return status.Error(codes.Internal, err.Error())
	}
}
