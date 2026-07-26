// Package memorystore implements project-scoped durable memory for aicli.
//
// MVP scope (Iteration B4):
//   - storage under <project>/.aicli/memory/notes.jsonl (or explicit root)
//   - append note / keyword search (no embeddings, no cloud sync)
//   - format top-k notes for session context injection under a token budget
//
// Session working memory remains in internal/memory (in-process observations).
package memorystore

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	// DefaultDirName is the project-relative memory directory.
	DefaultDirName = ".aicli/memory"
	// DefaultNotesFile is the append-only notes file name.
	DefaultNotesFile = "notes.jsonl"

	// DefaultSearchLimit is the default number of search hits.
	DefaultSearchLimit = 5
	// DefaultInjectLimit is the default number of notes injected into context.
	DefaultInjectLimit = 5
	// DefaultTokenBudget approximates prompt tokens reserved for project memory.
	DefaultTokenBudget = 600
)

// Note is one durable project memory entry.
type Note struct {
	ID        string    `json:"id"`
	Text      string    `json:"text"`
	Tags      []string  `json:"tags,omitempty"`
	Source    string    `json:"source,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// SearchHit is a ranked note match.
type SearchHit struct {
	Note  Note    `json:"note"`
	Score float64 `json:"score"`
}

// Config configures a project memory store.
type Config struct {
	// Root is the absolute directory that holds notes.jsonl.
	// Empty → ResolveRoot(ProjectRoot, ProfileMemoryDir).
	Root string
	// ProjectRoot is the workspace / git root used to resolve .aicli/memory.
	ProjectRoot string
	// ProfileMemoryDir is an optional profile-scoped memory directory
	// (e.g. <profile>/memory). Used when ProjectRoot is empty or as fallback.
	ProfileMemoryDir string
}

// Store is a file-backed durable project memory.
type Store struct {
	mu   sync.RWMutex
	root string
	path string
}

// ResolveRoot picks the durable memory directory.
// Preference: explicit root → <project>/.aicli/memory → profile memory dir → cwd/.aicli/memory.
func ResolveRoot(projectRoot, profileMemoryDir string) string {
	projectRoot = strings.TrimSpace(projectRoot)
	profileMemoryDir = strings.TrimSpace(profileMemoryDir)
	if projectRoot != "" {
		return filepath.Join(filepath.Clean(projectRoot), filepath.FromSlash(DefaultDirName))
	}
	if profileMemoryDir != "" {
		return filepath.Clean(profileMemoryDir)
	}
	if cwd, err := os.Getwd(); err == nil && strings.TrimSpace(cwd) != "" {
		return filepath.Join(cwd, filepath.FromSlash(DefaultDirName))
	}
	return filepath.FromSlash(DefaultDirName)
}

// New creates a store rooted at cfg.Root or a resolved default path.
func New(cfg Config) (*Store, error) {
	root := strings.TrimSpace(cfg.Root)
	if root == "" {
		root = ResolveRoot(cfg.ProjectRoot, cfg.ProfileMemoryDir)
	}
	root = filepath.Clean(root)
	if root == "" || root == "." {
		return nil, fmt.Errorf("memorystore: empty root")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("memorystore: create root %s: %w", root, err)
	}
	return &Store{
		root: root,
		path: filepath.Join(root, DefaultNotesFile),
	}, nil
}

// Root returns the store directory.
func (s *Store) Root() string {
	if s == nil {
		return ""
	}
	return s.root
}

// Path returns the notes.jsonl path.
func (s *Store) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

// AppendNoteOptions configures Append.
type AppendNoteOptions struct {
	Text      string
	Tags      []string
	Source    string
	SessionID string
	// ID optional; generated when empty.
	ID string
	// CreatedAt optional; defaults to now UTC.
	CreatedAt time.Time
}

// Append writes one note to durable storage.
func (s *Store) Append(opts AppendNoteOptions) (Note, error) {
	if s == nil {
		return Note{}, fmt.Errorf("memorystore: store is nil")
	}
	text := strings.TrimSpace(opts.Text)
	if text == "" {
		return Note{}, fmt.Errorf("memorystore: note text is required")
	}

	note := Note{
		ID:        strings.TrimSpace(opts.ID),
		Text:      text,
		Tags:      normalizeTags(opts.Tags),
		Source:    strings.TrimSpace(opts.Source),
		SessionID: strings.TrimSpace(opts.SessionID),
		CreatedAt: opts.CreatedAt.UTC(),
	}
	if note.CreatedAt.IsZero() {
		note.CreatedAt = time.Now().UTC()
	}
	if note.ID == "" {
		note.ID = newNoteID(note)
	}
	if note.Source == "" {
		note.Source = "manual"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.MkdirAll(s.root, 0o755); err != nil {
		return Note{}, fmt.Errorf("memorystore: create root: %w", err)
	}
	f, err := os.OpenFile(s.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return Note{}, fmt.Errorf("memorystore: open notes: %w", err)
	}
	defer f.Close()

	payload, err := json.Marshal(note)
	if err != nil {
		return Note{}, fmt.Errorf("memorystore: marshal note: %w", err)
	}
	if _, err := f.Write(append(payload, '\n')); err != nil {
		return Note{}, fmt.Errorf("memorystore: write note: %w", err)
	}
	return note, nil
}

// List loads notes newest-first. limit <= 0 returns all.
func (s *Store) List(limit int) ([]Note, error) {
	notes, err := s.loadAll()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(notes, func(i, j int) bool {
		return notes[i].CreatedAt.After(notes[j].CreatedAt)
	})
	if limit > 0 && len(notes) > limit {
		notes = notes[:limit]
	}
	return notes, nil
}

// SearchOptions configures keyword search.
type SearchOptions struct {
	Query string
	// Tags optional filter: note must include all listed tags (case-insensitive).
	Tags  []string
	Limit int
}

// Search ranks notes by simple keyword overlap. Empty query returns recent notes.
func (s *Store) Search(opts SearchOptions) ([]SearchHit, error) {
	notes, err := s.loadAll()
	if err != nil {
		return nil, err
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultSearchLimit
	}
	tagFilter := normalizeTags(opts.Tags)
	query := strings.TrimSpace(opts.Query)
	tokens := tokenize(query)

	hits := make([]SearchHit, 0, len(notes))
	for _, note := range notes {
		if !noteHasAllTags(note, tagFilter) {
			continue
		}
		if query == "" {
			hits = append(hits, SearchHit{Note: note, Score: float64(note.CreatedAt.Unix())})
			continue
		}
		score := scoreNote(note, tokens, query)
		if score <= 0 {
			continue
		}
		hits = append(hits, SearchHit{Note: note, Score: score})
	}

	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score == hits[j].Score {
			return hits[i].Note.CreatedAt.After(hits[j].Note.CreatedAt)
		}
		return hits[i].Score > hits[j].Score
	})
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// InjectOptions configures context injection selection.
type InjectOptions struct {
	Query       string
	Limit       int
	TokenBudget int
}

// SelectForInject returns top-k notes for session start / turn context, under a token budget.
func (s *Store) SelectForInject(opts InjectOptions) ([]Note, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultInjectLimit
	}
	budget := opts.TokenBudget
	if budget <= 0 {
		budget = DefaultTokenBudget
	}

	hits, err := s.Search(SearchOptions{Query: opts.Query, Limit: limit * 3})
	if err != nil {
		return nil, err
	}
	if len(hits) == 0 {
		return nil, nil
	}

	selected := make([]Note, 0, limit)
	used := 0
	for _, hit := range hits {
		cost := EstimateTokens(hit.Note.Text) + 8 // id/tags overhead
		if used > 0 && used+cost > budget {
			continue
		}
		if used == 0 && cost > budget {
			// Always allow a single truncated-budget note so callers can show something.
			selected = append(selected, hit.Note)
			break
		}
		selected = append(selected, hit.Note)
		used += cost
		if len(selected) >= limit {
			break
		}
	}
	return selected, nil
}

// FormatNotes builds a prompt-safe project memory block.
func FormatNotes(notes []Note, tokenBudget int) string {
	if len(notes) == 0 {
		return ""
	}
	if tokenBudget <= 0 {
		tokenBudget = DefaultTokenBudget
	}
	var b strings.Builder
	b.WriteString("Project durable memory (cross-session notes):\n")
	used := EstimateTokens(b.String())
	for i, note := range notes {
		line := formatNoteLine(i+1, note)
		cost := EstimateTokens(line)
		if used+cost > tokenBudget && i > 0 {
			break
		}
		if used+cost > tokenBudget {
			// Truncate first note body to fit budget.
			remain := tokenBudget - used - 24
			if remain < 40 {
				break
			}
			truncated := note
			truncated.Text = truncateRunes(note.Text, remain*4) // rough chars
			line = formatNoteLine(i+1, truncated)
			cost = EstimateTokens(line)
			if used+cost > tokenBudget {
				break
			}
		}
		b.WriteString(line)
		if !strings.HasSuffix(line, "\n") {
			b.WriteByte('\n')
		}
		used += cost
	}
	return strings.TrimRight(b.String(), "\n")
}

// EstimateTokens approximates tokens as utf-8 runes / 4 (min 1 for non-empty).
func EstimateTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	n := (len([]rune(text)) + 3) / 4
	if n < 1 {
		return 1
	}
	return n
}

func (s *Store) loadAll() ([]Note, error) {
	if s == nil {
		return nil, fmt.Errorf("memorystore: store is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	f, err := os.Open(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("memorystore: open notes: %w", err)
	}
	defer f.Close()

	notes := make([]Note, 0, 32)
	scanner := bufio.NewScanner(f)
	// Allow longer note bodies (default 64K is enough for most; raise to 1MB).
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var note Note
		if err := json.Unmarshal([]byte(line), &note); err != nil {
			// Skip corrupt lines rather than failing the whole store.
			continue
		}
		note.Text = strings.TrimSpace(note.Text)
		if note.Text == "" {
			continue
		}
		if note.ID == "" {
			note.ID = newNoteID(note)
		}
		note.Tags = normalizeTags(note.Tags)
		if note.CreatedAt.IsZero() {
			note.CreatedAt = time.Unix(int64(lineNo), 0).UTC()
		} else {
			note.CreatedAt = note.CreatedAt.UTC()
		}
		notes = append(notes, note)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("memorystore: read notes: %w", err)
	}
	return notes, nil
}

func formatNoteLine(index int, note Note) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d. ", index)
	if note.ID != "" {
		fmt.Fprintf(&b, "[%s] ", note.ID)
	}
	b.WriteString(strings.TrimSpace(note.Text))
	if len(note.Tags) > 0 {
		fmt.Fprintf(&b, " (tags: %s)", strings.Join(note.Tags, ", "))
	}
	if !note.CreatedAt.IsZero() {
		fmt.Fprintf(&b, " @ %s", note.CreatedAt.UTC().Format(time.RFC3339))
	}
	b.WriteByte('\n')
	return b.String()
}

func newNoteID(note Note) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "%s|%s|%s", note.CreatedAt.UTC().Format(time.RFC3339Nano), note.SessionID, note.Text)
	sum := hex.EncodeToString(h.Sum(nil))
	if len(sum) > 12 {
		sum = sum[:12]
	}
	return "mem_" + sum
}

func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		tag = strings.ToLower(strings.TrimSpace(tag))
		if tag == "" {
			continue
		}
		if _, ok := seen[tag]; ok {
			continue
		}
		seen[tag] = struct{}{}
		out = append(out, tag)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func noteHasAllTags(note Note, required []string) bool {
	if len(required) == 0 {
		return true
	}
	have := make(map[string]struct{}, len(note.Tags))
	for _, tag := range note.Tags {
		have[strings.ToLower(tag)] = struct{}{}
	}
	for _, tag := range required {
		if _, ok := have[tag]; !ok {
			return false
		}
	}
	return true
}

func tokenize(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	seen := make(map[string]struct{}, len(fields))
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		if _, ok := seen[f]; ok {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	return out
}

func scoreNote(note Note, tokens []string, rawQuery string) float64 {
	text := strings.ToLower(note.Text)
	tagBlob := strings.ToLower(strings.Join(note.Tags, " "))
	score := 0.0
	raw := strings.ToLower(strings.TrimSpace(rawQuery))
	if raw != "" && strings.Contains(text, raw) {
		score += 5
	}
	for _, tok := range tokens {
		if strings.Contains(text, tok) {
			score += 2
		}
		if strings.Contains(tagBlob, tok) {
			score += 3
		}
		if strings.EqualFold(note.Source, tok) {
			score += 1
		}
	}
	// Mild recency boost (seconds → tiny fraction).
	if !note.CreatedAt.IsZero() {
		score += float64(note.CreatedAt.Unix()%10_000) / 100_000
	}
	return score
}

func truncateRunes(text string, max int) string {
	runes := []rune(strings.TrimSpace(text))
	if max <= 0 || len(runes) <= max {
		return strings.TrimSpace(text)
	}
	if max <= 3 {
		return string(runes[:max])
	}
	return string(runes[:max-3]) + "..."
}
