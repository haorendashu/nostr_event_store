package query

import (
	"bytes"
	"strings"

	"nostr_event_store/src/types"
)

// MatchesFilter checks if an event matches the given filter.
func MatchesFilter(event *types.Event, filter *types.QueryFilter) bool {
	// Check kind
	if len(filter.Kinds) > 0 && !containsUint32(filter.Kinds, event.Kind) {
		return false
	}

	// Check author
	if len(filter.Authors) > 0 && !containsBytes32(filter.Authors, event.Pubkey) {
		return false
	}

	// Check timestamp range
	if filter.Since > 0 && event.CreatedAt < filter.Since {
		return false
	}
	if filter.Until > 0 && event.CreatedAt > filter.Until {
		return false
	}

	// Check e tags (event IDs)
	if len(filter.ETags) > 0 && !hasETags(event, filter.ETags) {
		return false
	}

	// Check p tags (pubkeys)
	if len(filter.PTags) > 0 && !hasPTags(event, filter.PTags) {
		return false
	}

	// Check hashtags (t tags)
	if len(filter.Hashtags) > 0 && !hasHashtags(event, filter.Hashtags) {
		return false
	}

	// Check search (simple substring match)
	if filter.Search != "" && !matchesSearch(event, filter.Search) {
		return false
	}

	return true
}

// containsUint32 checks if slice contains value.
func containsUint32(slice []uint32, value uint32) bool {
	for _, v := range slice {
		if v == value {
			return true
		}
	}
	return false
}

// containsBytes32 checks if slice contains value.
func containsBytes32(slice [][32]byte, value [32]byte) bool {
	for _, v := range slice {
		if v == value {
			return true
		}
	}
	return false
}

// hasETags checks if event has any of the specified event ID tags.
func hasETags(event *types.Event, etagIDs [][32]byte) bool {
	if len(event.Tags) == 0 {
		return false
	}
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == "e" {
			// Parse tag value as event ID (hex string to [32]byte)
			id := parseEventID(tag[1])
			if id != nil && containsBytes32(etagIDs, *id) {
				return true
			}
		}
	}
	return false
}

// hasPTags checks if event has any of the specified pubkey tags.
func hasPTags(event *types.Event, pubkeys [][32]byte) bool {
	if len(event.Tags) == 0 {
		return false
	}
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == "p" {
			// Parse tag value as pubkey (hex string to [32]byte)
			pubkey := parsePubkey(tag[1])
			if pubkey != nil && containsBytes32(pubkeys, *pubkey) {
				return true
			}
		}
	}
	return false
}

// hasHashtags checks if event has any of the specified hashtags.
func hasHashtags(event *types.Event, hashtags []string) bool {
	if len(event.Tags) == 0 {
		return false
	}
	for _, tag := range event.Tags {
		if len(tag) > 1 && tag[0] == "t" {
			if containsString(hashtags, strings.ToLower(tag[1])) {
				return true
			}
		}
	}
	return false
}

// matchesSearch checks if event matches search string (simple substring match).
func matchesSearch(event *types.Event, search string) bool {
	searchLower := strings.ToLower(search)
	
	// Search in content
	if strings.Contains(strings.ToLower(event.Content), searchLower) {
		return true
	}
	
	// Search in tag values
	for _, tag := range event.Tags {
		if len(tag) < 2 {
			continue
		}
		for _, val := range tag[1:] {
			if strings.Contains(strings.ToLower(val), searchLower) {
				return true
			}
		}
	}
	
	return false
}

// containsString checks if string slice contains value.
func containsString(slice []string, value string) bool {
	valueLower := strings.ToLower(value)
	for _, v := range slice {
		if strings.ToLower(v) == valueLower {
			return true
		}
	}
	return false
}

// parseEventID parses a hex string to [32]byte event ID.
// Returns nil if parsing fails.
func parseEventID(s string) *[32]byte {
	if len(s) != 64 {
		return nil
	}
	var id [32]byte
	for i := 0; i < 32; i++ {
		var b byte
		if s[i*2] >= '0' && s[i*2] <= '9' {
			b = s[i*2] - '0'
		} else if s[i*2] >= 'a' && s[i*2] <= 'f' {
			b = s[i*2] - 'a' + 10
		} else if s[i*2] >= 'A' && s[i*2] <= 'F' {
			b = s[i*2] - 'A' + 10
		} else {
			return nil
		}
		b = b << 4

		if s[i*2+1] >= '0' && s[i*2+1] <= '9' {
			b |= s[i*2+1] - '0'
		} else if s[i*2+1] >= 'a' && s[i*2+1] <= 'f' {
			b |= s[i*2+1] - 'a' + 10
		} else if s[i*2+1] >= 'A' && s[i*2+1] <= 'F' {
			b |= s[i*2+1] - 'A' + 10
		} else {
			return nil
		}
		id[i] = b
	}
	return &id
}

// parsePubkey parses a hex string to [32]byte pubkey.
// Returns nil if parsing fails.
func parsePubkey(s string) *[32]byte {
	if len(s) != 64 {
		return nil
	}
	var pubkey [32]byte
	for i := 0; i < 32; i++ {
		var b byte
		if s[i*2] >= '0' && s[i*2] <= '9' {
			b = s[i*2] - '0'
		} else if s[i*2] >= 'a' && s[i*2] <= 'f' {
			b = s[i*2] - 'a' + 10
		} else if s[i*2] >= 'A' && s[i*2] <= 'F' {
			b = s[i*2] - 'A' + 10
		} else {
			return nil
		}
		b = b << 4

		if s[i*2+1] >= '0' && s[i*2+1] <= '9' {
			b |= s[i*2+1] - '0'
		} else if s[i*2+1] >= 'a' && s[i*2+1] <= 'f' {
			b |= s[i*2+1] - 'a' + 10
		} else if s[i*2+1] >= 'A' && s[i*2+1] <= 'F' {
			b |= s[i*2+1] - 'A' + 10
		} else {
			return nil
		}
		pubkey[i] = b
	}
	return &pubkey
}

// eventIDToString converts [32]byte to hex string.
func eventIDToString(id [32]byte) string {
	return bytesToHex(id[:])
}

// pubkeyToString converts [32]byte to hex string.
func pubkeyToString(pk [32]byte) string {
	return bytesToHex(pk[:])
}

// bytesToHex converts bytes to hex string.
func bytesToHex(b []byte) string {
	var buf bytes.Buffer
	for _, v := range b {
		buf.WriteString("0123456789abcdef"[v>>4 : v>>4+1])
		buf.WriteString("0123456789abcdef"[v&0xf : v&0xf+1])
	}
	return buf.String()
}
