package planstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const (
	indexFileName = "index.json"
	indexVersion  = 1
)

// indexFile is the on-disk shape of <root>/index.json.
type indexFile struct {
	Version int      `json:"version"`
	Records []Record `json:"records"`
}

func (s *Store) indexPath() string {
	return filepath.Join(s.root, indexFileName)
}

// loadIndex reads the index from disk.
//
// A missing index.json is an empty index, so a fresh store works without any
// setup. A file that exists but cannot be decoded is an error: the store never
// silently rebuilds the index, because that would drop records.
func (s *Store) loadIndex() (indexFile, error) {
	path := s.indexPath()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return indexFile{Version: indexVersion, Records: []Record{}}, nil
		}
		return indexFile{}, fmt.Errorf("planstore: read index %s: %w", path, err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return indexFile{}, fmt.Errorf("planstore: index %s is empty", path)
	}
	var idx indexFile
	if err := json.Unmarshal(data, &idx); err != nil {
		return indexFile{}, fmt.Errorf("planstore: decode index %s: %w", path, err)
	}
	if idx.Version == 0 {
		idx.Version = indexVersion
	}
	if idx.Records == nil {
		idx.Records = []Record{}
	}
	return idx, nil
}

// saveIndex replaces index.json atomically.
func (s *Store) saveIndex(idx indexFile) error {
	idx.Version = indexVersion
	if idx.Records == nil {
		idx.Records = []Record{}
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("planstore: encode index: %w", err)
	}
	data = append(data, '\n')
	if err := writeFileAtomic(s.indexPath(), data, 0o644); err != nil {
		return fmt.Errorf("planstore: write index: %w", err)
	}
	return nil
}

// indexFind returns the position of id inside idx.
func indexFind(idx indexFile, id string) (int, bool) {
	for i := range idx.Records {
		if idx.Records[i].ID == id {
			return i, true
		}
	}
	return -1, false
}

// writeFileAtomic writes data to path through a temporary file in the same
// directory followed by an os.Rename, so concurrent readers never observe a
// truncated or partially written file.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmpName, path, err)
	}
	return nil
}
