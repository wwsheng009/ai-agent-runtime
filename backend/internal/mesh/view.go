package mesh

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// This file is the read side of the mesh (architecture §4.2 / §4.3 / §5.4): it
// scans mesh/nodes, refines liveness with pid + heartbeat, joins bindings,
// aggregates session ownership and (optionally) probes endpoints.
//
// Two contracts matter here:
//
//   - The read path never writes (no "helpful" repair of other processes'
//     files) — §4.2 rule 4.
//   - Aggregation always covers every node: workspace is a display/filter
//     dimension only, so conflict detection never hides a node (§4.2 rule 5,
//     §5.4).

// Ownership is the per-node answer to "who serves this session" (§3.5).
type Ownership string

// Ownership values (architecture §3.5).
const (
	OwnershipOwner    Ownership = "owner"
	OwnershipPeer     Ownership = "peer"
	OwnershipConflict Ownership = "conflict"
	OwnershipNone     Ownership = "none"
)

// Reachability is the optional liveness probe result (§3.5 / §4.3).
type Reachability string

// Reachability values (architecture §3.5).
const (
	ReachabilityOK          Reachability = "ok"
	ReachabilityUnreachable Reachability = "unreachable"
	ReachabilitySkipped     Reachability = "skipped"
)

// Session states shown by the views (architecture §3.5).
const (
	SessionStateRunning = "running"
	SessionStateBusy    = "busy"
	SessionStateIdle    = "idle"
	SessionStateUnknown = "unknown"
)

// Probe defaults (architecture §4.3): the list must stay usable even when a
// node is wedged, so the probe pass is bounded by a budget and every node that
// did not get an answer inside it is reported as `skipped`, not `unreachable`.
const (
	DefaultProbeConcurrency = 8
	DefaultProbeBudget      = 3 * time.Second
	DefaultProbeTimeout     = 1 * time.Second
	probeHealthPath         = "/web/api/health"
	// probeBodyLimit bounds how much of a health response is drained so a
	// misbehaving node cannot make the reader allocate without limit.
	probeBodyLimit = 4 << 10
)

// ViewOptions tunes BuildView. The zero value is the safe default: no probe, no
// journal join, defaults for every budget.
type ViewOptions struct {
	// SelfNodeID marks which node is "me" (ownership=owner instead of peer).
	SelfNodeID string
	// Now overrides the clock (zero means NowUTC()).
	Now time.Time
	// HeartbeatTTL is the freshness window of a heartbeat (zero means
	// DefaultHeartbeatTTL).
	HeartbeatTTL time.Duration
	// Probe enables the reachability pass. Off by default: a plain list must
	// not do network I/O (§4.3).
	Probe bool
	// ProbeConcurrency / ProbeBudget / ProbeTimeout tune the probe pass.
	ProbeConcurrency int
	ProbeBudget      time.Duration
	ProbeTimeout     time.Duration
	// ProbeClient overrides the HTTP client (tests).
	ProbeClient *http.Client
	// JournalTailLimit > 0 joins the last N journal event kinds per node.
	JournalTailLimit int
	// Filter narrows the *listed* nodes only (§5.4). Nil means "no filter and
	// no filter echo"; counts, workspaces and ownership are always full-scope.
	Filter *ViewFilter
}

// AuthView is the redacted auth section of a node (§5.4 / §9.1): the token
// itself never leaves the record file.
type AuthView struct {
	Required  bool   `json:"required"`
	Mode      string `json:"mode,omitempty"`
	TokenHint string `json:"token_hint,omitempty"`
	// Token is only ever filled by RevealTokens, i.e. after the caller verified
	// the §9.1 trust boundary (`--with-token` / `?reveal_token=1` + loopback).
	// Every default path leaves it empty so a view cannot leak a token (M7).
	Token string `json:"token,omitempty"`
}

// SessionView is the session section of a node plus the derived state.
type SessionView struct {
	ID          string    `json:"id"`
	Title       string    `json:"title,omitempty"`
	State       string    `json:"state"`
	Busy        bool      `json:"busy"`
	TurnID      string    `json:"turn_id,omitempty"`
	ActivatedAt time.Time `json:"activated_at,omitempty"`
}

// NodeView is one node as the aggregation layer sees it (§4.2 / §5.4).
type NodeView struct {
	NodeID       string          `json:"node_id"`
	PID          int             `json:"pid"`
	Kind         string          `json:"kind,omitempty"`
	State        NodeState       `json:"state"`
	Reachability Reachability    `json:"reachability"`
	Endpoint     *EndpointInfo   `json:"endpoint,omitempty"`
	Auth         *AuthView       `json:"auth,omitempty"`
	Process      *ProcessInfo    `json:"process,omitempty"`
	Session      *SessionView    `json:"session,omitempty"`
	Workspace    *WorkspaceInfo  `json:"workspace,omitempty"`
	Binding      *SessionBinding `json:"binding,omitempty"`
	Ownership    Ownership       `json:"ownership"`
	HeartbeatAt  *time.Time      `json:"heartbeat_at,omitempty"`
	// AgeSec is the heartbeat age in seconds, or -1 when the node never
	// reported one.
	AgeSec      int      `json:"age_sec"`
	JournalTail []string `json:"journal_tail,omitempty"`
	// Path / Err describe the record file itself (diagnostics for unreadable
	// or unknown-schema files).
	Path string `json:"path,omitempty"`
	Err  string `json:"error,omitempty"`

	// Record is the parsed record for in-process consumers. It carries the
	// auth token, so it is never marshalled.
	Record *NodeRecord `json:"-"`
}

// MeshCounts is the unfiltered (all-scope) node census of §5.4. `conflict`
// counts *nodes* that claim a session another live node also claims.
type MeshCounts struct {
	Live     int `json:"live"`
	Stale    int `json:"stale"`
	Stopped  int `json:"stopped,omitempty"`
	Unknown  int `json:"unknown"`
	Conflict int `json:"conflict"`
}

// ViewFilter is the §5.4 output filter: `scope=self|all`, `workspace=<path>`
// and `state=all|live`. It only decides what is *listed* — the hard contract is
// "默认全量": filtering must never hide a conflict or shrink `counts`.
type ViewFilter struct {
	// Scope is FilterScopeAll (default) or FilterScopeSelf (= the reader's own
	// workspace, an alias for `workspace=<self workspace>`).
	Scope string `json:"scope"`
	// Workspace is the requested workspace list, echoed verbatim. Nil renders
	// as JSON null, matching §5.4 ("workspace": null).
	Workspace []string `json:"workspace"`
	// State is FilterStateAll (default) or FilterStateLive.
	State string `json:"state"`
}

// Filter scopes / states of §5.4.
const (
	FilterScopeAll  = "all"
	FilterScopeSelf = "self"
	FilterStateAll  = "all"
	FilterStateLive = "live"
)

// WorkspaceGroup is one workspace bucket of the view (§5.4).
type WorkspaceGroup struct {
	Path  string `json:"path"`
	Name  string `json:"name"`
	Nodes int    `json:"nodes"`
}

// MeshSelf identifies the reader inside the view (§5.4).
type MeshSelf struct {
	NodeID    string `json:"node_id"`
	SessionID string `json:"session_id,omitempty"`
}

// MeshView is the aggregated mesh state: the single source of truth behind
// `aicli-mesh ls` and `GET /web/api/mesh/peers` (§5.4, §7.5).
type MeshView struct {
	SchemaVersion int        `json:"schema_version"`
	GeneratedAt   time.Time  `json:"generated_at"`
	Root          string     `json:"root,omitempty"`
	Self          *MeshSelf  `json:"self,omitempty"`
	Counts        MeshCounts `json:"counts"`
	// Filter echoes the effective output filter (§5.4). Nil when the caller
	// asked for no filter.
	Filter     *ViewFilter      `json:"filter,omitempty"`
	Nodes      []NodeView       `json:"nodes"`
	Workspaces []WorkspaceGroup `json:"workspaces"`
}

// BuildView aggregates the mesh read-side view. It never writes to disk.
func BuildView(paths Paths, opts ViewOptions) MeshView {
	now := opts.Now
	if now.IsZero() {
		now = NowUTC()
	}
	heartbeatTTL := opts.HeartbeatTTL
	if heartbeatTTL <= 0 {
		heartbeatTTL = DefaultHeartbeatTTL
	}
	view := MeshView{
		SchemaVersion: SchemaVersion,
		GeneratedAt:   now,
		Root:          paths.Root,
	}
	files := ListNodeFiles(paths)
	nodes := make([]NodeView, 0, len(files))
	for _, file := range files {
		nodes = append(nodes, buildNodeView(paths, file, now, heartbeatTTL, opts))
	}
	selfID := strings.TrimSpace(opts.SelfNodeID)
	applyOwnership(nodes, selfID)
	sortNodeViews(nodes)
	// Census first, list second: the filter below only trims nodes[] so a
	// filtered view can never pretend the mesh is smaller than it is (§5.4).
	view.Counts = countNodeViews(nodes)
	view.Workspaces = groupWorkspaces(nodes)
	if selfID != "" {
		view.Self = &MeshSelf{NodeID: selfID, SessionID: selfSessionID(nodes, selfID)}
	}
	listed := filterNodeViews(nodes, opts.Filter, selfWorkspacePath(nodes, selfID))
	// Probe only what is listed: filtered-out nodes are not part of the answer.
	if opts.Probe {
		probeNodes(listed, opts)
	} else {
		for i := range listed {
			listed[i].Reachability = ReachabilitySkipped
		}
	}
	view.Nodes = listed
	view.Filter = opts.Filter
	return view
}

// filterNodeViews applies the §5.4 output filter. Nil (or an empty filter)
// returns the input untouched, so callers that never filter pay nothing.
func filterNodeViews(nodes []NodeView, filter *ViewFilter, selfWorkspace string) []NodeView {
	if filter == nil {
		return nodes
	}
	state := strings.ToLower(strings.TrimSpace(filter.State))
	wanted := normalizeFilterWorkspaces(filter.Workspace)
	// `scope=self` is an alias for `workspace=<本节点工作区>`; a reader without
	// a workspace resolves to the "(无工作区)" bucket instead of "everything".
	matchEmptyWorkspace := false
	if strings.ToLower(strings.TrimSpace(filter.Scope)) == FilterScopeSelf && len(wanted) == 0 {
		if selfWorkspace = strings.TrimSpace(selfWorkspace); selfWorkspace == "" {
			matchEmptyWorkspace = true
		} else {
			wanted = []string{selfWorkspace}
		}
	}
	listed := make([]NodeView, 0, len(nodes))
	for _, node := range nodes {
		if state == FilterStateLive && node.State != NodeStateLive {
			continue
		}
		switch {
		case len(wanted) > 0:
			if !workspaceIn(node, wanted) {
				continue
			}
		case matchEmptyWorkspace:
			if workspacePathOf(node) != "" {
				continue
			}
		}
		listed = append(listed, node)
	}
	return listed
}

// normalizeFilterWorkspaces trims, drops empties and de-duplicates (paths
// compare case-insensitively: Windows paths are case-insensitive).
func normalizeFilterWorkspaces(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		duplicate := false
		for _, existing := range out {
			if strings.EqualFold(existing, value) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			out = append(out, value)
		}
	}
	return out
}

// workspacePathOf returns the node's workspace path ("" when it has none).
func workspacePathOf(node NodeView) string {
	if node.Workspace == nil {
		return ""
	}
	return strings.TrimSpace(node.Workspace.Path)
}

// workspaceIn reports whether the node belongs to one of the wanted workspaces.
func workspaceIn(node NodeView, wanted []string) bool {
	path := workspacePathOf(node)
	if path == "" {
		return false
	}
	for _, candidate := range wanted {
		if strings.EqualFold(path, candidate) {
			return true
		}
	}
	return false
}

// selfWorkspacePath resolves the reader's own workspace path ("" when unknown),
// which is what `scope=self` filters by.
func selfWorkspacePath(nodes []NodeView, selfID string) string {
	if selfID == "" {
		return ""
	}
	for _, node := range nodes {
		if node.NodeID == selfID {
			return workspacePathOf(node)
		}
	}
	return ""
}

// buildNodeView turns one tolerant file read into a node view: structural state
// first, then the pid + heartbeat refinement of §4.2.
func buildNodeView(paths Paths, file NodeFile, now time.Time, heartbeatTTL time.Duration, opts ViewOptions) NodeView {
	node := NodeView{
		State:        file.State,
		Reachability: ReachabilitySkipped,
		Ownership:    OwnershipNone,
		AgeSec:       -1,
		Path:         file.Path,
		Record:       file.Record,
	}
	if file.Err != nil {
		node.Err = file.Err.Error()
	}
	record := file.Record
	if record == nil {
		// An unparsable file still names a node: keep the id derived from the
		// file name so operators can tell which record is broken.
		node.NodeID = strings.TrimSuffix(filepath.Base(file.Path), ".json")
		node.State = NodeStateUnknown
		return node
	}
	node.NodeID = record.NodeID
	node.PID = record.PID
	node.Kind = record.Kind
	node.Endpoint = record.Endpoint
	node.Workspace = record.Workspace
	if record.Process.StartedAt.IsZero() {
		node.Process = nil
	} else {
		process := record.Process
		node.Process = &process
	}
	if record.Auth != nil {
		node.Auth = &AuthView{
			Required:  record.Auth.Required,
			Mode:      record.Auth.Mode,
			TokenHint: HintToken(record.Auth.Token),
		}
	}
	if heartbeat := record.Liveness.HeartbeatAt; !heartbeat.IsZero() {
		age := int(now.Sub(heartbeat) / time.Second)
		if age < 0 {
			age = 0
		}
		node.AgeSec = age
		at := heartbeat
		node.HeartbeatAt = &at
	}
	if record.Session != nil && strings.TrimSpace(record.Session.ID) != "" {
		session := record.Session
		node.Session = &SessionView{
			ID:          session.ID,
			Title:       session.Title,
			State:       SessionStateUnknown,
			Busy:        session.Busy,
			TurnID:      session.TurnID,
			ActivatedAt: session.ActivatedAt,
		}
		if binding, ok := LoadBinding(paths, session.ID); ok {
			node.Binding = &binding
		}
	}
	if opts.JournalTailLimit > 0 {
		node.JournalTail = journalTail(paths, record.NodeID, opts.JournalTailLimit)
	}
	node.State = effectiveNodeState(file.State, record, now, heartbeatTTL)
	if node.Session != nil {
		node.Session.State = sessionState(node.State, node.Session.Busy)
	}
	return node
}

// effectiveNodeState refines the writer-declared state with what the reader can
// observe: pid liveness plus heartbeat freshness (§3.5 / §4.2). Unreadable and
// unknown-schema files stay `unknown` and never take part in ownership.
func effectiveNodeState(declared NodeState, record *NodeRecord, now time.Time, heartbeatTTL time.Duration) NodeState {
	switch declared {
	case NodeStateUnknown:
		return NodeStateUnknown
	case NodeStateStopped:
		return NodeStateStopped
	}
	if record == nil {
		return NodeStateUnknown
	}
	if !processAlive(record.PID) {
		return NodeStateStale
	}
	heartbeat := record.Liveness.HeartbeatAt
	if heartbeat.IsZero() || now.Sub(heartbeat) > heartbeatTTL {
		return NodeStateStale
	}
	return NodeStateLive
}

func sessionState(nodeState NodeState, busy bool) string {
	if nodeState != NodeStateLive {
		return SessionStateUnknown
	}
	if busy {
		return SessionStateBusy
	}
	return SessionStateRunning
}

// applyOwnership implements the aggregation rules of §4.2: only live nodes
// participate, two or more live claimants make every one of them `conflict`,
// and a single claimant is `owner` for this process and `peer` for the others.
// Workspace is deliberately ignored: ownership is judged over all nodes
// (§4.2 rule 5).
func applyOwnership(nodes []NodeView, selfNodeID string) {
	bySession := make(map[string][]int)
	for i := range nodes {
		nodes[i].Ownership = OwnershipNone
		if nodes[i].Session == nil || nodes[i].Session.ID == "" {
			continue
		}
		bySession[nodes[i].Session.ID] = append(bySession[nodes[i].Session.ID], i)
	}
	for _, indexes := range bySession {
		live := make([]int, 0, len(indexes))
		for _, index := range indexes {
			if nodes[index].State == NodeStateLive {
				live = append(live, index)
			}
		}
		switch {
		case len(live) == 0:
			// No live claimant: `none` (the binding still shows the last
			// server, which is exactly the "idle" story of §3.5).
		case len(live) == 1:
			ownership := OwnershipPeer
			if nodes[live[0]].NodeID != "" && nodes[live[0]].NodeID == selfNodeID {
				ownership = OwnershipOwner
			}
			nodes[live[0]].Ownership = ownership
		default:
			for _, index := range live {
				nodes[index].Ownership = OwnershipConflict
			}
		}
	}
}

// sortNodeViews orders the list the way §4.2 rule 3 prescribes: live first,
// then stale, then stopped, then unknown; newest heartbeat first inside a group.
func sortNodeViews(nodes []NodeView) {
	sort.SliceStable(nodes, func(i, j int) bool {
		left, right := stateRank(nodes[i].State), stateRank(nodes[j].State)
		if left != right {
			return left < right
		}
		return heartbeatOf(nodes[i]).After(heartbeatOf(nodes[j]))
	})
}

func stateRank(state NodeState) int {
	switch state {
	case NodeStateLive:
		return 0
	case NodeStateStale:
		return 1
	case NodeStateStopped:
		return 2
	default:
		return 3
	}
}

func heartbeatOf(node NodeView) time.Time {
	if node.HeartbeatAt == nil {
		return time.Time{}
	}
	return *node.HeartbeatAt
}

func countNodeViews(nodes []NodeView) MeshCounts {
	var counts MeshCounts
	for _, node := range nodes {
		switch node.State {
		case NodeStateLive:
			counts.Live++
		case NodeStateStale:
			counts.Stale++
		case NodeStateStopped:
			counts.Stopped++
		default:
			counts.Unknown++
		}
		if node.Ownership == OwnershipConflict {
			counts.Conflict++
		}
	}
	return counts
}

// groupWorkspaces buckets the (unfiltered) node list by workspace path for the
// sidebar of §5.4. Nodes without a workspace land in the "(无工作区)" bucket.
func groupWorkspaces(nodes []NodeView) []WorkspaceGroup {
	groups := make(map[string]*WorkspaceGroup)
	order := make([]string, 0, 4)
	for _, node := range nodes {
		path, name := "", "(无工作区)"
		if node.Workspace != nil {
			path = node.Workspace.Path
			if strings.TrimSpace(node.Workspace.Name) != "" {
				name = node.Workspace.Name
			} else if path != "" {
				name = path
			}
		}
		group, ok := groups[path]
		if !ok {
			group = &WorkspaceGroup{Path: path, Name: name}
			groups[path] = group
			order = append(order, path)
		}
		group.Nodes++
	}
	sort.Strings(order)
	out := make([]WorkspaceGroup, 0, len(order))
	for _, path := range order {
		out = append(out, *groups[path])
	}
	return out
}

func selfSessionID(nodes []NodeView, selfNodeID string) string {
	for _, node := range nodes {
		if node.NodeID == selfNodeID && node.Session != nil {
			return node.Session.ID
		}
	}
	return ""
}

// journalTail returns the newest `limit` event kinds of one node journal in
// chronological order (oldest first). A missing or unreadable journal is simply
// an empty tail.
//
// The file is read once and only the last lines are parsed: a node journal is
// append-only NDJSON, so the tail is the interesting part and the front of a
// rotated file must not be parsed on every view request.
func journalTail(paths Paths, nodeID string, limit int) []string {
	path := paths.JournalPath(nodeID)
	if path == "" || limit <= 0 {
		return nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	lines := strings.Split(string(data), "\n")
	tail := make([]string, 0, limit)
	for i := len(lines) - 1; i >= 0 && len(tail) < limit; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var entry JournalEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue
		}
		if kind := strings.TrimSpace(entry.Kind); kind != "" {
			tail = append(tail, kind)
		}
	}
	// Collected newest-first: flip to chronological order.
	for i, j := 0, len(tail)-1; i < j; i, j = i+1, j-1 {
		tail[i], tail[j] = tail[j], tail[i]
	}
	return tail
}

// HintToken renders the documented `0f3a…` form of §5.4 / §9.1. Short tokens
// (and empty ones) degrade to a bare ellipsis so a hint never reveals a whole
// token. Exported so the HTTP layer, the CLI and the journal share one rule.
func HintToken(token string) string {
	token = strings.TrimSpace(token)
	if token == "" {
		return ""
	}
	if len(token) < 8 {
		return "…"
	}
	return token[:4] + "…"
}

// RevealTokens copies the raw token of every node into AuthView.Token and
// returns how many were revealed.
//
// This is the *only* path that puts a token into a view (§9.1). Callers must
// have verified the trust boundary first — loopback + an explicit
// `--with-token` / `?reveal_token=1` — because the default contract is
// "视图永不含令牌" (M7).
func RevealTokens(view *MeshView) int {
	if view == nil {
		return 0
	}
	revealed := 0
	for i := range view.Nodes {
		node := &view.Nodes[i]
		if node.Auth == nil || node.Record == nil {
			continue
		}
		token := strings.TrimSpace(node.Record.Auth.Token)
		if token == "" {
			continue
		}
		node.Auth.Token = token
		revealed++
	}
	return revealed
}

// probeNodes fills Reachability for every node with an endpoint. The pass is
// bounded twice: a per-request timeout and a whole-pass budget. Anything that
// did not answer inside the budget stays `skipped` — "list usable" beats
// "probe complete" (§4.3).
func probeNodes(nodes []NodeView, opts ViewOptions) {
	concurrency := opts.ProbeConcurrency
	if concurrency <= 0 {
		concurrency = DefaultProbeConcurrency
	}
	budget := opts.ProbeBudget
	if budget <= 0 {
		budget = DefaultProbeBudget
	}
	timeout := opts.ProbeTimeout
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	client := opts.ProbeClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i := range nodes {
		nodes[i].Reachability = ReachabilitySkipped
		endpoint := nodes[i].Endpoint
		if endpoint == nil || strings.TrimSpace(endpoint.BaseURL) == "" {
			continue
		}
		if ctx.Err() != nil {
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			continue
		}
		wg.Add(1)
		go func(index int, baseURL string) {
			defer wg.Done()
			defer func() { <-sem }()
			nodes[index].Reachability = probeOne(ctx, client, baseURL)
		}(i, endpoint.BaseURL)
	}
	wg.Wait()
}

func probeOne(ctx context.Context, client *http.Client, baseURL string) Reachability {
	url := strings.TrimRight(baseURL, "/") + probeHealthPath
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return ReachabilityUnreachable
	}
	response, err := client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			// The whole-pass budget ran out while this request was in flight:
			// "not answered in time", not "known to be down".
			return ReachabilitySkipped
		}
		return ReachabilityUnreachable
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, probeBodyLimit))
	if response.StatusCode == http.StatusOK {
		return ReachabilityOK
	}
	return ReachabilityUnreachable
}
