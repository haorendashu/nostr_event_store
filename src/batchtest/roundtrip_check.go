package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// TestStringRoundtrip tests if []byte -> string -> []byte preserves the content
func TestStringRoundtrip() {
	fmt.Println("=== String Roundtrip Test ===")

	// Build an e-tag key (kind|searchType|len|value|time)
	kind := uint32(10003)
	searchType := byte(1)
	tagValue := "28587da287bb2b61335dab7bad2adaf6b975acfb128f4cf078c3bcf26d73c2ce"
	tagLen := byte(len(tagValue))
	createdAt := uint64(1000000)

	key := make([]byte, 4+1+1+len(tagValue)+8)
	binary.BigEndian.PutUint32(key[0:4], kind)
	key[4] = searchType
	key[5] = tagLen
	copy(key[6:6+len(tagValue)], []byte(tagValue))
	binary.BigEndian.PutUint64(key[6+len(tagValue):], createdAt)

	fmt.Printf("Original key: %x\n", key)
	fmt.Printf("Original key length: %d\n", len(key))

	// Convert to string and back
	keyStr := string(key)
	keyBack := []byte(keyStr)

	fmt.Printf("After roundtrip: %x\n", keyBack)
	fmt.Printf("After roundtrip length: %d\n", len(keyBack))

	// Check if they match
	if bytes.Equal(key, keyBack) {
		fmt.Println("✓ Roundtrip SUCCESSFUL - bytes match!")
	} else {
		fmt.Println("✗ Roundtrip FAILED - bytes differ!")

		// Find first difference
		minLen := len(key)
		if len(keyBack) < minLen {
			minLen = len(keyBack)
		}
		for i := 0; i < minLen; i++ {
			if key[i] != keyBack[i] {
				fmt.Printf("  First diff at byte %d: original=%02x, after=%02x\n", i, key[i], keyBack[i])
				break
			}
		}
		if len(key) != len(keyBack) {
			fmt.Printf("  Length mismatch: original=%d, after=%d\n", len(key), len(keyBack))
		}
	}

	// Test with i-tag (which works)
	fmt.Println("\n=== i-tag test (working case) ===")
	kind2 := uint32(1)
	searchType2 := byte(11)
	tagValue2 := "url https://blossom.primal.net/b9bfc34b5293f6308b53881db1c84cd804f97865aa4d40913988e051277833df.jpg"
	tagLen2 := byte(len(tagValue2))
	createdAt2 := uint64(1000000)

	key2 := make([]byte, 4+1+1+len(tagValue2)+8)
	binary.BigEndian.PutUint32(key2[0:4], kind2)
	key2[4] = searchType2
	key2[5] = tagLen2
	copy(key2[6:6+len(tagValue2)], []byte(tagValue2))
	binary.BigEndian.PutUint64(key2[6+len(tagValue2):], createdAt2)

	keyStr2 := string(key2)
	keyBack2 := []byte(keyStr2)

	if bytes.Equal(key2, keyBack2) {
		fmt.Println("✓ i-tag roundtrip SUCCESSFUL")
	} else {
		fmt.Println("✗ i-tag roundtrip FAILED")
	}
}
