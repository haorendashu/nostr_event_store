package client

import (
	"fmt"

	pb "github.com/haorendashu/nostr_event_store/protos"
	"github.com/haorendashu/nostr_event_store/src/types"
)

// ConvertEventToProto converts a Go Event to a protobuf Event.
func ConvertEventToProto(event *types.Event) *pb.Event {
	if event == nil {
		return nil
	}

	pbTags := make([]*pb.Tag, len(event.Tags))
	for i, tag := range event.Tags {
		values := make([]string, len(tag))
		copy(values, tag)
		pbTags[i] = &pb.Tag{Values: values}
	}

	return &pb.Event{
		Id:        event.ID[:],
		Pubkey:    event.Pubkey[:],
		CreatedAt: uint32(event.CreatedAt),
		Kind:      uint32(event.Kind),
		Tags:      pbTags,
		Content:   event.Content,
		Sig:       event.Sig[:],
	}
}

// ConvertEventFromProto converts a protobuf Event to a Go Event.
func ConvertEventFromProto(pbEvent *pb.Event) (*types.Event, error) {
	if pbEvent == nil {
		return nil, fmt.Errorf("event is nil")
	}

	if len(pbEvent.Id) != 32 {
		return nil, fmt.Errorf("invalid event ID length: %d", len(pbEvent.Id))
	}
	if len(pbEvent.Pubkey) != 32 {
		return nil, fmt.Errorf("invalid pubkey length: %d", len(pbEvent.Pubkey))
	}
	if len(pbEvent.Sig) != 64 {
		return nil, fmt.Errorf("invalid signature length: %d", len(pbEvent.Sig))
	}

	event := &types.Event{
		CreatedAt: uint32(pbEvent.CreatedAt),
		Kind:      uint16(pbEvent.Kind),
		Content:   pbEvent.Content,
	}

	copy(event.ID[:], pbEvent.Id)
	copy(event.Pubkey[:], pbEvent.Pubkey)
	copy(event.Sig[:], pbEvent.Sig)

	// Convert tags
	event.Tags = make([][]string, len(pbEvent.Tags))
	for i, pbTag := range pbEvent.Tags {
		tag := make([]string, len(pbTag.Values))
		copy(tag, pbTag.Values)
		event.Tags[i] = tag
	}

	return event, nil
}

// ConvertQueryFilterToProto converts a Go QueryFilter to a protobuf QueryFilter.
func ConvertQueryFilterToProto(filter *types.QueryFilter) *pb.QueryFilter {
	if filter == nil {
		return &pb.QueryFilter{}
	}

	// Convert kinds
	kinds := make([]uint32, len(filter.Kinds))
	for i, kind := range filter.Kinds {
		kinds[i] = uint32(kind)
	}

	// Convert authors
	authors := make([][]byte, len(filter.Authors))
	for i, author := range filter.Authors {
		authors[i] = author[:]
	}

	// Convert tags
	tags := make(map[string]*pb.TagFilter)
	for key, values := range filter.Tags {
		tagValues := make([]string, len(values))
		copy(tagValues, values)
		tags[key] = &pb.TagFilter{Values: tagValues}
	}

	pbFilter := &pb.QueryFilter{
		Authors: authors,
		Kinds:   kinds,
		Since:   filter.Since,
		Until:   filter.Until,
		Tags:    tags,
		Search:  filter.Search,
		Limit:   int32(filter.Limit),
	}

	return pbFilter
}

// ConvertQueryFilterFromProto converts a protobuf QueryFilter to a Go QueryFilter.
func ConvertQueryFilterFromProto(pbFilter *pb.QueryFilter) (*types.QueryFilter, error) {
	if pbFilter == nil {
		return &types.QueryFilter{}, nil
	}

	filter := &types.QueryFilter{
		Since:  pbFilter.Since,
		Until:  pbFilter.Until,
		Search: pbFilter.Search,
		Limit:  int(pbFilter.Limit),
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
				return nil, fmt.Errorf("invalid author length at index %d: expected 32, got %d", i, len(author))
			}
			copy(filter.Authors[i][:], author)
		}
	}

	// Convert tags
	if len(pbFilter.Tags) > 0 {
		filter.Tags = make(map[string][]string)
		for key, tagFilter := range pbFilter.Tags {
			values := make([]string, len(tagFilter.Values))
			copy(values, tagFilter.Values)
			filter.Tags[key] = values
		}
	}

	return filter, nil
}
