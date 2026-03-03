package main

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
)

func main() {
	// 解析错误信息中的 key
	fmt.Println("=== 解析索引 Key 信息 ===\n")

	// Min/Max key bound
	minKeyHex := "0001090b636f6e6e656374696f6e5f69647369646964"
	fmt.Printf("Min/Max Key: %s\n", minKeyHex)

	minKey, _ := hex.DecodeString(minKeyHex)
	if len(minKey) >= 6 {
		kind := binary.BigEndian.Uint16(minKey[0:2])
		searchType := minKey[2]
		fmt.Printf("  - Kind: %d\n", kind)
		fmt.Printf("  - Search Type: 0x%02x (%d)\n", searchType, searchType)

		// 尝试解析 tag value 长度
		if len(minKey) >= 4 {
			tagLen := binary.BigEndian.Uint16(minKey[3:5])
			fmt.Printf("  - Tag Value Length: %d\n", tagLen)

			if len(minKey) >= 5+int(tagLen) {
				tagValue := minKey[5 : 5+tagLen]
				fmt.Printf("  - Tag Value: %s\n", string(tagValue))

				// 最后4字节是 timestamp
				if len(minKey) >= 5+int(tagLen)+4 {
					timestamp := binary.BigEndian.Uint32(minKey[5+int(tagLen):])
					fmt.Printf("  - Timestamp: %d\n", timestamp)
				}
			}
		}
	}

	fmt.Println()

	// Current key
	currentKeyHex := "00030240373736383961303134313034"
	fmt.Printf("Current Key: %s\n", currentKeyHex)

	currentKey, _ := hex.DecodeString(currentKeyHex)
	if len(currentKey) >= 6 {
		kind := binary.BigEndian.Uint16(currentKey[0:2])
		searchType := currentKey[2]
		fmt.Printf("  - Kind: %d\n", kind)
		fmt.Printf("  - Search Type: 0x%02x (%d)\n", searchType, searchType)

		if len(currentKey) >= 4 {
			tagLen := binary.BigEndian.Uint16(currentKey[3:5])
			fmt.Printf("  - Tag Value Length: %d\n", tagLen)

			if len(currentKey) >= 5+int(tagLen) {
				tagValue := currentKey[5 : 5+tagLen]
				fmt.Printf("  - Tag Value: %s\n", string(tagValue))

				if len(currentKey) >= 5+int(tagLen)+4 {
					timestamp := binary.BigEndian.Uint32(currentKey[5+int(tagLen):])
					fmt.Printf("  - Timestamp: %d\n", timestamp)
				}
			}
		}
	}

	fmt.Println("\n=== 问题诊断 ===")
	fmt.Println("Min/Max key 相同 = 精确查找（点查询）")
	fmt.Println("Current key 与查询 key 不匹配 = 迭代器在错误区域")
	fmt.Println("可能原因：反向迭代优化的边界条件有问题")
}
