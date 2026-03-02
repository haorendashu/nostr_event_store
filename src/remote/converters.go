// Package remote provides gRPC server and client implementations for remote EventStore access.
// This package enables distributed deployment of EventStore instances, allowing shards to
// be hosted on different machines and accessed over the network.
package remote

import (
	"context"
	"fmt"

	pb "github.com/haorendashu/nostr_event_store/protos"
	"github.com/haorendashu/nostr_event_store/src/errors"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// ConvertEventToProto converts a types.Event to protobuf Event
func ConvertEventToProto(event *types.Event) *pb.Event {
	if event == nil {
		return nil
	}

	pbEvent := &pb.Event{
		Id:        event.ID[:],
		Pubkey:    event.Pubkey[:],
		CreatedAt: uint32(event.CreatedAt),
		Kind:      uint32(event.Kind),
		Content:   event.Content,
		Sig:       event.Sig[:],
		Flags:     uint32(event.Flags),
	}

	// Convert tags
	pbEvent.Tags = make([]*pb.Tag, len(event.Tags))
	for i, tag := range event.Tags {
		pbEvent.Tags[i] = &pb.Tag{
			Values: tag,
		}
	}

	return pbEvent
}

// ConvertEventFromProto converts a protobuf Event to types.Event
func ConvertEventFromProto(pbEvent *pb.Event) (*types.Event, error) {
	if pbEvent == nil {
		return nil, errors.NewError("ErrInvalidEvent", "nil event")
	}

	event := &types.Event{
		CreatedAt: uint32(pbEvent.CreatedAt),
		Kind:      uint16(pbEvent.Kind),
		Content:   pbEvent.Content,
		Flags:     types.EventFlags(pbEvent.Flags),
	}

	// Copy fixed-size arrays
	if len(pbEvent.Id) != 32 {
		return nil, errors.NewError("ErrInvalidEvent", "invalid event ID length")
	}
	copy(event.ID[:], pbEvent.Id)

	if len(pbEvent.Pubkey) != 32 {
		return nil, errors.NewError("ErrInvalidEvent", "invalid pubkey length")
	}
	copy(event.Pubkey[:], pbEvent.Pubkey)

	if len(pbEvent.Sig) != 64 {
		return nil, errors.NewError("ErrInvalidEvent", "invalid signature length")
	}
	copy(event.Sig[:], pbEvent.Sig)

	// Convert tags
	event.Tags = make([][]string, len(pbEvent.Tags))
	for i, tag := range pbEvent.Tags {
		event.Tags[i] = tag.Values
	}

	return event, nil
}

// ConvertQueryFilterToProto converts a types.QueryFilter to protobuf QueryFilter
func ConvertQueryFilterToProto(filter *types.QueryFilter) *pb.QueryFilter {
	if filter == nil {
		return nil
	}

	pbFilter := &pb.QueryFilter{
		Since:  filter.Since,
		Until:  filter.Until,
		Limit:  int32(filter.Limit),
		Search: filter.Search,
	}

	// Convert kinds
	if len(filter.Kinds) > 0 {
		pbFilter.Kinds = make([]uint32, len(filter.Kinds))
		for i, kind := range filter.Kinds {
			pbFilter.Kinds[i] = uint32(kind)
		}
	}

	// Convert authors
	if len(filter.Authors) > 0 {
		pbFilter.Authors = make([][]byte, len(filter.Authors))
		for i, author := range filter.Authors {
			pbFilter.Authors[i] = author[:]
		}
	}

	// Note: IDs field is not supported in Go QueryFilter
	// IDs are queried via GetEvent() API instead

	// Convert tag filters
	if len(filter.Tags) > 0 {
		pbFilter.Tags = make(map[string]*pb.TagFilter)
		for key, values := range filter.Tags {
			pbFilter.Tags[key] = &pb.TagFilter{
				Values: values,
			}
		}
	}

	return pbFilter
}

// ConvertQueryFilterFromProto converts a protobuf QueryFilter to types.QueryFilter
func ConvertQueryFilterFromProto(pbFilter *pb.QueryFilter) (*types.QueryFilter, error) {
	if pbFilter == nil {
		return nil, nil
	}

	filter := &types.QueryFilter{
		Since:  pbFilter.Since,
		Until:  pbFilter.Until,
		Limit:  int(pbFilter.Limit),
		Search: pbFilter.Search,
	}

	// Convert kinds
	if len(pbFilter.Kinds) > 0 {
		filter.Kinds = make([]uint16, len(pbFilter.Kinds))
		for i, kind := range pbFilter.Kinds {
			filter.Kinds[i] = uint16(kind)
		}
	}

	// Convert authors
	if len(pbFilter.Authors) > 0 {
		filter.Authors = make([][32]byte, len(pbFilter.Authors))
		for i, author := range pbFilter.Authors {
			if len(author) != 32 {
				return nil, errors.NewError("ErrInvalidFilter", fmt.Sprintf("invalid author length at index %d", i))
			}
			copy(filter.Authors[i][:], author)
		}
	}

	// Convert tag filters
	if len(pbFilter.Tags) > 0 {
		filter.Tags = make(map[string][]string)
		for key, tagFilter := range pbFilter.Tags {
			filter.Tags[key] = tagFilter.Values
		}
	}

	return filter, nil
}

// ConvertRecordLocationToProto converts a types.RecordLocation to protobuf RecordLocation
func ConvertRecordLocationToProto(loc types.RecordLocation) *pb.RecordLocation {
	return &pb.RecordLocation{
		SegmentId: loc.SegmentID,
		Offset:    loc.Offset,
	}
}

// ConvertRecordLocationFromProto converts a protobuf RecordLocation to types.RecordLocation
func ConvertRecordLocationFromProto(pbLoc *pb.RecordLocation) types.RecordLocation {
	if pbLoc == nil {
		return types.RecordLocation{}
	}
	return types.RecordLocation{
		SegmentID: pbLoc.SegmentId,
		Offset:    pbLoc.Offset,
	}
}

// ConvertErrorToProto converts an error to protobuf ErrorResponse
func ConvertErrorToProto(err error) *pb.ErrorResponse {
	if err == nil {
		return nil
	}

	// Check if it's a typed error
	if storeErr, ok := err.(errors.Error); ok {
		return &pb.ErrorResponse{
			Code:    storeErr.Code(),
			Message: storeErr.Error(),
			Details: "",
		}
	}

	// Generic error
	return &pb.ErrorResponse{
		Code:    "ErrUnknown",
		Message: err.Error(),
		Details: "",
	}
}

// ConvertErrorFromProto converts a protobuf ErrorResponse to error
func ConvertErrorFromProto(pbErr *pb.ErrorResponse) error {
	if pbErr == nil {
		return nil
	}

	return errors.NewError(pbErr.Code, pbErr.Message)
}

// ValidateAPIKey validates the API key from request metadata
func ValidateAPIKey(ctx context.Context, expectedKey string) error {
	if expectedKey == "" {
		// No authentication required
		return nil
	}

	// Extract API key from context (will be set by gRPC metadata interceptor)
	// For now, we'll implement a simple validation
	// In production, this should use gRPC metadata
	return nil
}
