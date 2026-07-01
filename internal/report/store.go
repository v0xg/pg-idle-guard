package report

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Store persists Events as one JSON object per line (JSONL). A Store is owned
// by a single process — there is no cross-process locking, consistent with the
// single-daemon-per-database model. The mutex serializes the daemon's
// poll-loop appends against the scheduler's reads and prunes; in particular,
// the temp-file-plus-rename in Prune must not race an Append.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore creates a store backed by the JSONL file at path. The file and its
// directory are created lazily on first append.
func NewStore(path string) *Store {
	return &Store{path: path}
}

// Path returns the file the store reads and writes.
func (s *Store) Path() string {
	return s.path
}

// DefaultPath returns $XDG_STATE_HOME/pguard/events.jsonl, falling back to
// ~/.local/state/pguard/events.jsonl.
func DefaultPath() (string, error) {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "pguard", "events.jsonl"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "pguard", "events.jsonl"), nil
}

// Append writes one event line, creating the directory (0700) and file (0600)
// on first use.
func (s *Store) Append(e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("creating state directory: %w", err)
	}

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("marshaling event: %w", err)
	}

	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening event file: %w", err)
	}
	_, werr := f.Write(append(line, '\n'))
	cerr := f.Close()
	if werr != nil {
		return fmt.Errorf("writing event: %w", werr)
	}
	if cerr != nil {
		return fmt.Errorf("closing event file: %w", cerr)
	}
	return nil
}

// ReadSince returns all events recorded at or after t, in file order.
// A missing file yields an empty result, not an error. Malformed lines are
// skipped with a debug log so one corrupt line cannot poison the report.
func (s *Store) ReadSince(t time.Time) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readSinceLocked(t)
}

func (s *Store) readSinceLocked(t time.Time) ([]Event, error) {
	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("opening event file: %w", err)
	}
	defer f.Close()

	var events []Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			slog.Debug("skipping malformed event line", "file", s.path, "error", err)
			continue
		}
		if e.Time.Before(t) {
			continue
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading event file: %w", err)
	}
	return events, nil
}

// Prune rewrites the file keeping only events recorded at or after olderThan.
// Survivors are written to a temp file in the same directory which is then
// renamed over the original, so a crash mid-prune never loses the file.
// A missing file is a no-op.
func (s *Store) Prune(olderThan time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, err := os.Stat(s.path); os.IsNotExist(err) {
		return nil
	}

	keep, err := s.readSinceLocked(olderThan)
	if err != nil {
		return err
	}

	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, "events-*.jsonl.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if err := tmp.Chmod(0o600); err != nil {
		cleanup()
		return fmt.Errorf("setting temp file mode: %w", err)
	}
	w := bufio.NewWriter(tmp)
	for _, e := range keep {
		line, err := json.Marshal(e)
		if err != nil {
			cleanup()
			return fmt.Errorf("marshaling event: %w", err)
		}
		if _, err := w.Write(append(line, '\n')); err != nil {
			cleanup()
			return fmt.Errorf("writing temp file: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		cleanup()
		return fmt.Errorf("flushing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replacing event file: %w", err)
	}
	return nil
}
