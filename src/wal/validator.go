package wal

import (
	"encoding/binary"
	"fmt"
	"hash/crc64"
	"io"
	"os"
	"path/filepath"
)

// ValidationResult contains the validation report for a WAL file
type ValidationResult struct {
	FilePath          string
	FileSize          int64
	TotalEntries      int
	ValidEntries      int
	InvalidEntries    int
	Errors            []ValidationError
	FirstErrorAtLSN   uint64
	LastSuccessfulLSN uint64
	HeaderValid       bool
	LastCheckpointLSN uint64
}

// ValidationError describes a validation problem
type ValidationError struct {
	LSN     uint64
	Offset  int64
	Message string
	Data    string // Hex or descriptive info
}

// ValidateWALFile validates a WAL file for integrity
func ValidateWALFile(filePath string) (*ValidationResult, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open WAL file: %w", err)
	}
	defer file.Close()

	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to stat WAL file: %w", err)
	}

	result := &ValidationResult{
		FilePath:    filePath,
		FileSize:    fileInfo.Size(),
		Errors:      make([]ValidationError, 0),
		HeaderValid: false,
	}

	// Validate header
	headerData := make([]byte, walHeaderSize)
	if _, err := file.Read(headerData); err != nil {
		return result, fmt.Errorf("failed to read WAL header: %w", err)
	}

	// Check magic number
	magic := binary.BigEndian.Uint32(headerData[0:4])
	if magic != walMagic {
		result.Errors = append(result.Errors, ValidationError{
			LSN:     0,
			Offset:  0,
			Message: fmt.Sprintf("Invalid WAL magic number: 0x%X (expected 0x%X)", magic, walMagic),
			Data:    fmt.Sprintf("header=%x", headerData[:4]),
		})
		return result, nil
	}

	// Check version
	version := binary.BigEndian.Uint64(headerData[4:12])
	if version != walVersion {
		result.Errors = append(result.Errors, ValidationError{
			LSN:     0,
			Offset:  4,
			Message: fmt.Sprintf("Invalid WAL version: %d (expected %d)", version, walVersion),
			Data:    fmt.Sprintf("version=%d", version),
		})
		return result, nil
	}

	// Read checkpoint LSN
	result.LastCheckpointLSN = binary.BigEndian.Uint64(headerData[12:20])
	result.HeaderValid = true

	// Validate entries
	offset := int64(walHeaderSize)
	crcTable := crc64.MakeTable(crc64.ECMA)

	for {
		// Try to read entry header (1+8+8+4 = 21 bytes)
		headerBuf := make([]byte, 21)
		n, err := file.Read(headerBuf)
		if err == io.EOF {
			break
		}
		if err != nil {
			result.Errors = append(result.Errors, ValidationError{
				Offset:  offset,
				Message: fmt.Sprintf("Error reading entry header: %v", err),
			})
			break
		}
		if n < 21 {
			result.Errors = append(result.Errors, ValidationError{
				Offset:  offset,
				Message: fmt.Sprintf("Incomplete entry header: got %d bytes, expected 21", n),
			})
			break
		}

		opType := OpType(headerBuf[0])
		lsn := binary.BigEndian.Uint64(headerBuf[1:9])
		timestamp := binary.BigEndian.Uint64(headerBuf[9:17])
		dataLen := binary.BigEndian.Uint32(headerBuf[17:21])

		result.TotalEntries++

		// Validate dataLen
		if dataLen > uint32(walMaxRecordSize) {
			result.InvalidEntries++
			result.FirstErrorAtLSN = lsn
			result.Errors = append(result.Errors, ValidationError{
				LSN:     lsn,
				Offset:  offset,
				Message: fmt.Sprintf("dataLen exceeds max record size: %d > %d", dataLen, walMaxRecordSize),
				Data:    fmt.Sprintf("opType=%d ts=%d", opType, timestamp),
			})
			// Try to continue with next entry by seeking ahead
			if _, err := file.Seek(int64(dataLen)+8, io.SeekCurrent); err != nil {
				break
			}
			offset += 21 + int64(dataLen) + 8
			continue
		}

		// Check if entry data would exceed file size
		entryEndOffset := offset + 21 + int64(dataLen) + 8
		if entryEndOffset > result.FileSize {
			result.InvalidEntries++
			result.FirstErrorAtLSN = lsn
			result.Errors = append(result.Errors, ValidationError{
				LSN:     lsn,
				Offset:  offset,
				Message: fmt.Sprintf("Entry extends beyond file: offset=%d+21+%d+8=%d > fileSize=%d", offset, dataLen, entryEndOffset, result.FileSize),
				Data:    fmt.Sprintf("opType=%d ts=%d", opType, timestamp),
			})
			break
		}

		// Read data + checksum
		totalSize := int(dataLen) + 8
		entryData := make([]byte, totalSize)
		n, err = file.Read(entryData)
		if err != nil && err != io.EOF {
			result.InvalidEntries++
			result.FirstErrorAtLSN = lsn
			result.Errors = append(result.Errors, ValidationError{
				LSN:     lsn,
				Offset:  offset + 21,
				Message: fmt.Sprintf("Error reading entry data: %v", err),
			})
			break
		}
		if n < totalSize {
			result.InvalidEntries++
			result.FirstErrorAtLSN = lsn
			result.Errors = append(result.Errors, ValidationError{
				LSN:     lsn,
				Offset:  offset + 21,
				Message: fmt.Sprintf("Incomplete entry data: got %d bytes, expected %d", n, totalSize),
				Data:    fmt.Sprintf("dataLen=%d", dataLen),
			})
			break
		}

		// Extract data and checksum
		data := entryData[:dataLen]
		storedChecksum := binary.BigEndian.Uint64(entryData[dataLen : dataLen+8])

		// Verify checksum
		checksumData := make([]byte, 21+dataLen)
		copy(checksumData, headerBuf)
		copy(checksumData[21:], data)
		calculatedChecksum := crc64.Checksum(checksumData, crcTable)

		if calculatedChecksum != storedChecksum {
			result.InvalidEntries++
			result.FirstErrorAtLSN = lsn
			result.Errors = append(result.Errors, ValidationError{
				LSN:     lsn,
				Offset:  offset,
				Message: fmt.Sprintf("Checksum mismatch"),
				Data:    fmt.Sprintf("calculated=0x%X stored=0x%X", calculatedChecksum, storedChecksum),
			})
		} else {
			result.ValidEntries++
			result.LastSuccessfulLSN = lsn
		}

		offset += 21 + int64(dataLen) + 8
	}

	return result, nil
}

// ValidateWALDirectory validates all WAL files in a directory
func ValidateWALDirectory(walDir string) ([]*ValidationResult, error) {
	entries, err := os.ReadDir(walDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read WAL directory: %w", err)
	}

	var results []*ValidationResult
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if filepath.Ext(entry.Name()) != ".log" {
			continue
		}

		filePath := filepath.Join(walDir, entry.Name())
		result, err := ValidateWALFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to validate %s: %v\n", filePath, err)
			continue
		}
		results = append(results, result)
	}

	return results, nil
}

// PrintValidationReport prints a validation result in human-readable format
func PrintValidationReport(result *ValidationResult) {
	fmt.Printf("\n=== WAL Validation Report ===\n")
	fmt.Printf("File: %s\n", result.FilePath)
	fmt.Printf("Size: %d bytes\n", result.FileSize)
	fmt.Printf("Header Valid: %v\n", result.HeaderValid)
	if result.HeaderValid {
		fmt.Printf("Last Checkpoint LSN: %d\n", result.LastCheckpointLSN)
	}
	fmt.Printf("\nEntry Statistics:\n")
	fmt.Printf("  Total: %d\n", result.TotalEntries)
	fmt.Printf("  Valid: %d\n", result.ValidEntries)
	fmt.Printf("  Invalid: %d\n", result.InvalidEntries)

	if len(result.Errors) > 0 {
		fmt.Printf("\nFirst Error at LSN: %d\n", result.FirstErrorAtLSN)
		fmt.Printf("Last Successful LSN: %d\n", result.LastSuccessfulLSN)
		fmt.Printf("\nErrors (first 10):\n")
		maxErrors := 10
		if len(result.Errors) < maxErrors {
			maxErrors = len(result.Errors)
		}
		for i := 0; i < maxErrors; i++ {
			err := result.Errors[i]
			fmt.Printf("  [LSN %d @ offset %d]: %s\n", err.LSN, err.Offset, err.Message)
			if err.Data != "" {
				fmt.Printf("    Data: %s\n", err.Data)
			}
		}
		if len(result.Errors) > maxErrors {
			fmt.Printf("  ... and %d more errors\n", len(result.Errors)-maxErrors)
		}
	} else {
		fmt.Printf("\n✓ WAL file is valid\n")
	}
}
