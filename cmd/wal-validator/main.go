package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/haorendashu/nostr_event_store/src/wal"
)

func main() {
	walDir := flag.String("dir", "", "WAL directory to validate")
	file := flag.String("file", "", "Single WAL file to validate")
	flag.Parse()

	if *walDir == "" && *file == "" {
		fmt.Fprintf(os.Stderr, "Usage: wal-validator -dir <path> | -file <path>\n")
		os.Exit(1)
	}

	if *file != "" {
		validateSingleFile(*file)
	} else if *walDir != "" {
		validateDirectory(*walDir)
	}
}

func validateSingleFile(filePath string) {
	if _, err := os.Stat(filePath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot access file %s: %v\n", filePath, err)
		os.Exit(1)
	}

	result, err := wal.ValidateWALFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error validating WAL file: %v\n", err)
		os.Exit(1)
	}

	wal.PrintValidationReport(result)

	if result.InvalidEntries > 0 {
		os.Exit(1)
	}
}

func validateDirectory(walDir string) {
	if _, err := os.Stat(walDir); err != nil {
		fmt.Fprintf(os.Stderr, "Error: cannot access directory %s: %v\n", walDir, err)
		os.Exit(1)
	}

	fmt.Printf("Validating WAL directory: %s\n", walDir)

	// If it's a wal subdirectory, validate it
	if filepath.Base(walDir) == "wal" {
		results, err := wal.ValidateWALDirectory(walDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		totalValid := 0
		totalInvalid := 0

		for _, result := range results {
			wal.PrintValidationReport(result)
			totalValid += result.ValidEntries
			totalInvalid += result.InvalidEntries
		}

		fmt.Printf("\n=== Summary ===\n")
		fmt.Printf("Files validated: %d\n", len(results))
		fmt.Printf("Total valid entries: %d\n", totalValid)
		fmt.Printf("Total invalid entries: %d\n", totalInvalid)

		if totalInvalid > 0 {
			os.Exit(1)
		}
	} else {
		// Otherwise look for wal subdirectory
		walSubDir := filepath.Join(walDir, "wal")
		if _, err := os.Stat(walSubDir); err == nil {
			validateDirectory(walSubDir)
		} else {
			fmt.Fprintf(os.Stderr, "Error: no wal subdirectory found in %s\n", walDir)
			os.Exit(1)
		}
	}
}
