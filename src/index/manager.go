package index

import (
	"context"
	"path/filepath"
	"time"
)

// manager is the default in-memory index manager implementation.
type manager struct {
	config     Config
	keyBuilder KeyBuilder
	primary    Index
	authorTime Index
	search     Index
	isOpen     bool
	flusher    *flushScheduler
}

func newManager() Manager {
	return &manager{}
}

// Open initializes all indexes from storage.
func (m *manager) Open(ctx context.Context, dir string, cfg Config) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	m.config = cfg
	m.config.Dir = dir
	if m.config.LastRebuildEpoch == 0 {
		m.config.LastRebuildEpoch = time.Now().Unix()
	}

	m.keyBuilder = NewKeyBuilder(cfg.TagNameToSearchTypeCode)

	// Create persistent indexes
	var err error
	primaryPath := filepath.Join(dir, "primary.idx")
	m.primary, err = NewPersistentBTreeIndexWithType(primaryPath, cfg, indexTypePrimary)
	if err != nil {
		return err
	}

	authorTimePath := filepath.Join(dir, "author_time.idx")
	m.authorTime, err = NewPersistentBTreeIndexWithType(authorTimePath, cfg, indexTypeAuthorTime)
	if err != nil {
		m.primary.Close()
		return err
	}

	searchPath := filepath.Join(dir, "search.idx")
	m.search, err = NewPersistentBTreeIndexWithType(searchPath, cfg, indexTypeSearch)
	if err != nil {
		m.primary.Close()
		m.authorTime.Close()
		return err
	}

	// Start flush scheduler for periodic persistence
	m.flusher = newFlushScheduler([]Index{m.primary, m.authorTime, m.search}, int64(cfg.FlushIntervalMs))
	m.flusher.Start(ctx)

	m.isOpen = true
	return nil
}

// PrimaryIndex returns the primary index (id → location).
func (m *manager) PrimaryIndex() Index {
	return m.primary
}

// AuthorTimeIndex returns the author+time index ((pubkey, kind, created_at) → location).
func (m *manager) AuthorTimeIndex() Index {
	return m.authorTime
}

// SearchIndex returns the unified search index.
func (m *manager) SearchIndex() Index {
	return m.search
}

// KeyBuilder returns the current key builder.
func (m *manager) KeyBuilder() KeyBuilder {
	return m.keyBuilder
}

// Flush flushes all indexes to disk.
func (m *manager) Flush(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	if m.primary != nil {
		if err := m.primary.Flush(ctx); err != nil {
			return err
		}
	}
	if m.authorTime != nil {
		if err := m.authorTime.Flush(ctx); err != nil {
			return err
		}
	}
	if m.search != nil {
		if err := m.search.Flush(ctx); err != nil {
			return err
		}
	}
	return nil
}

// Close closes all indexes.
func (m *manager) Close() error {
	// Stop flush scheduler first
	if m.flusher != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		_ = m.flusher.Stop(ctx)
	}

	if m.primary != nil {
		_ = m.primary.Close()
	}
	if m.authorTime != nil {
		_ = m.authorTime.Close()
	}
	if m.search != nil {
		_ = m.search.Close()
	}
	m.isOpen = false
	return nil
}

// AllStats returns statistics for all indexes.
func (m *manager) AllStats() map[string]Stats {
	stats := make(map[string]Stats)
	if m.primary != nil {
		stats["primary"] = m.primary.Stats()
	}
	if m.authorTime != nil {
		stats["author_time"] = m.authorTime.Stats()
	}
	if m.search != nil {
		stats["search"] = m.search.Stats()
	}
	return stats
}
