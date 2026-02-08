package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

// EventDTO represents a Nostr event prepared for JSON serialization
type EventDTO struct {
	ID        string     `json:"id"`
	Pubkey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

func main() {
	relayURL := flag.String("relay", "wss://relay.damus.io", "Relay URL to connect to")
	targetCount := flag.Int("count", 1000, "Target number of events to collect")
	timeout := flag.Int("timeout", 180, "Timeout in seconds")
	useLocal := flag.Bool("local", false, "Generate local test data instead of connecting to relay")

	flag.Parse()

	fmt.Println("[*] Starting Nostr Event Collector...")
	fmt.Printf("[*] Target event count: %d\n", *targetCount)

	var events []*EventDTO

	if *useLocal {
		fmt.Println("[*] Using local data generation mode")
		events = generateLocalTestData(*targetCount)
	} else {
		fmt.Printf("[*] Connecting to relay: %s\n", *relayURL)
		fmt.Printf("[*] Timeout: %d seconds\n", *timeout)

		ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeout)*time.Second)
		defer cancel()

		var err error
		events, err = collectFromRelay(ctx, *relayURL, *targetCount)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[-] Failed to collect from relay: %v\n", err)
			fmt.Println("[*] Falling back to local data generation...")
			events = generateLocalTestData(*targetCount)
		}
	}

	// Save to JSON file
	saveEventsToFile(events)
}

func collectFromRelay(ctx context.Context, relayURL string, targetCount int) ([]*EventDTO, error) {
	// Connect to relay
	relay, err := nostr.RelayConnect(ctx, relayURL)
	if err != nil {
		return nil, fmt.Errorf("failed to connect: %w", err)
	}
	defer relay.Close()

	fmt.Println("[+] Connected to relay")

	// Create filter for all events
	filters := nostr.Filters{
		{
			Limit: targetCount * 2, // Request more than we need
		},
	}

	// Subscribe to events
	fmt.Println("[*] Subscribing to events...")
	subscription, err := relay.Subscribe(ctx, filters)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe: %w", err)
	}

	// Collect events
	eventMap := make(map[string]*EventDTO)
	startTime := time.Now()

	// Inner context for receiving events
	receiveCtx, receiveCancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer receiveCancel()

	for {
		select {
		case <-receiveCtx.Done():
			fmt.Println("[*] Receive timeout reached")
			goto finish
		case event := <-subscription.Events:
			if event == nil {
				fmt.Println("[*] Subscription closed")
				goto finish
			}

			// Convert to EventDTO
			// Convert nostr.Timestamp to Unix timestamp
			createdAtUnix := int64(event.CreatedAt)

			// Convert nostr.Tags to [][]string
			tagsSlice := make([][]string, len(event.Tags))
			for i, tag := range event.Tags {
				tagsSlice[i] = tag
			}

			dto := EventDTO{
				ID:        event.ID,
				Pubkey:    event.PubKey,
				CreatedAt: createdAtUnix,
				Kind:      event.Kind,
				Tags:      tagsSlice,
				Content:   event.Content,
				Sig:       event.Sig,
			}

			// Store in map (automatically deduplicates by ID)
			eventMap[event.ID] = &dto

			// Print progress
			if len(eventMap)%50 == 0 {
				fmt.Printf("[+] Collected %d unique events...\n", len(eventMap))
			}

			// Stop if we have enough events
			if len(eventMap) >= targetCount {
				fmt.Printf("[+] Reached target count of %d events\n", targetCount)
				goto finish
			}

		case <-ctx.Done():
			fmt.Println("[*] Context deadline exceeded")
			goto finish
		}
	}

finish:
	fmt.Printf("[+] Collection finished in %.2f seconds\n", time.Since(startTime).Seconds())
	fmt.Printf("[+] Total unique events collected: %d\n", len(eventMap))

	// Convert map to sorted slice
	events := make([]*EventDTO, 0, len(eventMap))
	for _, event := range eventMap {
		events = append(events, event)
	}

	return events, nil
}

func generateLocalTestData(count int) []*EventDTO {
	fmt.Printf("[*] Generating %d local test events...\n", count)

	events := make([]*EventDTO, 0, count)
	baseTime := time.Now().Unix()

	// Sample content for different event types
	userProfiles := []string{
		`{"name":"Alice","about":"Bitcoin enthusiast","picture":"https://example.com/alice.jpg"}`,
		`{"name":"Bob","about":"Nostr developer","picture":"https://example.com/bob.jpg"}`,
		`{"name":"Carol","about":"Privacy advocate","picture":"https://example.com/carol.jpg"}`,
		`{"name":"Dave","about":"Nostr user","picture":"https://example.com/dave.jpg"}`,
		`{"name":"Eve","about":"Cryptographer","picture":"https://example.com/eve.jpg"}`,
	}

	notes := []string{
		"Just joined Nostr, excited to explore!",
		"Bitcoin is the future of money.",
		"Decentralized social media matters.",
		"Censorship-resistant communication is essential.",
		"Love the Nostr protocol and its simplicity.",
		"Building cool stuff with Nostr.",
		"Privacy should be a fundamental right.",
		"Interoperability is key to success.",
		"Distributed networks are powerful.",
		"Looking forward to seeing where Nostr goes.",
		"Great day to be on Nostr!",
		"The future is decentralized.",
		"Nostr makes sense for many use cases.",
		"Freedom of expression matters.",
		"Decentralization over centralization.",
	}

	reactions := []string{"🎉", "❤️", "🔥", "👍", "🤑", "🚀"}

	for i := 0; i < count; i++ {
		kind := (i % 6) // Cycle through kinds 0-5

		pubkey := generateRandomHex(64)

		var content string
		var tags [][]string

		switch kind {
		case 0: // Metadata
			content = userProfiles[i%len(userProfiles)]
		case 1: // Note
			content = notes[i%len(notes)]
		case 3: // Contacts
			// Add some tags for contacts
			tags = append(tags, []string{"p", generateRandomHex(64), "wss://relay.example.com", "friend"})
		case 5: // Reaction
			targetNoteID := generateRandomHex(64)
			content = reactions[i%len(reactions)]
			tags = append(tags, []string{"e", targetNoteID, "wss://relay.example.com", "reply"})
			tags = append(tags, []string{"p", generateRandomHex(64), "wss://relay.example.com"})
		}

		// Create event
		event := &EventDTO{
			ID:        generateRandomHex(64),
			Pubkey:    pubkey,
			CreatedAt: baseTime - int64(count-i)*60, // Spread over time
			Kind:      kind,
			Tags:      tags,
			Content:   content,
			Sig:       generateRandomHex(128),
		}

		events = append(events, event)
	}

	fmt.Printf("[+] Generated %d test events\n", len(events))
	return events
}

func generateRandomHex(length int) string {
	bytes := make([]byte, length/2)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}

func saveEventsToFile(events []*EventDTO) {
	outputPath := filepath.Join("seed", "events.json")
	fmt.Printf("[*] Saving events to: %s\n", outputPath)

	// Ensure output directory exists
	outputDir := filepath.Dir(outputPath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Failed to create output directory: %v\n", err)
		os.Exit(1)
	}

	// Marshal to JSON
	jsonData, err := json.MarshalIndent(events, "", "  ")
	if err != nil {
		fmt.Fprintf(os.Stderr, "[-] Failed to marshal JSON: %v\n", err)
		os.Exit(1)
	}

	// Write to file
	if err := os.WriteFile(outputPath, jsonData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "[-] Failed to write output file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("[+] Successfully saved %d events to %s\n", len(events), outputPath)

	// Print sample event for verification
	if len(events) > 0 {
		fmt.Println("\n[*] Sample event (first):")
		sampleJSON, _ := json.MarshalIndent(events[0], "    ", "  ")
		fmt.Printf("    %s\n", string(sampleJSON))
	}

	// Print event kind distribution
	fmt.Println("\n[*] Event kind distribution:")
	kindCount := make(map[int]int)
	for _, event := range events {
		kindCount[event.Kind]++
	}
	for kind := 0; kind <= 10; kind++ {
		if count, exists := kindCount[kind]; exists {
			fmt.Printf("    Kind %d: %d events\n", kind, count)
		}
	}
}
