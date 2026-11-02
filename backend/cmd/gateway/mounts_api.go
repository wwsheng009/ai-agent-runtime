package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/audit"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/mcpmount"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/tunnel"
	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/wire"
)

// mountManager owns the gateway side of the MCP mount namespace: the persisted
// registry, the mount token store and the public handler
// (docs/gateway/16-mcp-mount-tunnel.md).
type mountManager struct {
	registry *mcpmount.PersistentRegistry
	tokens   *mcpmount.PersistentTokenStore
	handler  *mcpmount.Handler
	style    string
	baseURL  string
	tokenTTL time.Duration
	audit    *audit.Log
	metrics  *gatewayMetrics
	logf     func(format string, args ...any)

	// reqLog is an in-memory ring of recent mount requests. Unlike audit.log
	// (security events, 4xx/5xx only) it keeps successful calls too, so the
	// console can answer "did the request reach the gateway, and what did it
	// return?" without shell access. It is deliberately not persisted: the
	// gateway must not store request history on disk.
	logMu     sync.Mutex
	reqLog    []mountLogEntry
	reqLogMax int
}

// mountLogEntry is one recent mount request. TokenID is the public token id
// (never the secret); the bearer value is never captured.
type mountLogEntry struct {
	Time       time.Time `json:"time"`
	Slug       string    `json:"slug"`
	NodeID     string    `json:"node_id,omitempty"`
	TokenID    string    `json:"token_id,omitempty"`
	Method     string    `json:"method"`
	Path       string    `json:"path"`
	Status     int       `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	DurationMS int64     `json:"duration_ms"`
	RemoteAddr string    `json:"remote_addr,omitempty"`
	// Command is the MCP method parsed from the request (e.g. tools/call:read_file).
	Command string `json:"command,omitempty"`
	// UserAgent is the caller's User-Agent header.
	UserAgent string `json:"user_agent,omitempty"`
	// RequestBytes / ResponseBytes are the proxied payload sizes (best-effort).
	RequestBytes  int64 `json:"request_bytes,omitempty"`
	ResponseBytes int64 `json:"response_bytes,omitempty"`
}

// newMountManager opens the persisted state and builds the public handler.
func newMountManager(
	dataDir string,
	style string,
	publicBaseURL string,
	tokenTTL time.Duration,
	proxy mcpmount.Proxy,
	nodeLive func(string) bool,
	clientIP func(*http.Request) string,
	metrics *gatewayMetrics,
	auditLog *audit.Log,
	logf func(format string, args ...any),
) (*mountManager, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	registry, err := mcpmount.OpenRegistry(filepath.Join(dataDir, mcpmount.RegistryFileName))
	if err != nil {
		return nil, fmt.Errorf("open mount registry: %w", err)
	}
	tokens, err := mcpmount.OpenTokenStore(filepath.Join(dataDir, mcpmount.TokensFileName))
	if err != nil {
		return nil, fmt.Errorf("open mount token store: %w", err)
	}
	manager := &mountManager{
		registry:  registry,
		tokens:    tokens,
		style:     mcpmount.NormalizePathStyle(style),
		baseURL:   strings.TrimRight(strings.TrimSpace(publicBaseURL), "/"),
		tokenTTL:  tokenTTL,
		audit:     auditLog,
		metrics:   metrics,
		logf:      logf,
		reqLogMax: 1000,
	}
	manager.handler = mcpmount.NewHandler(mcpmount.HandlerConfig{
		Registry: registry.Registry,
		Tokens:   tokens.TokenStore,
		Proxy:    proxy,
		NodeLive: nodeLive,
		ClientIP: clientIP,
		OnEvent:  manager.observe,
	})
	return manager, nil
}

// observe feeds one mount request into metrics, the recent-request ring and the
// audit log. Successes are kept only in the ring; audit.log stays sparse.
func (m *mountManager) observe(event mcpmount.Event) {
	if m.metrics != nil {
		m.metrics.mountRequests.Add(1)
		if event.Status == http.StatusUnauthorized ||
			(event.Status == http.StatusForbidden && event.Reason == "token_scope") {
			m.metrics.mountAuthFailed.Add(1)
		}
	}
	m.recordRequest(event)
	if m.audit == nil || event.Status < 400 {
		return
	}
	m.audit.Record(audit.Event{
		Type:   audit.TypeMountRequest,
		Result: event.Reason,
		IP:     remoteHost(event.RemoteAddr),
		Detail: fmt.Sprintf("%s %s %s -> %d%s", event.Slug, event.Method, event.Path, event.Status, mcpCommandSuffix(event.Command)),
	})
}

// mcpCommandSuffix renders the MCP command into the audit detail only when one
// was parsed, keeping security-event lines concise otherwise.
func mcpCommandSuffix(command string) string {
	if command == "" {
		return ""
	}
	return " " + command
}

// recordRequest appends one observation to the in-memory ring.
func (m *mountManager) recordRequest(event mcpmount.Event) {
	entry := mountLogEntry{
		Time:          time.Now().UTC(),
		Slug:          event.Slug,
		NodeID:        event.NodeID,
		TokenID:       event.TokenID,
		Method:        event.Method,
		Path:          event.Path,
		Status:        event.Status,
		Reason:        event.Reason,
		DurationMS:    event.Duration.Milliseconds(),
		RemoteAddr:    remoteHost(event.RemoteAddr),
		Command:       event.Command,
		UserAgent:     event.UserAgent,
		RequestBytes:  event.RequestBytes,
		ResponseBytes: event.ResponseBytes,
	}
	m.logMu.Lock()
	defer m.logMu.Unlock()
	m.reqLog = append(m.reqLog, entry)
	if m.reqLogMax <= 0 {
		m.reqLogMax = 1000
	}
	if len(m.reqLog) > m.reqLogMax {
		m.reqLog = append([]mountLogEntry(nil), m.reqLog[len(m.reqLog)-m.reqLogMax:]...)
	}
}

// prepareMounts validates and records a node's mount declarations. Records are
// inserted stale: REGISTER_ACK has not been written and the session is not in the
// table yet, so nothing may route to them.
func (m *mountManager) prepareMounts(nodeID, nodeStable string, routes []string, specs []wire.MountSpec) ([]wire.MountAck, []wire.MountAck) {
	if len(specs) == 0 {
		return nil, nil
	}
	if strings.TrimSpace(nodeStable) == "" {
		rejected := make([]wire.MountAck, 0, len(specs))
		for _, spec := range specs {
			rejected = append(rejected, wire.MountAck{Slug: strings.TrimSpace(spec.Slug), Reason: "node_identity_missing"})
		}
		return nil, rejected
	}

	accepted := make([]wire.MountAck, 0, len(specs))
	rejected := make([]wire.MountAck, 0)
	acceptedSlugs := make(map[string]bool, len(specs))
	changed := false
	for _, spec := range specs {
		slug := strings.TrimSpace(spec.Slug)
		if err := mcpmount.ValidateSlug(slug); err != nil {
			rejected = append(rejected, wire.MountAck{Slug: slug, Reason: "mount_slug_invalid"})
			m.recordMountRejected(slug, nodeID, "mount_slug_invalid")
			continue
		}
		localPath, err := mountLocalPath(spec.Upstream)
		if err != nil {
			rejected = append(rejected, wire.MountAck{Slug: slug, Reason: "mount_upstream_invalid"})
			m.recordMountRejected(slug, nodeID, "mount_upstream_invalid")
			continue
		}
		if !tunnel.RouteAllowed(routes, localPath) {
			// The node must declare the local path it exposes. Accepting the
			// mount without it would publish an endpoint that 403s on first use.
			rejected = append(rejected, wire.MountAck{Slug: slug, Reason: "mount_route_missing"})
			m.recordMountRejected(slug, nodeID, "mount_route_missing")
			continue
		}
		record := &mcpmount.Record{
			Slug:       slug,
			MountID:    strings.TrimSpace(nodeStable) + "/" + mountSpecID(spec, slug),
			NodeID:     nodeID,
			NodeStable: nodeStable,
			Kind:       firstNonEmpty(spec.Kind, "mcp"),
			Name:       strings.TrimSpace(spec.Name),
			LocalPath:  localPath,
			Labels:     sanitizeMountLabels(spec.Labels),
			Limits:     mountLimits(spec.Limits),
			Stale:      true,
		}
		if err := m.registry.Upsert(record); err != nil {
			rejected = append(rejected, wire.MountAck{Slug: slug, Reason: "mount_slug_conflict"})
			m.recordMountRejected(slug, nodeID, "mount_slug_conflict")
			continue
		}
		changed = true
		acceptedSlugs[slug] = true
		accepted = append(accepted, wire.MountAck{
			Slug:      slug,
			PublicURL: mcpmount.PublicURL(m.baseURL, m.style, slug, localPath),
		})
		if m.audit != nil {
			m.audit.Record(audit.Event{Type: audit.TypeMountRegistered, Detail: slug + " node=" + nodeID})
		}
	}
	// A mount removed from the node's config must stop being advertised. Keep
	// the record (stale) so a client that still holds the URL gets 502 rather
	// than a surprising 404, and let the console's stale cleanup remove it.
	for _, record := range m.registry.List() {
		if record.NodeStable != nodeStable || acceptedSlugs[record.Slug] {
			continue
		}
		if m.registry.MarkStale(record.Slug) {
			changed = true
		}
	}
	if changed {
		if err := m.registry.Save(); err != nil {
			m.logf("mounts: persist registry failed: %v", err)
		}
	}
	if len(rejected) == 0 {
		rejected = nil
	}
	return accepted, rejected
}

// Mount labels are display-only and come from a remote node, so the gateway
// bounds them before persisting/listing. The caps keep mounts.json and the
// console API small even if a node sends an oversized or duplicated list.
const (
	maxMountLabelShort      = 128
	maxMountLabelHostname   = 255
	maxMountLabelWorkspace  = 1024
	maxMountLabelToolItems  = 128
	maxMountLabelToolLength = 128
	maxMountLabelRootItems  = 32
	maxMountLabelRootLength = 1024
)

func sanitizeMountLabels(in *wire.MountLabels) *wire.MountLabels {
	if in == nil {
		return nil
	}
	out := *in
	out.Profile = clipMountLabel(in.Profile, 32)
	out.ToolPrefix = clipMountLabel(in.ToolPrefix, maxMountLabelShort)
	out.TargetName = clipMountLabel(in.TargetName, maxMountLabelShort)
	out.Workspace = clipMountLabel(in.Workspace, maxMountLabelWorkspace)
	out.Hostname = clipMountLabel(in.Hostname, maxMountLabelHostname)
	out.OS = clipMountLabel(in.OS, maxMountLabelShort)
	out.Arch = clipMountLabel(in.Arch, maxMountLabelShort)
	out.Version = clipMountLabel(in.Version, maxMountLabelShort)
	out.Instance = clipMountLabel(in.Instance, maxMountLabelShort)
	out.Backend = clipMountLabel(in.Backend, maxMountLabelShort)
	out.Tools = sanitizeMountLabelList(in.Tools, maxMountLabelToolItems, maxMountLabelToolLength)
	out.Roots = sanitizeMountLabelList(in.Roots, maxMountLabelRootItems, maxMountLabelRootLength)
	if out.ToolCount < len(out.Tools) {
		out.ToolCount = len(out.Tools)
	}
	if out.ToolCount < 0 {
		out.ToolCount = 0
	}
	return &out
}

func sanitizeMountLabelList(values []string, maxItems, maxLength int) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(values), maxItems))
	seen := make(map[string]bool, len(out))
	for _, raw := range values {
		value := clipMountLabel(raw, maxLength)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) >= maxItems {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func clipMountLabel(value string, max int) string {
	value = strings.TrimSpace(value)
	if value == "" || max <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max]) + "…"
}

// activateMounts marks this node's mounts live once its session is registered.
func (m *mountManager) activateMounts(nodeID, nodeStable string) {
	changed := false
	for _, record := range m.registry.List() {
		if record.NodeStable != nodeStable {
			continue
		}
		if m.registry.Bind(record.Slug, nodeID, time.Now().UTC()) {
			changed = true
		}
	}
	if changed {
		_ = m.registry.Save()
	}
}

// deactivateMounts marks this node's mounts stale on disconnect. The public URLs
// stay reserved so clients see 502 (retry) instead of 404 (misconfigured).
func (m *mountManager) deactivateMounts(nodeID, nodeStable string) {
	changed := false
	for _, record := range m.registry.List() {
		if record.NodeStable != nodeStable || record.NodeID != nodeID {
			continue
		}
		if m.registry.MarkStale(record.Slug) {
			changed = true
		}
	}
	if changed {
		_ = m.registry.Save()
	}
}

// serveMounts is the public entry point. Authentication happens in the handler.
func (m *mountManager) serveMounts(w http.ResponseWriter, r *http.Request) {
	slug, local := m.splitPublicPath(r.URL.Path)
	m.handler.ServeMount(w, r, slug, local)
}

// splitPublicPath removes the namespace prefix (/m/{slug} or /{slug} in root
// style) and returns the remainder as the node's local path.
func (m *mountManager) splitPublicPath(requestPath string) (slug, local string) {
	trimmed := strings.TrimPrefix(requestPath, "/")
	if m.style != mcpmount.PathStyleRoot {
		trimmed = strings.TrimPrefix(trimmed, "m/")
	}
	head, tail, _ := strings.Cut(trimmed, "/")
	if tail == "" {
		return head, "/"
	}
	return head, "/" + tail
}

// matchesRootPath decides whether a root-style path belongs to the mount
// namespace. It matches the slug shape (not the registry) so an unknown but
// plausible mount path answers 404 JSON instead of the console's HTML fallback,
// and it never shadows a reserved gateway/console path.
func (m *mountManager) matchesRootPath(requestPath string) bool {
	if m.style != mcpmount.PathStyleRoot {
		return false
	}
	head, _, _ := strings.Cut(strings.TrimPrefix(requestPath, "/"), "/")
	if head == "" || mcpmount.IsReservedRootSegment(head) {
		return false
	}
	return mcpmount.LooksLikeSlug(head)
}

// mountLocalPath derives the node-local path from its upstream URL.
func mountLocalPath(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("mount upstream %q must be an absolute http(s) URL", raw)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("mount upstream %q must use http or https", raw)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("mount upstream %q must not carry a query or fragment", raw)
	}
	local := parsed.EscapedPath()
	if local == "" {
		local = "/"
	}
	return mcpmount.NormalizeLocalPath(local)
}

func mountSpecID(spec wire.MountSpec, slug string) string {
	return strings.TrimSpace(firstNonEmpty(spec.ID, spec.Name, slug))
}

func mountLimits(limits *wire.MountLimits) wire.MountLimits {
	if limits == nil {
		return wire.MountLimits{}
	}
	return *limits
}

func (m *mountManager) recordMountRejected(slug, nodeID, reason string) {
	if m.audit == nil {
		return
	}
	m.audit.Record(audit.Event{Type: audit.TypeMountRejected, Result: reason, Detail: slug + " node=" + nodeID})
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(remoteAddr))
	if err != nil {
		return strings.TrimSpace(remoteAddr)
	}
	return host
}

// ------------------------------------------------------------ admin API ----

// mountView is the console/API projection of a record.
type mountView struct {
	Slug      string            `json:"slug"`
	Name      string            `json:"name,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	NodeID    string            `json:"node_id,omitempty"`
	MountID   string            `json:"mount_id"`
	LocalPath string            `json:"local_path"`
	PublicURL string            `json:"public_url,omitempty"`
	Labels    *wire.MountLabels `json:"labels,omitempty"`
	Limits    wire.MountLimits  `json:"limits"`
	Disabled  bool              `json:"disabled"`
	Stale     bool              `json:"stale"`
	Tokens    int               `json:"tokens"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	LastSeen  time.Time         `json:"last_seen,omitempty"`
}

// handleList returns every mount with its public URL and live token count.
func (m *mountManager) handleList(w http.ResponseWriter, r *http.Request) {
	records := m.registry.List()
	views := make([]mountView, 0, len(records))
	for _, record := range records {
		views = append(views, mountView{
			Slug:      record.Slug,
			Name:      record.Name,
			Kind:      record.Kind,
			NodeID:    record.NodeID,
			MountID:   record.MountID,
			LocalPath: record.LocalPath,
			PublicURL: mcpmount.PublicURL(m.baseURL, m.style, record.Slug, record.LocalPath),
			Labels:    record.Labels,
			Limits:    record.Limits,
			Disabled:  record.Disabled,
			Stale:     record.Stale,
			Tokens:    m.tokens.CountForMount(record.Slug),
			CreatedAt: record.CreatedAt,
			UpdatedAt: record.UpdatedAt,
			LastSeen:  record.LastSeen,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"mounts": views})
}

// handleTokens lists or issues mount tokens. The plaintext is returned exactly
// once, in the create response.
func (m *mountManager) handleTokens(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(mux.Vars(r)["slug"])
	record, ok := m.registry.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount_not_found"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"slug":   slug,
			"tokens": m.tokens.ListForMount(slug),
		})
	case http.MethodPost:
		var req struct {
			Name         string   `json:"name"`
			TTLSeconds   int64    `json:"ttl_seconds"`
			NeverExpires bool     `json:"never_expires"`
			Mounts       []string `json:"mounts"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
			return
		}
		ttl := m.tokenTTL
		switch {
		case req.NeverExpires || req.TTLSeconds < 0:
			// Explicit opt-out from expiry. A negative ttl_seconds is accepted
			// as the wire-level spelling for clients that cannot send a bool.
			ttl = 0
		case req.TTLSeconds > 0:
			ttl = time.Duration(req.TTLSeconds) * time.Second
		}
		scope := append([]string{record.Slug}, req.Mounts...)
		plaintext, info, err := m.tokens.Create(req.Name, scope, ttl)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "token_create_failed", "message": err.Error()})
			return
		}
		if m.audit != nil {
			m.audit.Record(audit.Event{Type: audit.TypeMountTokenCreated, Detail: slug + " token=" + info.ID})
		}
		writeJSON(w, http.StatusCreated, map[string]any{"token": plaintext, "token_info": info})
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

// handleTokenDelete revokes (or purges, with ?purge=1) one mount token.
func (m *mountManager) handleTokenDelete(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	slug, id := strings.TrimSpace(vars["slug"]), strings.TrimSpace(vars["id"])
	token, ok := m.tokens.Get(id)
	if !ok || !token.Allows(slug) {
		// A token issued for another mount must not be revocable through this
		// mount's URL: the path is the authorization scope, not a label.
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "token_not_found"})
		return
	}
	purge := r.URL.Query().Get("purge") == "1"
	ok = false
	if purge {
		ok = m.tokens.Delete(id)
	} else {
		ok = m.tokens.Revoke(id)
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "token_not_found"})
		return
	}
	if m.audit != nil {
		m.audit.Record(audit.Event{Type: audit.TypeMountTokenRevoked, Detail: slug + " token=" + id})
	}
	writeJSON(w, http.StatusOK, map[string]any{"revoked": id, "purged": purge})
}

// handleLogs returns the in-memory recent mount requests, newest first.
// query: slug (optional exact filter), limit (default 200, max 1000).
func (m *mountManager) handleLogs(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.URL.Query().Get("slug"))
	limit := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 1000 {
		limit = 1000
	}
	entries := m.recentLogs(slug, limit)
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": entries,
		"count":   len(entries),
		"limit":   limit,
	})
}

// recentLogs copies at most limit matching entries, newest first.
func (m *mountManager) recentLogs(slug string, limit int) []mountLogEntry {
	if limit <= 0 {
		limit = 200
	}
	m.logMu.Lock()
	defer m.logMu.Unlock()
	out := make([]mountLogEntry, 0, limit)
	for i := len(m.reqLog) - 1; i >= 0 && len(out) < limit; i-- {
		entry := m.reqLog[i]
		if slug != "" && entry.Slug != slug {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// handleDisable is the operator emergency stop. The flag survives reconnects: a
// node must not be able to re-enable its own mount by dialing again.
func (m *mountManager) handleDisable(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(mux.Vars(r)["slug"])
	var req struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req)
	if !m.registry.SetDisabled(slug, true) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount_not_found"})
		return
	}
	_ = m.registry.Save()
	if m.audit != nil {
		m.audit.Record(audit.Event{Type: audit.TypeMountDisabled, Detail: slug, Actor: strings.TrimSpace(req.Reason)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "disabled": true})
}

// handleEnable lifts the emergency stop.
func (m *mountManager) handleEnable(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(mux.Vars(r)["slug"])
	if !m.registry.SetDisabled(slug, false) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount_not_found"})
		return
	}
	_ = m.registry.Save()
	if m.audit != nil {
		m.audit.Record(audit.Event{Type: audit.TypeMountEnabled, Detail: slug})
	}
	writeJSON(w, http.StatusOK, map[string]any{"slug": slug, "disabled": false})
}

// handleDelete removes a mount record. Live mounts are refused unless forced:
// deleting one turns every configured client URL into a 404.
func (m *mountManager) handleDelete(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(mux.Vars(r)["slug"])
	record, ok := m.registry.Get(slug)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount_not_found"})
		return
	}
	force := r.URL.Query().Get("force") == "1"
	if !record.Stale && !force {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   "mount_live",
			"message": "disconnect the node or pass ?force=1 to remove a live mount",
		})
		return
	}
	if !m.registry.Remove(slug) {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": "mount_not_found"})
		return
	}
	_ = m.registry.Save()
	if m.audit != nil {
		m.audit.Record(audit.Event{Type: audit.TypeMountRemoved, Detail: slug})
	}
	writeJSON(w, http.StatusOK, map[string]any{"removed": slug})
}
