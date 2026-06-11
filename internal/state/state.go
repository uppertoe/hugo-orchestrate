// Package state persists per-site build state as JSON files whose shape
// matches the Python implementation, so existing tooling keeps working.
package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SiteState records the outcome of a site's most recent build.
type SiteState struct {
	Slug       string    `json:"slug"`
	Reason     string    `json:"reason"`
	Commit     string    `json:"commit"`
	DurationMS int64     `json:"duration_ms"`
	Status     string    `json:"status"` // success | failed
	Error      string    `json:"error,omitempty"`
	FinishedAt time.Time `json:"finished_at"`
}

// Store reads and writes SiteState files under a directory.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore creates the state directory if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	return &Store{dir: dir}, nil
}

func (s *Store) path(slug string) string { return filepath.Join(s.dir, slug+".json") }

// Write persists the state atomically (temp file + rename).
func (s *Store) Write(st SiteState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(st.Slug) + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write state: %w", err)
	}
	if err := os.Rename(tmp, s.path(st.Slug)); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("write state: %w", err)
	}
	return nil
}

// Read returns the state for slug, or (nil, nil) if none exists yet.
func (s *Store) Read(slug string) (*SiteState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := os.ReadFile(s.path(slug))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var st SiteState
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fmt.Errorf("parse state for %s: %w", slug, err)
	}
	return &st, nil
}
