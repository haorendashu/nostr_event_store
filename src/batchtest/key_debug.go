package main

import (
	"fmt"

	"nostr_event_store/src/index"
)

// KeyDebug tests key building for e-tags and other tags
func KeyDebug() {
	fmt.Println("=== Key Building Debug ===")

	// Create a KeyBuilder with default mappings
	kb := index.NewKeyBuilder(index.DefaultSearchTypeCodes())

	testCases := []struct {
		name  string
		kind  uint32
		tag   string
		value string
	}{
		{
			name:  "i-tag",
			kind:  1,
			tag:   "i",
			value: "url https://blossom.primal.net/b9bfc34b5293f6308b53881db1c84cd804f97865aa4d40913988e051277833df.jpg",
		},
		{
			name:  "e-tag",
			kind:  10003,
			tag:   "e",
			value: "28587da287bb2b61335dab7bad2adaf6b975acfb128f4cf078c3bcf26d73c2ce",
		},
		{
			name:  "a-tag",
			kind:  10000,
			tag:   "a",
			value: "Mute List",
		},
	}

	for _, tc := range testCases {
		fmt.Printf("\n=== %s ===\n", tc.name)
		fmt.Printf("Tag: %s=%q (len=%d)\n", tc.tag, tc.value, len(tc.value))

		// Get the search type code
		searchTypeCodes := kb.TagNameToSearchTypeCode()
		searchType, ok := searchTypeCodes[tc.tag]
		if !ok {
			fmt.Printf("  ERROR: Tag %s not in mapping\n", tc.tag)
			continue
		}
		fmt.Printf("SearchType code: %d\n", searchType)

		// Build search key for insert (with actual createdAt=1000000)
		insertKey := kb.BuildSearchKey(tc.kind, searchType, []byte(tc.value), 1000000)
		fmt.Printf("Insert key: %x (length:%d)\n", insertKey, len(insertKey))

		// Print key breakdown
		kind := uint32(insertKey[0])<<24 | uint32(insertKey[1])<<16 | uint32(insertKey[2])<<8 | uint32(insertKey[3])
		stype := insertKey[4]
		tagLen := insertKey[5]
		tagValue := insertKey[6 : 6+tagLen]
		createdAt := uint64(insertKey[6+tagLen])<<56 | uint64(insertKey[6+tagLen+1])<<48 | uint64(insertKey[6+tagLen+2])<<40 | uint64(insertKey[6+tagLen+3])<<32 | uint64(insertKey[6+tagLen+4])<<24 | uint64(insertKey[6+tagLen+5])<<16 | uint64(insertKey[6+tagLen+6])<<8 | uint64(insertKey[6+tagLen+7])

		fmt.Printf("Key breakdown:\n")
		fmt.Printf("  [0:4] kind: %d\n", kind)
		fmt.Printf("  [4] searchType: %d\n", stype)
		fmt.Printf("  [5] tagLen: %d\n", tagLen)
		fmt.Printf("  [6:%d] tagValue: %q\n", 6+tagLen, string(tagValue))
		fmt.Printf("  [%d:%d] createdAt: %d\n", 6+tagLen, 6+tagLen+8, createdAt)

		// Build query range keys
		startKey := kb.BuildSearchKey(tc.kind, searchType, []byte(tc.value), 0)
		endKey := kb.BuildSearchKey(tc.kind, searchType, []byte(tc.value), ^uint64(0))

		fmt.Printf("Query startKey: %x\n", startKey)
		fmt.Printf("Query endKey: %x\n", endKey)

		// Check if insert key is in range
		insertKeyStr := string(insertKey)
		startKeyStr := string(startKey)
		endKeyStr := string(endKey)

		inRange := insertKeyStr >= startKeyStr && insertKeyStr <= endKeyStr
		fmt.Printf("Insert key in query range: %v\n", inRange)

		// Check prefix matching
		prefixLen := 6 + len(tc.value)
		if len(insertKey) >= prefixLen && len(startKey) >= prefixLen {
			insertPrefix := insertKey[:prefixLen]
			startPrefix := startKey[:prefixLen]
			fmt.Printf("Insert key prefix matches start key prefix (except createdAt): %v\n", string(insertPrefix) == string(startPrefix))
		}
	}
}
