// Command session-dedupe removes surplus duplicate rows from the
// session_messages table of the durable session history database.
//
// The defect it repairs: the same logical message is appended repeatedly with a
// fresh message_id, producing adjacent rows whose role, content and tool-call
// signature are identical. The judgement rule is shared with the runtime via
// cmd/session-dedupe/dedupe, which mirrors chat.canonicalMessageSubstanceKey /
// chat.toolCallSignature.
//
// Safety properties:
//   - dry-run is the default; rows are only deleted with -apply.
//   - -session is mandatory and the scan/delete are scoped to that session id,
//     so no other session can be touched.
//   - -apply re-reads and re-plans inside a single IMMEDIATE transaction, then
//     deletes surplus rows by primary key (session_id, seq). A retained row can
//     never enter the delete set, and any delete that does not affect exactly
//     one row aborts the whole transaction.
//   - the sessions table is not modified: message_count keeps its pre-existing
//     value, which only makes future seq allocation skip numbers and can never
//     collide with an existing row.
//
// The tool is safe to run while runtime-server holds the database: it uses the
// same busy-timeout/immediate-transaction DSN options as the runtime, scans
// through a PRAGMA query_only connection, and never rewrites unrelated rows.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/cmd/session-dedupe/dedupe"
	_ "github.com/wwsheng009/ai-agent-runtime/internal/sqlitedriver"
)

const (
	defaultWindowSeconds = 3600
	// previewGroupLimit caps how many duplicate groups are printed before an
	// -apply run touches the database.
	previewGroupLimit = 20
	// busyTimeout mirrors the runtime handler so a concurrent writer only makes
	// this tool wait, never fail instantly.
	busyTimeout = 5 * time.Second
)

type options struct {
	dbPath    string
	sessionID string
	window    time.Duration
	apply     bool
	jsonOut   bool
	anyTurn   bool
}

// ruleFromOptions is the single place that turns the flags into the planner
// rule, so the preview and the delete transaction can never disagree.
func ruleFromOptions(opts options) dedupe.Rule {
	return dedupe.Rule{Window: opts.window, AnyTurn: opts.anyTurn}
}

// report is the machine readable summary (-json) and the source of the text
// report, so both views always agree.
type report struct {
	DB                 string        `json:"db"`
	SessionID          string        `json:"session_id"`
	WindowSeconds      int64         `json:"window_seconds"`
	Apply              bool          `json:"apply"`
	ScannedRows        int           `json:"scanned_rows"`
	DuplicateGroups    int           `json:"duplicate_groups"`
	SurplusRows        int           `json:"surplus_rows"`
	DeletedRows        int           `json:"deleted_rows"`
	SkippedRows        int           `json:"skipped_rows"`
	MissingPayloadRows int           `json:"missing_payload_rows"`
	AnyTurn            bool          `json:"any_turn"`
	PlanChanged        bool          `json:"plan_changed"`
	Groups             []groupReport `json:"groups"`
}

type groupReport struct {
	KeepSeq       int64   `json:"keep_seq"`
	KeepCreatedAt string  `json:"keep_created_at"`
	DropSeqs      []int64 `json:"drop_seqs"`
}

// scanResult is the raw transcript projection plus the number of rows whose
// payload could not be materialized (large bodies live in artifact files).
type scanResult struct {
	Rows           []dedupe.Row
	MissingPayload int
}

// queryer is implemented by both *sql.DB and *sql.Tx, so the scan can run
// against the scan handle and inside the delete transaction unchanged.
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "session-dedupe: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return execute(context.Background(), opts, stdout, stderr)
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	defaultDB, err := defaultDatabasePath()
	if err != nil {
		return opts, err
	}
	windowSeconds := defaultWindowSeconds
	fs := flag.NewFlagSet("session-dedupe", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		fmt.Fprintf(stderr, "usage: go run ./cmd/session-dedupe -session <session-id> [flags]\n\n")
		fs.PrintDefaults()
	}
	fs.StringVar(&opts.dbPath, "db", defaultDB, "session history database path (default: <user home>/.aicli/sessions/session_history.sqlite)")
	fs.StringVar(&opts.sessionID, "session", "", "session id to repair (required)")
	fs.IntVar(&windowSeconds, "window", defaultWindowSeconds, "duplicate window in seconds; only rows within this gap of the retained row are folded")
	fs.BoolVar(&opts.apply, "apply", false, "delete surplus rows in one transaction (default: dry-run)")
	fs.BoolVar(&opts.anyTurn, "any-turn", false, "also fold equal-substance neighbours from different turns (can delete a prompt the user really sent twice)")
	fs.BoolVar(&opts.jsonOut, "json", false, "emit a JSON summary on stdout")
	if err := fs.Parse(args); err != nil {
		return opts, err
	}
	if windowSeconds < 0 {
		return opts, fmt.Errorf("-window must not be negative (got %d)", windowSeconds)
	}
	opts.window = time.Duration(windowSeconds) * time.Second
	opts.sessionID = strings.TrimSpace(opts.sessionID)
	opts.dbPath = strings.TrimSpace(opts.dbPath)
	if opts.sessionID == "" {
		return opts, errors.New("-session is required")
	}
	if opts.dbPath == "" {
		return opts, errors.New("-db must not be empty")
	}
	info, err := os.Stat(opts.dbPath)
	if err != nil {
		return opts, fmt.Errorf("session history database %s: %w", opts.dbPath, err)
	}
	if info.IsDir() {
		return opts, fmt.Errorf("session history database %s is a directory", opts.dbPath)
	}
	return opts, nil
}

// defaultDatabasePath resolves the runtime default
// (<user home>/.aicli/sessions/session_history.sqlite) without hardcoding a
// user name.
func defaultDatabasePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	return filepath.Join(home, ".aicli", "sessions", "session_history.sqlite"), nil
}

func execute(ctx context.Context, opts options, stdout, stderr io.Writer) error {
	// The preview scan always runs on a query_only handle, so a dry-run cannot
	// write even if a later code path were to drift. The handle is closed before
	// an -apply run opens the writer so the delete transaction never has to
	// share the file with an idle reader.
	scan, err := scanReadOnly(ctx, opts)
	if err != nil {
		return err
	}
	plan := dedupe.BuildWithRule(scan.Rows, ruleFromOptions(opts))
	summary := buildReport(opts, scan, plan)

	if !opts.apply {
		return writeReport(stdout, summary, 0, opts.jsonOut)
	}

	// Print the preview before any row is touched.
	preview := summary
	if opts.jsonOut {
		fmt.Fprintln(stderr, "session-dedupe: preview before apply (first 20 duplicate groups)")
		if err := writeReport(stderr, preview, previewGroupLimit, true); err != nil {
			return err
		}
	} else if err := writeReport(stdout, preview, previewGroupLimit, false); err != nil {
		return err
	}

	result, err := applyPlan(ctx, opts, plan)
	if err != nil {
		return err
	}
	summary.DeletedRows = result.Deleted
	summary.PlanChanged = result.PlanChanged
	return writeReport(stdout, summary, 0, opts.jsonOut)
}

// scanReadOnly reads the transcript through a short-lived query_only handle.
func scanReadOnly(ctx context.Context, opts options) (scanResult, error) {
	db, err := openSQLite(opts.dbPath, true)
	if err != nil {
		return scanResult{}, err
	}
	defer db.Close()
	return scanSession(ctx, db, opts.sessionID, opts.dbPath)
}

// buildReport projects the primary scan and its plan into the report used by
// both output modes.
func buildReport(opts options, scan scanResult, plan dedupe.Plan) report {
	// The planner keeps one group per retained row so a run of duplicates can
	// chain onto its anchor; only the anchors that actually carry surplus rows
	// are duplicate groups. Projecting the anchors verbatim claimed that every
	// row duplicates (a healthy 2651-row session reported 2641 groups and a
	// 10-row repair list), which buries the real finding.
	groups := make([]groupReport, 0, len(plan.Groups))
	for _, group := range plan.Groups {
		if len(group.DropSeqs) == 0 {
			continue
		}
		groups = append(groups, groupReport{
			KeepSeq:       group.KeepSeq,
			KeepCreatedAt: group.KeepAt.UTC().Format(time.RFC3339Nano),
			DropSeqs:      group.DropSeqs,
		})
	}
	return report{
		DB:                 opts.dbPath,
		SessionID:          opts.sessionID,
		WindowSeconds:      int64(opts.window / time.Second),
		Apply:              opts.apply,
		ScannedRows:        plan.Scanned,
		DuplicateGroups:    len(groups),
		SurplusRows:        plan.SurplusCount(),
		SkippedRows:        plan.Skipped,
		MissingPayloadRows: scan.MissingPayload,
		AnyTurn:            opts.anyTurn,
		Groups:             groups,
	}
}

type applyResult struct {
	Deleted     int
	PlanChanged bool
}

// applyPlan deletes the surplus rows of one session inside a single IMMEDIATE
// transaction.
//
// The plan is rebuilt from a fresh read inside the transaction, so the delete
// set always matches the rows that exist at delete time: rows appended by a
// running runtime-server between the preview and the delete can only widen the
// plan, never invalidate it, and a concurrently removed row aborts the whole
// transaction instead of silently deleting the wrong row.
func applyPlan(ctx context.Context, opts options, previewPlan dedupe.Plan) (applyResult, error) {
	db, err := openSQLite(opts.dbPath, false)
	if err != nil {
		return applyResult{}, err
	}
	defer db.Close()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return applyResult{}, fmt.Errorf("begin delete transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	scan, err := scanSession(ctx, tx, opts.sessionID, opts.dbPath)
	if err != nil {
		return applyResult{}, err
	}
	plan := dedupe.BuildWithRule(scan.Rows, ruleFromOptions(opts))
	retained := make(map[int64]bool, len(plan.Groups))
	for _, group := range plan.Groups {
		retained[group.KeepSeq] = true
	}

	statement, err := tx.PrepareContext(ctx, `DELETE FROM session_messages WHERE session_id = ? AND seq = ?`)
	if err != nil {
		return applyResult{}, fmt.Errorf("prepare delete surplus row: %w", err)
	}
	defer statement.Close()

	deleted := 0
	for _, seq := range plan.SurplusSeqs {
		// Belt and braces: the planner never lists a retained seq, and this
		// guard keeps that invariant true even if the rules change later.
		if retained[seq] {
			return applyResult{}, fmt.Errorf("refusing to delete retained row seq=%d", seq)
		}
		result, err := statement.ExecContext(ctx, opts.sessionID, seq)
		if err != nil {
			return applyResult{}, fmt.Errorf("delete surplus row seq=%d: %w", seq, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return applyResult{}, fmt.Errorf("count deleted row seq=%d: %w", seq, err)
		}
		if affected != 1 {
			return applyResult{}, fmt.Errorf("delete surplus row seq=%d affected %d rows, want 1", seq, affected)
		}
		deleted++
	}
	if err := tx.Commit(); err != nil {
		return applyResult{}, fmt.Errorf("commit delete transaction: %w", err)
	}
	return applyResult{Deleted: deleted, PlanChanged: !sameSeqs(previewPlan.SurplusSeqs, plan.SurplusSeqs)}, nil
}

func sameSeqs(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// scanSession reads one session transcript, ordered by seq.
//
// Only the columns the rule needs are read; artifact_path is used as a fallback
// when payload_json is empty because the runtime spilled a large body to a
// file. A row whose payload cannot be materialized is still returned (with a
// nil payload) so the planner keeps it and reports it as skipped rather than
// folding it away.
func scanSession(ctx context.Context, q queryer, sessionID, dbPath string) (scanResult, error) {
	columns, err := tableColumns(ctx, q, "session_messages")
	if err != nil {
		return scanResult{}, err
	}
	for _, required := range []string{"session_id", "seq", "payload_json", "created_at"} {
		if !columns[required] {
			return scanResult{}, fmt.Errorf("table session_messages is missing column %s", required)
		}
	}
	query := `SELECT seq, payload_json, created_at FROM session_messages WHERE session_id = ? ORDER BY seq ASC`
	if columns["artifact_path"] {
		query = `SELECT seq, payload_json, artifact_path, created_at FROM session_messages WHERE session_id = ? ORDER BY seq ASC`
	}
	rows, err := q.QueryContext(ctx, query, sessionID)
	if err != nil {
		return scanResult{}, fmt.Errorf("scan session %s messages: %w", sessionID, err)
	}
	defer rows.Close()

	baseDir := filepath.Dir(dbPath)
	result := scanResult{Rows: make([]dedupe.Row, 0, 256)}
	for rows.Next() {
		var (
			seq       int64
			payload   []byte
			artifact  sql.NullString
			createdAt string
		)
		if columns["artifact_path"] {
			if err := rows.Scan(&seq, &payload, &artifact, &createdAt); err != nil {
				return scanResult{}, fmt.Errorf("scan session %s messages: %w", sessionID, err)
			}
		} else if err := rows.Scan(&seq, &payload, &createdAt); err != nil {
			return scanResult{}, fmt.Errorf("scan session %s messages: %w", sessionID, err)
		}
		if len(payload) == 0 && artifact.Valid {
			if loaded, err := readArtifactPayload(baseDir, artifact.String); err == nil {
				payload = loaded
			}
		}
		if len(payload) == 0 {
			result.MissingPayload++
		}
		result.Rows = append(result.Rows, dedupe.Row{Seq: seq, CreatedAt: createdAt, Payload: payload})
	}
	if err := rows.Err(); err != nil {
		return scanResult{}, fmt.Errorf("scan session %s messages: %w", sessionID, err)
	}
	return result, nil
}

// tableColumns lists the columns of a table. It also proves the table exists,
// so an old or unrelated database fails with a readable error instead of
// "no such table".
func tableColumns(ctx context.Context, q queryer, table string) (map[string]bool, error) {
	rows, err := q.QueryContext(ctx, `PRAGMA table_info(`+table+`)`)
	if err != nil {
		return nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	defer rows.Close()

	columns := map[string]bool{}
	for rows.Next() {
		var (
			cid          int
			name         string
			columnType   string
			notNull      int
			defaultValue sql.NullString
			primaryKey   int
		)
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return nil, fmt.Errorf("inspect table %s: %w", table, err)
		}
		columns[strings.ToLower(strings.TrimSpace(name))] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect table %s: %w", table, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("table %s not found", table)
	}
	return columns, nil
}

// readArtifactPayload mirrors how the runtime resolves a spilled message body:
// relative to the directory holding the session database. Relative paths are
// additionally kept inside that directory.
func readArtifactPayload(baseDir, artifactPath string) ([]byte, error) {
	trimmed := strings.TrimSpace(artifactPath)
	if trimmed == "" {
		return nil, errors.New("empty artifact path")
	}
	resolved := filepath.FromSlash(trimmed)
	if !filepath.IsAbs(resolved) {
		if baseDir == "" {
			return nil, errors.New("artifact path without a base directory")
		}
		resolved = filepath.Join(baseDir, resolved)
		if relative, err := filepath.Rel(baseDir, resolved); err != nil || strings.HasPrefix(relative, "..") {
			return nil, fmt.Errorf("artifact path %s escapes %s", trimmed, baseDir)
		}
	}
	return os.ReadFile(resolved)
}

// openSQLite opens a single-connection handle with the runtime DSN options
// (URI form, busy timeout, IMMEDIATE transactions).
//
// The scan handle adds PRAGMA query_only, so SQLite itself rejects any write on
// it (SQLITE_READONLY) and the dry-run cannot modify the database even if a
// later code path were to drift. query_only is used instead of a mode=ro DSN on
// purpose: a read-only connection to a WAL database needs a writable -shm file
// and can fail to open once the writer is gone.
func openSQLite(path string, readOnly bool) (*sql.DB, error) {
	db, err := sql.Open("sqlite3", sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open session history database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if readOnly {
		if _, err := db.Exec("PRAGMA query_only=ON"); err != nil {
			db.Close()
			return nil, fmt.Errorf("open session history database read-only: %w", err)
		}
	}
	return db, nil
}

// sqliteDSN builds the DSN used by the runtime stores: URI form, busy timeout,
// and immediate transactions.
func sqliteDSN(path string) string {
	trimmed := strings.TrimPrefix(strings.TrimSpace(path), "file:")
	if index := strings.Index(trimmed, "?"); index >= 0 {
		trimmed = trimmed[:index]
	}
	slashed := filepath.ToSlash(trimmed)
	if !strings.HasPrefix(slashed, "/") && !isWindowsDrivePath(slashed) {
		slashed = "./" + slashed
	}
	return "file:" + slashed + "?_busy_timeout=" + strconv.FormatInt(busyTimeout.Milliseconds(), 10) + "&_txlock=immediate"
}

func isWindowsDrivePath(path string) bool {
	return len(path) >= 3 && path[1] == ':' && (path[2] == '/' || path[2] == '\\')
}

func writeReport(w io.Writer, summary report, groupLimit int, jsonOut bool) error {
	if jsonOut {
		encoded, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			return fmt.Errorf("encode JSON summary: %w", err)
		}
		if _, err := fmt.Fprintln(w, string(encoded)); err != nil {
			return err
		}
		return nil
	}

	var builder strings.Builder
	if summary.Apply {
		fmt.Fprintf(&builder, "session-dedupe apply\n")
	} else {
		fmt.Fprintf(&builder, "session-dedupe dry-run (no rows deleted; re-run with -apply after reviewing)\n")
	}
	fmt.Fprintf(&builder, "database: %s\n", summary.DB)
	fmt.Fprintf(&builder, "session: %s\n", summary.SessionID)
	fmt.Fprintf(&builder, "window: %ds\n", summary.WindowSeconds)
	fmt.Fprintf(&builder, "fold across turns: %t (equal substance from different turns is kept unless -any-turn)\n", summary.AnyTurn)
	fmt.Fprintf(&builder, "scanned rows: %d\n", summary.ScannedRows)
	fmt.Fprintf(&builder, "duplicate groups: %d\n", summary.DuplicateGroups)
	if summary.Apply {
		fmt.Fprintf(&builder, "surplus rows (planned): %d\n", summary.SurplusRows)
	} else {
		fmt.Fprintf(&builder, "surplus rows (would be deleted): %d\n", summary.SurplusRows)
	}
	fmt.Fprintf(&builder, "skipped rows (kept: unreadable payload or timestamp): %d\n", summary.SkippedRows)
	fmt.Fprintf(&builder, "missing payload rows (kept: artifact unavailable): %d\n", summary.MissingPayloadRows)

	groups := summary.Groups
	if groupLimit > 0 && len(groups) > groupLimit {
		fmt.Fprintf(&builder, "groups (first %d of %d):\n", groupLimit, len(groups))
		groups = groups[:groupLimit]
	} else {
		fmt.Fprintf(&builder, "groups (%d):\n", len(groups))
	}
	for index, group := range groups {
		fmt.Fprintf(&builder, "  group %d: keep seq=%d (%s) drop seq=%v\n",
			index+1, group.KeepSeq, group.KeepCreatedAt, group.DropSeqs)
	}
	if summary.Apply {
		fmt.Fprintf(&builder, "deleted rows: %d\n", summary.DeletedRows)
		fmt.Fprintf(&builder, "plan changed inside the delete transaction: %t\n", summary.PlanChanged)
	}
	if _, err := io.WriteString(w, builder.String()); err != nil {
		return err
	}
	return nil
}
