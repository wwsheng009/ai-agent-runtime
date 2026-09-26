package planstore

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ErrInvalidComment reports a malformed review comment (empty body, bad line
// range, missing revision).
var ErrInvalidComment = errors.New("planstore: invalid review comment")

// ReviewComment is one line-anchored review comment on an archived plan
// revision.
//
// The anchor is stored as raw line numbers *plus* the anchored excerpt: keeping
// the text the reviewer actually pointed at is what lets a later reader replay
// the anchor against a rewritten revision and either move it (excerpt found
// elsewhere) or mark it orphaned — instead of silently pointing at unrelated
// lines. Comments are therefore never rewritten in place by revision churn;
// only Add/Delete change the log.
type ReviewComment struct {
	ID        string `json:"id"`
	RecordID  string `json:"record_id,omitempty"`
	Revision  int    `json:"revision"`          // anchored archived version, >= 1
	StartLine int    `json:"start_line"`        // 1-based, inclusive
	EndLine   int    `json:"end_line"`          // 1-based, inclusive, >= StartLine
	Excerpt   string `json:"excerpt,omitempty"` // anchored text at creation time
	Body      string `json:"body"`
	Author    string `json:"author,omitempty"`
	CreatedAt string `json:"created_at"`
}

// commentsRelPath is the root-relative, slash-separated comment log of one
// record: comments/<projectSlug>/<planName>.jsonl. It mirrors the versions/
// layout so pruning and deletion stay symmetric and a hand-edited index can
// never make the store write outside its root.
func commentsRelPath(id string) string {
	project, plan := splitID(id)
	return fmt.Sprintf("comments/%s/%s.jsonl", slugOr(project, "project"), slugOr(plan, "plan"))
}

// Comments returns the stored comments of a record in append order. A record
// that has no comment log yet yields an empty slice, not an error.
//
// The log is newline-delimited JSON: one malformed line fails the whole read
// (with its line number) rather than silently dropping a reviewer's comment.
func (s *Store) Comments(id string) ([]ReviewComment, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, fmt.Errorf("%w: empty id", ErrNotFound)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.readCommentsLocked(id)
}

// AppendComment stores one comment and returns the stored copy with ID and
// CreatedAt filled in. An empty ID is generated ("c-<unixnano>", suffixed on
// collision so concurrent writers never share an id).
func (s *Store) AppendComment(id string, comment ReviewComment) (ReviewComment, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return ReviewComment{}, fmt.Errorf("%w: empty id", ErrNotFound)
	}
	comment, err := normalizeReviewComment(comment)
	if err != nil {
		return ReviewComment{}, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.readCommentsLocked(id)
	if err != nil {
		return ReviewComment{}, err
	}
	if comment.RecordID == "" {
		comment.RecordID = id
	}
	if comment.CreatedAt == "" {
		comment.CreatedAt = nowStamp()
	}
	comment.ID = uniqueCommentID(comment.ID, existing)

	if err := s.writeCommentsLocked(id, append(existing, comment)); err != nil {
		return ReviewComment{}, err
	}
	return comment, nil
}

// DeleteComment removes one comment by id and reports whether it existed.
// Deleting a missing comment is not an error (idempotent), matching
// Store.Delete's contract.
func (s *Store) DeleteComment(id, commentID string) (bool, error) {
	id = strings.TrimSpace(id)
	commentID = strings.TrimSpace(commentID)
	if id == "" {
		return false, fmt.Errorf("%w: empty id", ErrNotFound)
	}
	if commentID == "" {
		return false, fmt.Errorf("%w: empty comment id", ErrInvalidComment)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	existing, err := s.readCommentsLocked(id)
	if err != nil {
		return false, err
	}
	kept := make([]ReviewComment, 0, len(existing))
	deleted := false
	for _, c := range existing {
		if c.ID == commentID {
			deleted = true
			continue
		}
		kept = append(kept, c)
	}
	if !deleted {
		return false, nil
	}
	if err := s.writeCommentsLocked(id, kept); err != nil {
		return false, err
	}
	return true, nil
}

// readCommentsLocked loads the comment log; the caller must hold s.mu.
func (s *Store) readCommentsLocked(id string) ([]ReviewComment, error) {
	rel := commentsRelPath(id)
	abs, err := s.resolveSnapshotPath(rel)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(abs)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("planstore: read comments %s: %w", rel, err)
	}
	defer func() { _ = file.Close() }()

	comments := make([]ReviewComment, 0, 4)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		var comment ReviewComment
		if err := json.Unmarshal(raw, &comment); err != nil {
			return nil, fmt.Errorf("planstore: decode comments %s line %d: %w", rel, line, err)
		}
		comments = append(comments, comment)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("planstore: scan comments %s: %w", rel, err)
	}
	return comments, nil
}

// writeCommentsLocked atomically rewrites the whole log; the caller must hold
// s.mu. Comment bodies are small and few, so a rewrite buys crash safety (readers
// never observe a half-written line) without the torn-append risk of O_APPEND.
func (s *Store) writeCommentsLocked(id string, comments []ReviewComment) error {
	rel := commentsRelPath(id)
	abs, err := s.resolveSnapshotPath(rel)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, comment := range comments {
		encoded, err := json.Marshal(comment)
		if err != nil {
			return fmt.Errorf("planstore: encode comment %s: %w", comment.ID, err)
		}
		buf.Write(encoded)
		buf.WriteByte('\n')
	}
	if err := writeFileAtomic(abs, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("planstore: write comments %s: %w", rel, err)
	}
	return nil
}

// normalizeReviewComment validates a comment and trims the fields that must not
// carry incidental whitespace.
func normalizeReviewComment(comment ReviewComment) (ReviewComment, error) {
	comment.Body = strings.TrimSpace(comment.Body)
	if comment.Body == "" {
		return ReviewComment{}, fmt.Errorf("%w: empty body", ErrInvalidComment)
	}
	if comment.Revision < 1 {
		return ReviewComment{}, fmt.Errorf("%w: revision must be >= 1", ErrInvalidComment)
	}
	if comment.StartLine < 1 {
		return ReviewComment{}, fmt.Errorf("%w: start_line must be >= 1", ErrInvalidComment)
	}
	if comment.EndLine < comment.StartLine {
		return ReviewComment{}, fmt.Errorf("%w: end_line %d is before start_line %d", ErrInvalidComment, comment.EndLine, comment.StartLine)
	}
	comment.ID = strings.TrimSpace(comment.ID)
	comment.Author = strings.TrimSpace(comment.Author)
	comment.Excerpt = strings.TrimRight(comment.Excerpt, "\r\n")
	return comment, nil
}

// uniqueCommentID returns id, or a generated one, that no existing comment
// uses.
func uniqueCommentID(id string, existing []ReviewComment) string {
	taken := make(map[string]struct{}, len(existing))
	for _, c := range existing {
		taken[c.ID] = struct{}{}
	}
	if id == "" {
		id = fmt.Sprintf("c-%d", time.Now().UTC().UnixNano())
	}
	if _, clash := taken[id]; !clash {
		return id
	}
	for suffix := 2; ; suffix++ {
		candidate := fmt.Sprintf("%s-%d", id, suffix)
		if _, clash := taken[candidate]; !clash {
			return candidate
		}
	}
}
