// Package planstore persists plan artifacts as first-class, session-independent
// objects: an index of records, one immutable snapshot per review round, and a
// review status per plan.
//
// Layout under the store root (see DefaultRoot):
//
//	index.json                                   {"version":1,"records":[...]}
//	versions/<projectSlug>/<planName>-v<N>.md    one file per review round
//
// The package is deliberately self-contained: it depends only on the standard
// library and never reads or writes the workspace copy of a plan file.
package planstore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is the review state of a stored plan.
type Status string

const (
	// StatusPending marks a plan that has been written but not adjudicated yet.
	StatusPending Status = "pending"
	// StatusApproved marks a plan the host approved for implementation.
	StatusApproved Status = "approved"
	// StatusNotImplemented marks a plan whose review was cancelled or quit.
	StatusNotImplemented Status = "not_implemented"
)

// ErrNotFound reports that a record (or a requested record version) does not
// exist in the store. Use errors.Is to detect it.
var ErrNotFound = errors.New("planstore: record not found")

// Round is one review/adjudication round of a plan.
type Round struct {
	Version   int    `json:"version"`            // starts at 1
	Decision  string `json:"decision,omitempty"` // approve|request_changes|quit|enter
	Notes     string `json:"notes,omitempty"`    //
	Source    string `json:"source,omitempty"`   // user|model
	Snapshot  string `json:"snapshot,omitempty"` // store-root-relative snapshot path
	CreatedAt string `json:"created_at"`         // RFC3339Nano UTC
}

// Record is the indexed metadata of one plan artifact.
type Record struct {
	ID          string  `json:"id"` // "<projectSlug>/<planName>"
	SessionID   string  `json:"session_id,omitempty"`
	ProjectSlug string  `json:"project_slug"`
	ProjectPath string  `json:"project_path,omitempty"`
	PlanPath    string  `json:"plan_path,omitempty"`
	Title       string  `json:"title,omitempty"`
	Status      Status  `json:"status"`
	Version     int     `json:"version"`
	Rounds      []Round `json:"rounds,omitempty"`
	CreatedAt   string  `json:"created_at"`
	UpdatedAt   string  `json:"updated_at"`
}

// DefaultRoot returns the default store root: "$HOME/.aicli/plans", falling
// back to the CWD-relative ".aicli/plans" when no home directory resolves.
func DefaultRoot() string {
	if home, err := os.UserHomeDir(); err == nil && strings.TrimSpace(home) != "" {
		return filepath.Join(home, ".aicli", "plans")
	}
	return filepath.Join(".aicli", "plans")
}

// Store is a concurrency-safe plan artifact store rooted at one directory.
type Store struct {
	mu   sync.Mutex
	root string
}

// NewStore opens (or lazily creates on first write) a store at root. An empty
// root falls back to DefaultRoot.
func NewStore(root string) *Store {
	root = strings.TrimSpace(root)
	if root == "" {
		root = DefaultRoot()
	}
	return &Store{root: filepath.Clean(root)}
}

// Root returns the resolved store root directory.
func (s *Store) Root() string {
	return s.root
}

// RecordOptions describes the metadata upsert performed by Record.
type RecordOptions struct {
	ID          string // empty means IDFor(ProjectPath, PlanPath)
	SessionID   string
	ProjectPath string
	PlanPath    string
	Title       string
}

// Record upserts the metadata of a plan record and returns the stored copy.
//
// A record that does not exist yet starts as StatusPending with Version 0. For
// an existing record only the metadata fields are touched: Status, Version and
// Rounds are preserved, and an empty option field keeps its previous value.
func (s *Store) Record(opts RecordOptions) (Record, error) {
	id := strings.TrimSpace(opts.ID)
	if id == "" {
		id = IDFor(opts.ProjectPath, opts.PlanPath)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return Record{}, err
	}

	now := nowStamp()
	i, ok := indexFind(idx, id)
	if !ok {
		rec := Record{
			ID:          id,
			SessionID:   strings.TrimSpace(opts.SessionID),
			ProjectSlug: projectSlugFromID(id),
			ProjectPath: strings.TrimSpace(opts.ProjectPath),
			PlanPath:    strings.TrimSpace(opts.PlanPath),
			Title:       strings.TrimSpace(opts.Title),
			Status:      StatusPending,
			CreatedAt:   now,
			UpdatedAt:   now,
		}
		idx.Records = append(idx.Records, rec)
		if err := s.saveIndex(idx); err != nil {
			return Record{}, err
		}
		return cloneRecord(rec), nil
	}

	rec := idx.Records[i]
	if v := strings.TrimSpace(opts.SessionID); v != "" {
		rec.SessionID = v
	}
	if v := strings.TrimSpace(opts.ProjectPath); v != "" {
		rec.ProjectPath = v
	}
	if v := strings.TrimSpace(opts.PlanPath); v != "" {
		rec.PlanPath = v
	}
	if v := strings.TrimSpace(opts.Title); v != "" {
		rec.Title = v
	}
	if rec.ProjectSlug == "" {
		rec.ProjectSlug = projectSlugFromID(rec.ID)
	}
	rec.UpdatedAt = now
	idx.Records[i] = rec
	if err := s.saveIndex(idx); err != nil {
		return Record{}, err
	}
	return cloneRecord(rec), nil
}

// SnapshotOptions describes one appended review round.
type SnapshotOptions struct {
	ID       string
	Decision string
	Notes    string
	Source   string
	Content  []byte // plan body written to the version file
	Status   Status // empty keeps the current status
}

// Snapshot appends one round to an existing record: the plan body is written
// to versions/<projectSlug>/<planName>-v<N>.md, Version is bumped, a Round is
// appended and UpdatedAt is refreshed. The record must already exist, and an
// empty Status keeps the stored status unchanged.
func (s *Store) Snapshot(opts SnapshotOptions) (Record, error) {
	id := strings.TrimSpace(opts.ID)

	s.mu.Lock()
	defer s.mu.Unlock()

	if id == "" {
		return Record{}, fmt.Errorf("%w: empty id", ErrNotFound)
	}
	idx, err := s.loadIndex()
	if err != nil {
		return Record{}, err
	}
	i, ok := indexFind(idx, id)
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}

	rec := idx.Records[i]
	now := nowStamp()
	version := rec.Version + 1
	rel := versionRelPath(rec.ID, version)

	// The snapshot file is written before the index: a crash in between leaves
	// an unreferenced file, never an index entry pointing at nothing.
	if err := writeFileAtomic(filepath.Join(s.root, filepath.FromSlash(rel)), opts.Content, 0o644); err != nil {
		return Record{}, fmt.Errorf("planstore: write snapshot %s: %w", rel, err)
	}

	if rec.ProjectSlug == "" {
		rec.ProjectSlug = projectSlugFromID(rec.ID)
	}
	if rec.CreatedAt == "" {
		rec.CreatedAt = now
	}
	rec.Version = version
	rec.Rounds = append(rec.Rounds, Round{
		Version:   version,
		Decision:  strings.TrimSpace(opts.Decision),
		Notes:     opts.Notes,
		Source:    strings.TrimSpace(opts.Source),
		Snapshot:  rel,
		CreatedAt: now,
	})
	rec.UpdatedAt = now
	if opts.Status != "" {
		rec.Status = opts.Status
	}
	idx.Records[i] = rec
	if err := s.saveIndex(idx); err != nil {
		return Record{}, err
	}
	return cloneRecord(rec), nil
}

// SetStatus updates the review status of a record.
func (s *Store) SetStatus(id string, status Status) (Record, error) {
	id = strings.TrimSpace(id)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return Record{}, err
	}
	i, ok := indexFind(idx, id)
	if !ok {
		return Record{}, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	rec := idx.Records[i]
	rec.Status = status
	rec.UpdatedAt = nowStamp()
	idx.Records[i] = rec
	if err := s.saveIndex(idx); err != nil {
		return Record{}, err
	}
	return cloneRecord(rec), nil
}

// Get returns the record with the given id. A missing record is reported as
// (Record{}, false, nil), not as an error.
func (s *Store) Get(id string) (Record, bool, error) {
	id = strings.TrimSpace(id)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return Record{}, false, err
	}
	i, ok := indexFind(idx, id)
	if !ok {
		return Record{}, false, nil
	}
	return cloneRecord(idx.Records[i]), true, nil
}

// List returns every record, newest UpdatedAt first, as independent copies.
func (s *Store) List() ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	out := make([]Record, 0, len(idx.Records))
	for _, rec := range idx.Records {
		out = append(out, cloneRecord(rec))
	}
	sort.SliceStable(out, func(i, j int) bool {
		ti, tj := parseStamp(out[i].UpdatedAt), parseStamp(out[j].UpdatedAt)
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		if out[i].UpdatedAt != out[j].UpdatedAt {
			return out[i].UpdatedAt > out[j].UpdatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// ReadVersion returns the stored snapshot of one record version.
func (s *Store) ReadVersion(id string, version int) ([]byte, error) {
	id = strings.TrimSpace(id)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	i, ok := indexFind(idx, id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return s.readVersionLocked(idx.Records[i], version)
}

// ReadLatest returns the snapshot of the highest version of a record.
func (s *Store) ReadLatest(id string) ([]byte, error) {
	id = strings.TrimSpace(id)

	s.mu.Lock()
	defer s.mu.Unlock()

	idx, err := s.loadIndex()
	if err != nil {
		return nil, err
	}
	i, ok := indexFind(idx, id)
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	rec := idx.Records[i]
	if rec.Version <= 0 {
		return nil, fmt.Errorf("%w: %s has no snapshot yet", ErrNotFound, rec.ID)
	}
	return s.readVersionLocked(rec, rec.Version)
}

// readVersionLocked resolves and reads one version file; the caller holds s.mu.
func (s *Store) readVersionLocked(rec Record, version int) ([]byte, error) {
	rel := ""
	for i := len(rec.Rounds) - 1; i >= 0; i-- {
		if rec.Rounds[i].Version == version {
			rel = rec.Rounds[i].Snapshot
			break
		}
	}
	if rel == "" {
		if version <= 0 || version > rec.Version {
			return nil, fmt.Errorf("%w: %s version %d", ErrNotFound, rec.ID, version)
		}
		rel = versionRelPath(rec.ID, version)
	}
	abs, err := s.resolveSnapshotPath(rel)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s version %d snapshot %s", ErrNotFound, rec.ID, version, rel)
		}
		return nil, fmt.Errorf("planstore: read snapshot %s: %w", rel, err)
	}
	return data, nil
}

// resolveSnapshotPath turns a stored, root-relative snapshot path into an
// absolute path and rejects anything that would escape the store root.
func (s *Store) resolveSnapshotPath(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New("planstore: empty snapshot path")
	}
	native := filepath.FromSlash(rel)
	if filepath.IsAbs(native) {
		return "", fmt.Errorf("planstore: absolute snapshot path %q", rel)
	}
	abs := filepath.Join(s.root, native)
	inside, err := filepath.Rel(s.root, abs)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("planstore: snapshot path %q escapes store root", rel)
	}
	return abs, nil
}

// nowStamp renders the current time as RFC3339Nano UTC.
func nowStamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}

// parseStamp parses a stored timestamp, returning the zero time when the value
// is empty or malformed.
func parseStamp(value string) time.Time {
	if value == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts
		}
	}
	return time.Time{}
}

// cloneRecord returns a copy that shares no mutable state with the stored one.
func cloneRecord(rec Record) Record {
	if rec.Rounds != nil {
		rec.Rounds = append([]Round(nil), rec.Rounds...)
	}
	return rec
}
