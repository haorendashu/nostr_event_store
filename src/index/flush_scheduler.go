package index

import (
	"context"
	"sync"
	"time"
)

// flushScheduler manages periodic flushing of dirty index pages
type flushScheduler struct {
	indexes         []Index
	flushIntervalMs int64
	stopChan        chan struct{}
	wg              sync.WaitGroup
	mu              sync.Mutex
	running         bool
}

// newFlushScheduler creates a new flush scheduler
func newFlushScheduler(indexes []Index, flushIntervalMs int64) *flushScheduler {
	return &flushScheduler{
		indexes:         indexes,
		flushIntervalMs: flushIntervalMs,
		stopChan:        make(chan struct{}),
	}
}

// Start begins the flush scheduler background task
func (fs *flushScheduler) Start(ctx context.Context) {
	fs.mu.Lock()
	if fs.running {
		fs.mu.Unlock()
		return
	}
	fs.running = true
	fs.mu.Unlock()

	fs.wg.Add(1)
	go fs.flushLoop(ctx)
}

// Stop stops the flush scheduler and flushes all indexes before returning
func (fs *flushScheduler) Stop(ctx context.Context) error {
	fs.mu.Lock()
	if !fs.running {
		fs.mu.Unlock()
		return nil
	}
	fs.mu.Unlock()

	// Signal flush loop to stop
	select {
	case fs.stopChan <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}

	// Wait for flush loop to finish
	done := make(chan struct{})
	go func() {
		fs.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Final flush before stopping
		for _, idx := range fs.indexes {
			if idx != nil {
				_ = idx.Flush(ctx)
			}
		}
		fs.mu.Lock()
		fs.running = false
		fs.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// flushLoop runs the periodic flush task
func (fs *flushScheduler) flushLoop(ctx context.Context) {
	defer fs.wg.Done()

	ticker := time.NewTicker(time.Duration(fs.flushIntervalMs) * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Periodic flush
			for _, idx := range fs.indexes {
				if idx != nil {
					_ = idx.Flush(ctx)
				}
			}
		case <-fs.stopChan:
			// Stop requested
			return
		case <-ctx.Done():
			// Context cancelled
			return
		}
	}
}
