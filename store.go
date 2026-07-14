package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// LocalStore is a flat JSONL ring-buffer store for MeasureResults.
// It survives upstream outages and replays on reconnection.
type LocalStore struct {
	path     string
	maxLines int
	mu       sync.Mutex
	file     *os.File
}

// NewLocalStore opens (or creates) the store file at path with a cap of maxLines.
func NewLocalStore(path string, maxLines int) (*LocalStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0644)
	if err != nil {
		return nil, fmt.Errorf("open store %s: %w", path, err)
	}
	return &LocalStore{path: path, maxLines: maxLines, file: f}, nil
}

// Append writes one MeasureResult as a JSON line, enforcing the cap.
func (s *LocalStore) Append(r MeasureResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	line, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if _, err := fmt.Fprintf(s.file, "%s\n", line); err != nil {
		return fmt.Errorf("write store: %w", err)
	}
	return s.enforceCap()
}

// enforceCap reads all lines and rewrites the file keeping only the last maxLines.
// Called while mu is held.
func (s *LocalStore) enforceCap() error {
	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	scanner := bufio.NewScanner(s.file)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) <= s.maxLines {
		return nil
	}
	lines = lines[len(lines)-s.maxLines:]
	if err := s.file.Truncate(0); err != nil {
		return err
	}
	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	w := bufio.NewWriter(s.file)
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	return w.Flush()
}

// Flush reads all stored results, calls fn in batches of 100, and clears the file on success.
func (s *LocalStore) Flush(ctx context.Context, fn func([]MeasureResult) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := s.file.Seek(0, 0); err != nil {
		return err
	}
	scanner := bufio.NewScanner(s.file)
	var batch []MeasureResult
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		var r MeasureResult
		if err := json.Unmarshal([]byte(scanner.Text()), &r); err != nil {
			continue
		}
		batch = append(batch, r)
		if len(batch) >= 100 {
			if err := fn(batch); err != nil {
				return err
			}
			batch = batch[:0]
		}
	}
	if len(batch) > 0 {
		if err := fn(batch); err != nil {
			return err
		}
	}
	// Clear the file after successful flush
	s.file.Truncate(0)
	s.file.Seek(0, 0)
	return nil
}

// Close closes the underlying file.
func (s *LocalStore) Close() error {
	return s.file.Close()
}
