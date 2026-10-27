// Package state stores snapshots of preferences before Pear changes them.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const snapshotVersion = 1

// Store is a directory of timestamped snapshots.
type Store struct {
	Dir string
}

// Entry records the previous state of one setting.
type Entry struct {
	ID      string   `json:"id"`
	Scope   string   `json:"scope"`
	Domain  string   `json:"domain"`
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Restart []string `json:"restart,omitempty"`
	Existed bool     `json:"existed"`
	Value   any      `json:"value,omitempty"`
}

// Snapshot is one apply's saved state.
type Snapshot struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	Hostname  string    `json:"hostname"`
	Entries   []Entry   `json:"entries"`
}

// Info describes a stored snapshot.
type Info struct {
	Name      string
	Path      string
	CreatedAt time.Time
	Entries   int
}

// DefaultDir returns $XDG_STATE_HOME/pear or ~/.local/state/pear.
func DefaultDir() string {
	if x := strings.TrimSpace(os.Getenv("XDG_STATE_HOME")); x != "" {
		return filepath.Join(x, "pear")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".local", "state", "pear")
	}
	return filepath.Join(home, ".local", "state", "pear")
}

// New returns a store rooted at dir, creating it if needed.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("state directory: %w", err)
	}
	return &Store{Dir: dir}, nil
}

// Default returns a store in the default location.
func Default() (*Store, error) {
	return New(DefaultDir())
}

// Save writes entries to a new timestamped snapshot and returns its directory.
func (s *Store) Save(entries []Entry) (string, error) {
	backups := filepath.Join(s.Dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		return "", fmt.Errorf("create backups directory: %w", err)
	}

	base := time.Now().UTC().Format("20060102T150405Z")
	dir := filepath.Join(backups, base)
	for i := 1; ; i++ {
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			break
		}
		dir = filepath.Join(backups, fmt.Sprintf("%s-%02d", base, i))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create snapshot: %w", err)
	}

	hostname, _ := os.Hostname()
	snap := Snapshot{
		Version:   snapshotVersion,
		CreatedAt: time.Now().UTC(),
		Hostname:  hostname,
		Entries:   entries,
	}
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode snapshot: %w", err)
	}
	data = append(data, '\n')

	path := filepath.Join(dir, "snapshot.json")
	tmp, err := os.CreateTemp(dir, ".snapshot-*")
	if err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("write snapshot: %w", err)
	}
	return dir, nil
}

// List returns snapshots newest first.
func (s *Store) List() ([]Info, error) {
	backups := filepath.Join(s.Dir, "backups")
	entries, err := os.ReadDir(backups)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list snapshots: %w", err)
	}

	var infos []Info
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(backups, entry.Name())
		snap, err := Load(path)
		if err != nil {
			continue // Ignore incomplete or foreign directories.
		}
		infos = append(infos, Info{
			Name:      entry.Name(),
			Path:      path,
			CreatedAt: snap.CreatedAt,
			Entries:   len(snap.Entries),
		})
	}
	sort.Slice(infos, func(i, j int) bool {
		if infos[i].CreatedAt.Equal(infos[j].CreatedAt) {
			return infos[i].Name > infos[j].Name
		}
		return infos[i].CreatedAt.After(infos[j].CreatedAt)
	})
	return infos, nil
}

// Latest returns the newest snapshot.
func (s *Store) Latest() (*Snapshot, string, error) {
	infos, err := s.List()
	if err != nil {
		return nil, "", err
	}
	if len(infos) == 0 {
		return nil, "", fmt.Errorf("no snapshots in %s", filepath.Join(s.Dir, "backups"))
	}
	snap, err := Load(infos[0].Path)
	if err != nil {
		return nil, "", err
	}
	return snap, infos[0].Path, nil
}

// Open resolves a snapshot directory or name and loads it. An empty name means
// the newest snapshot.
func (s *Store) Open(name string) (*Snapshot, string, error) {
	if strings.TrimSpace(name) == "" {
		return s.Latest()
	}
	path := name
	if !filepath.IsAbs(path) {
		candidate := filepath.Join(s.Dir, "backups", name)
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
		}
	}
	snap, err := Load(path)
	if err != nil {
		return nil, "", err
	}
	return snap, path, nil
}

// Load reads a snapshot from a directory containing snapshot.json.
func Load(dir string) (*Snapshot, error) {
	data, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("parse snapshot %s: %w", dir, err)
	}
	if snap.Version != snapshotVersion {
		return nil, fmt.Errorf("snapshot %s: unsupported version %d", dir, snap.Version)
	}
	return &snap, nil
}
