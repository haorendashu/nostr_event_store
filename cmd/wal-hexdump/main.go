package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <wal-file> <offset>\n", os.Args[0])
		os.Exit(1)
	}

	filePath := os.Args[1]
	var offset int64 = 1033925 // LSN=528 entry offset
	if len(os.Args) > 2 {
		fmt.Sscanf(os.Args[2], "%d", &offset)
	}

	file, err := os.Open(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer file.Close()

	// Seek to offset and read header
	if _, err := file.Seek(offset, 0); err != nil {
		fmt.Fprintf(os.Stderr, "Error seeking: %v\n", err)
		os.Exit(1)
	}

	header := make([]byte, 21)
	n, err := file.Read(header)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Read %d bytes at offset %d\n\n", n, offset)

	// Parse header
	opType := header[0]
	lsn := uint64(header[1])<<56 | uint64(header[2])<<48 | uint64(header[3])<<40 | uint64(header[4])<<32 |
		uint64(header[5])<<24 | uint64(header[6])<<16 | uint64(header[7])<<8 | uint64(header[8])
	timestamp := uint64(header[9])<<56 | uint64(header[10])<<48 | uint64(header[11])<<40 | uint64(header[12])<<32 |
		uint64(header[13])<<24 | uint64(header[14])<<16 | uint64(header[15])<<8 | uint64(header[16])
	dataLen := uint32(header[17])<<24 | uint32(header[18])<<16 | uint32(header[19])<<8 | uint32(header[20])

	fmt.Printf("Offset: %d\n", offset)
	fmt.Printf("OpType: %d\n", opType)
	fmt.Printf("LSN: %d\n", lsn)
	fmt.Printf("Timestamp: %d\n", timestamp)
	fmt.Printf("DataLen: %d\n", dataLen)
	fmt.Printf("Next entry would start at: %d\n", offset+21+int64(dataLen)+8)
	fmt.Printf("Buffer position after header+dataLen: %d\n", 1033925+21+int64(dataLen))

	// Read some data
	dataBuf := make([]byte, min(int(dataLen), 100))
	n2, err := file.Read(dataBuf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error reading data: %v\n", err)
	}
	fmt.Printf("\nFirst %d bytes of data:\n", n2)
	fmt.Printf("%x\n", dataBuf[:n2])
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
