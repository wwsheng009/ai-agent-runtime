package agentcontrol

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

const (
	// MailboxScopeSession identifies runtime/session-scoped mailbox rows.
	MailboxScopeSession = "session"
	// MailboxScopeTeam identifies team-scoped mailbox rows.
	MailboxScopeTeam = "team"

	// MailboxSourceGlobal identifies the durable global mailbox registry.
	MailboxSourceGlobal = "global"
	// MailboxSourceRuntimeSessions identifies runtime/session mailbox rows.
	MailboxSourceRuntimeSessions = "runtime_sessions"
	// MailboxSourceTeams identifies team mailbox rows.
	MailboxSourceTeams = "teams"

	// SQLiteGlobalMailboxAttachSchema is the schema name used when a local
	// SQLite store attaches the durable global mailbox registry for an atomic
	// projection commit.
	SQLiteGlobalMailboxAttachSchema = "agent_control_registry"
)

// MailboxRecordFilter describes reads from a generic AgentControl mailbox
// registry. Implementations may be backed by runtime session rows, team rows,
// or a future single global table.
type MailboxRecordFilter struct {
	Workflow  string
	Scope     string
	SessionID string
	TeamID    string
	AfterSeq  int64
	Limit     int
}

// MailboxRecord is the storage-neutral AgentControl mailbox row. It preserves
// both registry seq and compatibility cursor fields so callers can bridge
// runtime/session mailbox and team mailbox implementations without knowing the
// backing table shape.
type MailboxRecord struct {
	Seq               int64                  `json:"seq,omitempty"`
	GlobalSeq         int64                  `json:"global_seq,omitempty"`
	Source            string                 `json:"source,omitempty"`
	SourceSeq         int64                  `json:"source_seq,omitempty"`
	Workflow          string                 `json:"workflow,omitempty"`
	Scope             string                 `json:"scope,omitempty"`
	SessionID         string                 `json:"session_id,omitempty"`
	SessionMailboxSeq int64                  `json:"session_mailbox_seq,omitempty"`
	TeamID            string                 `json:"team_id,omitempty"`
	TeamSeq           int64                  `json:"team_seq,omitempty"`
	MessageID         string                 `json:"message_id,omitempty"`
	FromAgent         string                 `json:"from_agent,omitempty"`
	ToAgent           string                 `json:"to_agent,omitempty"`
	TaskID            string                 `json:"task_id,omitempty"`
	Kind              string                 `json:"kind,omitempty"`
	Body              string                 `json:"body,omitempty"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
	CreatedAt         time.Time              `json:"created_at,omitempty"`
	AckedAt           *time.Time             `json:"acked_at,omitempty"`
}

// MailboxRegistryReader exposes generic AgentControl mailbox registry reads.
type MailboxRegistryReader interface {
	ListAgentControlMailboxRecords(ctx context.Context, filter MailboxRecordFilter) ([]MailboxRecord, error)
}

// MailboxRegistrySequencer exposes generic AgentControl mailbox registry
// high-water marks.
type MailboxRegistrySequencer interface {
	LastAgentControlMailboxRecordSeq(ctx context.Context, filter MailboxRecordFilter) (int64, error)
}

// MailboxRegistrySource combines read and sequence access for one registry
// backing store.
type MailboxRegistrySource interface {
	MailboxRegistryReader
	MailboxRegistrySequencer
}

// GlobalMailboxWriter writes source-local mailbox rows into a durable global
// registry. The write must be idempotent by source and source-local sequence.
type GlobalMailboxWriter interface {
	AppendGlobalMailboxRecord(ctx context.Context, source string, record MailboxRecord) (int64, error)
}

// GlobalMailboxPrimaryWriter writes canonical AgentControl mailbox records
// directly into the durable global registry before local compatibility
// projections are written.
type GlobalMailboxPrimaryWriter interface {
	AppendPrimaryGlobalMailboxRecord(ctx context.Context, record MailboxRecord) (MailboxRecord, error)
}

// GlobalMailboxSQLiteTxWriter supports atomic SQLite projection commits where
// a local runtime/team store attaches the durable global registry DB and writes
// both the global primary row and local compatibility row in one transaction.
type GlobalMailboxSQLiteTxWriter interface {
	AppendPrimaryGlobalMailboxRecordTx(ctx context.Context, tx *sql.Tx, schema string, record MailboxRecord) (MailboxRecord, error)
	GlobalMailboxAttachDSN() (string, bool)
}

// GlobalMailboxWakeNotifier lets transaction-based writers publish in-process
// wake notifications after the caller has committed its local transaction.
type GlobalMailboxWakeNotifier interface {
	NotifyGlobalMailboxWake(record MailboxRecord)
}

// AttachGlobalMailboxSQLiteTx attaches a durable global mailbox DB to the
// provided SQLite connection. Call the returned detach function after the
// transaction commits or rolls back.
func AttachGlobalMailboxSQLiteTx(ctx context.Context, conn *sql.Conn, writer GlobalMailboxWriter) (GlobalMailboxSQLiteTxWriter, string, func(), bool, error) {
	if conn == nil || writer == nil {
		return nil, "", func() {}, false, nil
	}
	txWriter, ok := writer.(GlobalMailboxSQLiteTxWriter)
	if !ok || txWriter == nil {
		return nil, "", func() {}, false, nil
	}
	dsn, ok := txWriter.GlobalMailboxAttachDSN()
	if !ok || strings.TrimSpace(dsn) == "" {
		return nil, "", func() {}, false, nil
	}
	schema := SQLiteGlobalMailboxAttachSchema
	if _, err := conn.ExecContext(ctx, "ATTACH DATABASE ? AS "+schema, dsn); err != nil {
		return nil, "", func() {}, false, err
	}
	detach := func() {
		_, _ = conn.ExecContext(context.Background(), "DETACH DATABASE "+schema)
	}
	return txWriter, schema, detach, true, nil
}

// GlobalMailboxRegistryStore is the durable global mailbox substrate used when
// local runtime/team sources have been materialized into one row-id space.
type GlobalMailboxRegistryStore interface {
	MailboxRegistrySource
	GlobalMailboxWriter
	GlobalMailboxPrimaryWriter
	MailboxWakeSource
	MaterializeMailboxRecords(ctx context.Context, sources []NamedMailboxRegistrySource, filter MailboxRecordFilter) (int64, error)
	Close() error
}

// MailboxProjectionRepairer backfills missing local-to-global mailbox
// projection links for source rows that were written before a global registry
// writer was configured.
type MailboxProjectionRepairer interface {
	RepairAgentControlMailboxProjection(ctx context.Context, filter MailboxRecordFilter) (int64, error)
}

// MailboxLocalProjectionRepairer backfills local runtime/team mailbox
// projection rows from durable global mailbox records.
type MailboxLocalProjectionRepairer interface {
	RepairAgentControlMailboxLocalProjection(ctx context.Context, filter MailboxRecordFilter) (int64, error)
}

// MailboxProjectionReconcileResult summarizes one bidirectional projection
// repair pass.
type MailboxProjectionReconcileResult struct {
	LocalToGlobal int64 `json:"local_to_global"`
	GlobalToLocal int64 `json:"global_to_local"`
}

// ReconcileMailboxProjections repairs both local->global backlinks and
// global->local compatibility projections for stores that implement the
// corresponding repair interfaces.
func ReconcileMailboxProjections(ctx context.Context, filter MailboxRecordFilter, stores ...interface{}) (MailboxProjectionReconcileResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	filter = filter.Normalize()
	result := MailboxProjectionReconcileResult{}
	for _, store := range stores {
		repairer, ok := store.(MailboxProjectionRepairer)
		if !ok || repairer == nil {
			continue
		}
		count, err := repairer.RepairAgentControlMailboxProjection(ctx, filter)
		if err != nil {
			return result, err
		}
		result.LocalToGlobal += count
	}
	for _, store := range stores {
		repairer, ok := store.(MailboxLocalProjectionRepairer)
		if !ok || repairer == nil {
			continue
		}
		count, err := repairer.RepairAgentControlMailboxLocalProjection(ctx, filter)
		if err != nil {
			return result, err
		}
		result.GlobalToLocal += count
	}
	return result, nil
}

// NamedMailboxRegistrySource gives one registry source a stable diagnostic
// name. It is useful for API responses and for future registry routing without
// coupling handlers to source construction details.
type NamedMailboxRegistrySource struct {
	Name   string
	Source MailboxRegistrySource
}

// ScopedMailboxSeq packs a source index and a source-local sequence into one
// stable positive cursor. It is retained for source-scoped compatibility; the
// combined registry uses CombinedMailboxSeq so lower-index sources can still
// produce newer rows after higher-index sources have advanced.
func ScopedMailboxSeq(sourceIndex int, localSeq int64) int64 {
	if sourceIndex < 0 || localSeq <= 0 {
		return 0
	}
	return int64(sourceIndex+1)<<48 | (localSeq & ((int64(1) << 48) - 1))
}

// SplitScopedMailboxSeq unpacks a cursor produced by ScopedMailboxSeq.
func SplitScopedMailboxSeq(seq int64) (int, int64, bool) {
	if seq <= 0 {
		return 0, 0, false
	}
	source := int(seq >> 48)
	if source <= 0 {
		return 0, seq, false
	}
	local := seq & ((int64(1) << 48) - 1)
	return source - 1, local, true
}

// CombinedMailboxSeq builds the scalar cursor used by CombinedMailboxRegistry.
// It is time-major with a small source/local tie breaker, which is good enough
// for the in-process bridge while runtime/team stores still use independent
// physical tables. A future single global registry table should replace this
// bridge with its own durable row id.
func CombinedMailboxSeq(sourceIndex int, localSeq int64, createdAt time.Time) int64 {
	if sourceIndex < 0 || localSeq <= 0 {
		return 0
	}
	millis := createdAt.UnixMilli()
	if millis <= 0 {
		millis = localSeq
	}
	return millis<<20 | int64(sourceIndex&0xff)<<12 | (localSeq & 0xfff)
}

// CombinedMailboxRegistry provides one read model over several mailbox
// registry sources. It is the in-process bridge used until runtime and team
// stores are backed by a single cross-process registry service.
type CombinedMailboxRegistry struct {
	Sources []MailboxRegistrySource
}

var _ MailboxRegistryReader = CombinedMailboxRegistry{}
var _ MailboxRegistrySequencer = CombinedMailboxRegistry{}

// ListAgentControlMailboxRecords reads all matching sources and returns rows
// ordered by combined cursor. When Scope is set, only sources that can satisfy
// that scope return rows; unrelated sources return empty results.
func (r CombinedMailboxRegistry) ListAgentControlMailboxRecords(ctx context.Context, filter MailboxRecordFilter) ([]MailboxRecord, error) {
	filter = filter.Normalize()
	records := make([]MailboxRecord, 0)
	for index, source := range r.Sources {
		if source == nil {
			continue
		}
		sourceFilter := filter
		sourceFilter.AfterSeq = 0
		sourceFilter.Limit = 0
		sourceRecords, err := source.ListAgentControlMailboxRecords(ctx, sourceFilter)
		if err != nil {
			return nil, err
		}
		for _, record := range sourceRecords {
			record = record.Normalize()
			record.Seq = CombinedMailboxSeq(index, record.Seq, record.CreatedAt)
			if record.Seq <= filter.AfterSeq {
				continue
			}
			records = append(records, record)
		}
	}
	sortMailboxRecords(records)
	if filter.Limit > 0 && len(records) > filter.Limit {
		records = records[:filter.Limit]
	}
	return records, nil
}

// LastAgentControlMailboxRecordSeq returns the largest combined cursor across
// matching registry sources.
func (r CombinedMailboxRegistry) LastAgentControlMailboxRecordSeq(ctx context.Context, filter MailboxRecordFilter) (int64, error) {
	filter = filter.Normalize()
	var maxSeq int64
	for index, source := range r.Sources {
		if source == nil {
			continue
		}
		sourceFilter := filter
		sourceFilter.AfterSeq = 0
		sourceFilter.Limit = 0
		records, err := source.ListAgentControlMailboxRecords(ctx, sourceFilter)
		if err != nil {
			return 0, err
		}
		for _, record := range records {
			combined := CombinedMailboxSeq(index, record.Seq, record.CreatedAt)
			if combined > maxSeq {
				maxSeq = combined
			}
		}
	}
	return maxSeq, nil
}

// GlobalMailboxRegistry provides a named global read substrate over several
// mailbox registry sources. It currently delegates to the in-process combined
// cursor bridge, but gives callers a stable registry shape that can later be
// backed by a single durable cross-process service/table.
type GlobalMailboxRegistry struct {
	Durable NamedMailboxRegistrySource
	Sources []NamedMailboxRegistrySource
}

var _ MailboxRegistryReader = GlobalMailboxRegistry{}
var _ MailboxRegistrySequencer = GlobalMailboxRegistry{}

// NewGlobalMailboxRegistry builds a global mailbox registry from named sources.
func NewGlobalMailboxRegistry(sources ...NamedMailboxRegistrySource) GlobalMailboxRegistry {
	return NewGlobalMailboxRegistryWithDurable(NamedMailboxRegistrySource{}, sources...)
}

// NewGlobalMailboxRegistryWithDurable builds a global mailbox registry backed
// by a primary durable source plus optional local materialization sources.
func NewGlobalMailboxRegistryWithDurable(durable NamedMailboxRegistrySource, sources ...NamedMailboxRegistrySource) GlobalMailboxRegistry {
	out := make([]NamedMailboxRegistrySource, 0, len(sources))
	for _, source := range sources {
		if normalized, ok := normalizeNamedMailboxRegistrySource(source); ok {
			out = append(out, normalized)
		}
	}
	if normalized, ok := normalizeNamedMailboxRegistrySource(durable); ok {
		return GlobalMailboxRegistry{Durable: normalized, Sources: out}
	}
	return GlobalMailboxRegistry{Sources: out}
}

func normalizeNamedMailboxRegistrySource(source NamedMailboxRegistrySource) (NamedMailboxRegistrySource, bool) {
	source.Name = strings.TrimSpace(source.Name)
	if source.Source == nil {
		return NamedMailboxRegistrySource{}, false
	}
	return source, true
}

// SourceNames returns the configured source names in order, omitting unnamed
// sources. It is intended for diagnostics/API responses, not cursor semantics.
func (r GlobalMailboxRegistry) SourceNames() []string {
	names := make([]string, 0, len(r.Sources)+1)
	if r.Durable.Source != nil {
		name := strings.TrimSpace(r.Durable.Name)
		if name != "" {
			names = append(names, name)
		}
	}
	for _, source := range r.Sources {
		name := strings.TrimSpace(source.Name)
		if name == "" || source.Source == nil {
			continue
		}
		names = append(names, name)
	}
	return names
}

// ListAgentControlMailboxRecords reads the durable global row-id space when it
// is configured, otherwise it falls back to the in-process combined cursor
// bridge used during migration.
func (r GlobalMailboxRegistry) ListAgentControlMailboxRecords(ctx context.Context, filter MailboxRecordFilter) ([]MailboxRecord, error) {
	if r.Durable.Source != nil {
		return r.Durable.Source.ListAgentControlMailboxRecords(ctx, filter)
	}
	return r.combined().ListAgentControlMailboxRecords(ctx, filter)
}

// LastAgentControlMailboxRecordSeq returns the durable global high-water mark
// when configured, otherwise the high-water mark across all named sources.
func (r GlobalMailboxRegistry) LastAgentControlMailboxRecordSeq(ctx context.Context, filter MailboxRecordFilter) (int64, error) {
	if r.Durable.Source != nil {
		return r.Durable.Source.LastAgentControlMailboxRecordSeq(ctx, filter)
	}
	return r.combined().LastAgentControlMailboxRecordSeq(ctx, filter)
}

func (r GlobalMailboxRegistry) combined() CombinedMailboxRegistry {
	sources := make([]MailboxRegistrySource, 0, len(r.Sources))
	for _, source := range r.Sources {
		if source.Source == nil {
			continue
		}
		sources = append(sources, source.Source)
	}
	return CombinedMailboxRegistry{Sources: sources}
}

// Normalize trims mailbox registry filter fields.
func (f MailboxRecordFilter) Normalize() MailboxRecordFilter {
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.Scope = strings.TrimSpace(f.Scope)
	f.SessionID = strings.TrimSpace(f.SessionID)
	f.TeamID = strings.TrimSpace(f.TeamID)
	return f
}

// Normalize trims mailbox registry record fields.
func (r MailboxRecord) Normalize() MailboxRecord {
	r.Source = strings.TrimSpace(r.Source)
	r.Workflow = strings.TrimSpace(r.Workflow)
	r.Scope = strings.TrimSpace(r.Scope)
	r.SessionID = strings.TrimSpace(r.SessionID)
	r.TeamID = strings.TrimSpace(r.TeamID)
	r.MessageID = strings.TrimSpace(r.MessageID)
	r.FromAgent = strings.TrimSpace(r.FromAgent)
	r.ToAgent = strings.TrimSpace(r.ToAgent)
	r.TaskID = strings.TrimSpace(r.TaskID)
	r.Kind = strings.TrimSpace(r.Kind)
	return r
}

func sortMailboxRecords(records []MailboxRecord) {
	if len(records) < 2 {
		return
	}
	for i := 1; i < len(records); i++ {
		for j := i; j > 0 && mailboxRecordLess(records[j], records[j-1]); j-- {
			records[j], records[j-1] = records[j-1], records[j]
		}
	}
}

func mailboxRecordLess(left, right MailboxRecord) bool {
	if left.Seq != right.Seq {
		return left.Seq < right.Seq
	}
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	if left.Scope != right.Scope {
		return left.Scope < right.Scope
	}
	return left.MessageID < right.MessageID
}

// MailboxWakeFilter identifies the workflow-scoped mailbox wake stream a
// scheduler or orchestrator wants to consume.
type MailboxWakeFilter struct {
	Workflow  string
	TeamID    string
	SessionID string
}

// MailboxWakeEvent is the storage-neutral mailbox wake signal exposed through
// AgentControl. Seq is scoped by the backing mailbox stream represented by the
// same filter.
type MailboxWakeEvent struct {
	Seq       int64     `json:"seq,omitempty"`
	Workflow  string    `json:"workflow,omitempty"`
	TeamID    string    `json:"team_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	MessageID string    `json:"message_id,omitempty"`
	Kind      string    `json:"kind,omitempty"`
	FromAgent string    `json:"from_agent,omitempty"`
	ToAgent   string    `json:"to_agent,omitempty"`
	TaskID    string    `json:"task_id,omitempty"`
	CreatedAt time.Time `json:"created_at,omitempty"`
}

// MailboxWakeWatcher exposes mailbox wake notifications through AgentControl
// instead of a workflow-specific mailbox watcher type.
type MailboxWakeWatcher interface {
	WatchAgentControlMailboxWake(ctx context.Context, filter MailboxWakeFilter) (<-chan MailboxWakeEvent, func())
}

// MailboxWakeSequencer exposes the durable high-water mark for an
// AgentControl mailbox wake stream.
type MailboxWakeSequencer interface {
	LastAgentControlMailboxWakeSeq(ctx context.Context, filter MailboxWakeFilter) (int64, error)
}

// MailboxWakeSource is the combined watcher/sequence substrate consumed by
// orchestrators that need durable mailbox wake semantics.
type MailboxWakeSource interface {
	MailboxWakeWatcher
	MailboxWakeSequencer
}

// Normalize trims mailbox wake filter fields.
func (f MailboxWakeFilter) Normalize() MailboxWakeFilter {
	f.Workflow = strings.TrimSpace(f.Workflow)
	f.TeamID = strings.TrimSpace(f.TeamID)
	f.SessionID = strings.TrimSpace(f.SessionID)
	return f
}

// Normalize trims mailbox wake event fields.
func (e MailboxWakeEvent) Normalize() MailboxWakeEvent {
	e.Workflow = strings.TrimSpace(e.Workflow)
	e.TeamID = strings.TrimSpace(e.TeamID)
	e.SessionID = strings.TrimSpace(e.SessionID)
	e.MessageID = strings.TrimSpace(e.MessageID)
	e.Kind = strings.TrimSpace(e.Kind)
	e.FromAgent = strings.TrimSpace(e.FromAgent)
	e.ToAgent = strings.TrimSpace(e.ToAgent)
	e.TaskID = strings.TrimSpace(e.TaskID)
	return e
}
