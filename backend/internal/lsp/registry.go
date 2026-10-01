package lsp

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// RegistryOptions carries the injectable seams. Tests supply a Dial function
// that speaks LSP over an in-memory pipe instead of spawning processes.
type RegistryOptions struct {
	Dial          DialFunc
	Logger        Logger
	Observer      Observer
	ClientName    string
	ClientVersion string
	// Now is the injectable clock for restart-window tests.
	Now func() time.Time
	// LookPath is the executable preflight seam (nil = exec.LookPath). It is
	// consulted only for process-spawning transports: Dial-based transports
	// (tests, custom hosts) are always considered available.
	LookPath func(string) (string, error)
}

// Registry owns the pool of language server clients for one workspace root.
// Lifecycle follows the host session (ADR-0002); a server that fails to start
// is marked unavailable and skipped, never surfaced as a session error
// (docs/lsp 02 §2.2, acceptance A2/A8).
type Registry struct {
	cfg  Config
	root string
	opts RegistryOptions

	mu      sync.RWMutex
	entries map[string]*registryEntry
	order   []string
	stopped bool
	now     func() time.Time
}

type registryEntry struct {
	spec   ServerSpec
	parent *Registry

	mu            sync.Mutex
	client        *Client
	starting      bool
	lastErr       string
	restarts      int
	lastRestartAt time.Time
	startedAt     time.Time
	readyAt       time.Time
	// availability 是缺二进制预检的一次性缓存（manual restart 会失效重查）。
	availChecked bool
	available    bool
	availErr     string
}

// checkAvailable resolves and caches the executable preflight. Dial-based
// transports (tests, custom hosts) are always considered available; the
// explicit LookPath seam wins over both. The cache is invalidated by manual
// StartServer/Restart so a fresh install is picked up without a new session.
func (e *registryEntry) checkAvailable() bool {
	e.mu.Lock()
	if e.availChecked {
		available := e.available
		e.mu.Unlock()
		return available
	}
	e.mu.Unlock()

	available, reason := true, ""
	if e.parent.opts.Dial == nil {
		lookPath := e.parent.opts.LookPath
		if lookPath == nil {
			lookPath = exec.LookPath
		}
		if _, err := lookPath(e.spec.Command); err != nil {
			available = false
			reason = fmt.Sprintf("lsp: start %s: executable %q not found", e.spec.Name, e.spec.Command)
		}
	}

	e.mu.Lock()
	e.availChecked = true
	e.available = available
	e.availErr = reason
	e.mu.Unlock()
	return available
}

// availabilityReason returns the cached preflight failure text ("" when the
// member is available).
func (e *registryEntry) availabilityReason() string {
	e.checkAvailable()
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.availErr
}

// resetAvailability invalidates the cached preflight for an explicit retry.
func (e *registryEntry) resetAvailability() {
	e.mu.Lock()
	e.availChecked = false
	e.available = false
	e.availErr = ""
	e.mu.Unlock()
}

// NewRegistry builds the pool from config. Servers start lazily on first use
// (W3: routing decides who is needed), so creating a Registry is cheap.
func NewRegistry(cfg Config, root string, opts RegistryOptions) *Registry {
	cfg = cfg.Normalize()
	if strings.TrimSpace(root) == "" {
		root = "."
	}
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	registry := &Registry{
		cfg:     cfg,
		root:    root,
		opts:    opts,
		entries: make(map[string]*registryEntry, len(cfg.Servers)),
		now:     opts.Now,
	}
	if registry.now == nil {
		registry.now = time.Now
	}
	for _, spec := range cfg.Servers {
		if !spec.IsEnabled() {
			continue
		}
		registry.entries[spec.Name] = &registryEntry{spec: spec, parent: registry}
		registry.order = append(registry.order, spec.Name)
	}
	return registry
}

// Root is the workspace root every client is bound to.
func (r *Registry) Root() string { return r.root }

// DiagnosticsConfig exposes the normalized thresholds for the caller.
func (r *Registry) DiagnosticsConfig() DiagnosticsConfig { return r.cfg.Diagnostics }

// Empty reports whether no enabled server is configured.
func (r *Registry) Empty() bool {
	if r == nil {
		return true
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.entries) == 0
}

// StartAll prewarms every configured server. It is optional: first use starts
// the servers on demand.
func (r *Registry) StartAll(ctx context.Context) {
	if r == nil {
		return
	}
	for _, server := range r.Servers() {
		if !server.available() {
			continue
		}
		server.ensureStarted(ctx)
	}
}

// Servers returns the pool members in configuration order.
func (r *Registry) Servers() []*Server {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	servers := make([]*Server, 0, len(r.order))
	for _, name := range r.order {
		if entry := r.entries[name]; entry != nil {
			servers = append(servers, &Server{entry: entry})
		}
	}
	return servers
}

// ServersForPath is W3 routing: it returns exactly the members whose W1
// decision accepts the path, starting pending members in the background. A
// path that matches nothing is never opened, notified or queried (A2).
func (r *Registry) ServersForPath(path string) []*Server {
	if r == nil {
		return nil
	}
	var matched []*Server
	for _, server := range r.Servers() {
		if !server.entry.spec.HandlesFile(path) {
			continue
		}
		if !server.available() {
			// 缺二进制预检：该成员在本次会话内不再尝试启动；状态面
			// （lsp_servers / /lsp status / web）仍展示原因，manual
			// StartServer/Restart 会失效缓存重试。
			continue
		}
		server.ensureStarted(context.Background())
		matched = append(matched, server)
	}
	return matched
}

// Statuses returns the observable `lsp_servers`-shaped records (L2/A4).
func (r *Registry) Statuses() []ServerStatus {
	if r == nil {
		return nil
	}
	servers := r.Servers()
	statuses := make([]ServerStatus, 0, len(servers))
	for _, server := range servers {
		statuses = append(statuses, server.Status())
	}
	return statuses
}

// Restart is the recovery entry point (L5 "LSP 重启"): stop the named server,
// start a fresh process, and surface the failure reason when it cannot start.
func (r *Registry) Restart(ctx context.Context, name string) error {
	if r == nil {
		return fmt.Errorf("lsp: registry is not configured")
	}
	r.mu.RLock()
	entry := r.entries[strings.TrimSpace(name)]
	r.mu.RUnlock()
	if entry == nil {
		return fmt.Errorf("lsp: unknown server %q", name)
	}
	entry.resetAvailability()
	entry.stop(ctx)
	entry.mu.Lock()
	entry.restarts++
	entry.lastRestartAt = r.now()
	entry.mu.Unlock()
	return entry.start(ctx)
}

// StartServer starts the named member synchronously. It is the manual
// counterpart of the lazy first-use start (L2): an already-ready member is a
// no-op, a crashed member is replaced inside the restart budget, and a member
// whose first start failed is retried because the caller asked explicitly.
// Failure is recorded on the entry and returned; callers degrade, never abort
// the session (docs/lsp 02 §2.2).
func (r *Registry) StartServer(ctx context.Context, name string) error {
	if r == nil {
		return fmt.Errorf("lsp: registry is not configured")
	}
	r.mu.RLock()
	entry := r.entries[strings.TrimSpace(name)]
	r.mu.RUnlock()
	if entry == nil {
		return fmt.Errorf("lsp: unknown server %q", name)
	}
	// A manual start is an explicit retry: clear the recorded first-start
	// failure and the preflight cache (the binary may have been installed
	// after the session started) so entry.start is willing to try again.
	entry.resetAvailability()
	entry.mu.Lock()
	if entry.client == nil && !entry.starting {
		entry.lastErr = ""
	}
	entry.mu.Unlock()
	return entry.start(ctx)
}

// Stop shuts every managed client down. Idempotent.
func (r *Registry) Stop(ctx context.Context) {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.stopped {
		r.mu.Unlock()
		return
	}
	r.stopped = true
	r.mu.Unlock()
	for _, server := range r.Servers() {
		server.entry.stop(ctx)
	}
}

// Server is a read-only handle to one pool member.
type Server struct {
	entry *registryEntry
}

// Name is the configured server name.
func (s *Server) Name() string { return s.entry.spec.Name }

// Spec returns the server declaration.
func (s *Server) Spec() ServerSpec { return s.entry.spec }

// Client returns the running client, or nil when the server never started.
func (s *Server) Client() *Client {
	s.entry.mu.Lock()
	defer s.entry.mu.Unlock()
	return s.entry.client
}

// available reports whether the member passed the executable preflight.
func (s *Server) available() bool { return s.entry.checkAvailable() }

// Status is the merged lifecycle record of the member.
func (s *Server) Status() ServerStatus {
	entry := s.entry
	entry.mu.Lock()
	client := entry.client
	lastErr := entry.lastErr
	startedAt := entry.startedAt
	readyAt := entry.readyAt
	restarts := entry.restarts
	entry.mu.Unlock()

	if client != nil {
		status := client.Status()
		if status.Name == "" {
			status.Name = entry.spec.Name
		}
		if status.Command == "" {
			status.Command = entry.spec.Command
		}
		if status.Root == "" {
			status.Root = entry.parent.root
		}
		if len(entry.spec.Languages) > 0 {
			status.Language = strings.Join(entry.spec.Languages, ",")
		}
		status.Restarts += restarts
		// 崩溃且（自动）恢复已被拒绝：结构化原因优先，同时保留崩溃原文（live:
		// "exit status 0xffffffff"），供分类、状态页与排查共用。
		if status.State == StateCrashed && lastErr != "" {
			status.Reason = lastErr
			status.LastError = lastErr
		}
		return status
	}

	status := ServerStatus{
		Name:      entry.spec.Name,
		Command:   entry.spec.Command,
		Root:      entry.parent.root,
		StartedAt: startedAt,
		ReadyAt:   readyAt,
		Restarts:  restarts,
		LastError: lastErr,
	}
	if len(entry.spec.Languages) > 0 {
		status.Language = strings.Join(entry.spec.Languages, ",")
	}
	switch {
	case lastErr != "":
		status.State = StateUnavailable
		status.Reason = lastErr
	case !entry.checkAvailable():
		// 预检失败：成员从未启动，必须报 unavailable + 可行动原因，
		// 而不是默认的 starting（否则状态页会误导用户等待）。
		status.State = StateUnavailable
		status.Reason = entry.availabilityReason()
		status.LastError = status.Reason
	case entry.parent.isStopped():
		status.State = StateStopped
		status.Reason = "shut down"
	default:
		status.State = StateStarting
		status.Reason = "pending first use"
	}
	return status
}

// wantsStart reports whether a start (initial or crash replacement) should be
// attempted now. A member whose first start failed is not retried
// automatically; a crashed member is replaced while the restart budget lasts
// (L2, `lsp.restartLimit`).
func (s *Server) wantsStart() bool {
	if s.entry.parent.isStopped() {
		return false
	}
	entry := s.entry
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.starting {
		return false
	}
	if entry.client == nil {
		return entry.lastErr == ""
	}
	if entry.client.Status().State != StateCrashed {
		return false
	}
	// Sliding window: a crash that follows a quiet period longer than
	// restartWindow gets a fresh budget instead of leaving the member
	// degraded for the rest of the process lifetime.
	if entry.restarts > 0 && entry.parent.now().Sub(entry.lastRestartAt) > entry.parent.cfg.RestartWindowValue() {
		entry.restarts = 0
	}
	return entry.restarts < entry.parent.cfg.RestartLimitValue()
}

// recoverCrashed kicks a bounded crash replacement and reports whether waiting
// for it makes sense. It never blocks: callers wait on the observable state
// (starting → ready/unavailable) inside their own deadline.
func (s *Server) recoverCrashed(ctx context.Context) bool {
	entry := s.entry
	if entry.parent.isStopped() {
		return false
	}
	entry.mu.Lock()
	starting := entry.starting
	entry.mu.Unlock()
	if starting {
		return true
	}
	if !s.wantsStart() {
		s.recordCrashBudgetExhausted()
		return false
	}
	go func() { _ = entry.start(ctx) }()
	return true
}

// recordCrashBudgetExhausted writes the structured "crashed; restart budget
// exhausted" reason onto the entry when automatic recovery is refused. Without
// it the observable reason stays the raw process error (live: "exit status
// 0xffffffff"): ReasonCategory cannot classify it, so the request outcome falls
// back to a bare `degraded` (feeding the unclassified bucket the baseline
// tracks) and the inline note reads like a mystery rather than a crash. The
// status is the single source of truth: the bridge's degrade reason, the TUI /
// web status page and the event stream all read this text.
func (s *Server) recordCrashBudgetExhausted() {
	entry := s.entry
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if entry.client == nil || entry.client.Status().State != StateCrashed {
		return
	}
	if strings.Contains(entry.lastErr, "restart budget exhausted") {
		return
	}
	reason := fmt.Sprintf(
		"lsp: %s crashed; restart budget exhausted (restartLimit=%d)",
		entry.spec.Name, entry.parent.cfg.RestartLimitValue(),
	)
	if raw := strings.TrimSpace(entry.client.Status().Reason); raw != "" &&
		!strings.Contains(raw, "restart budget exhausted") {
		reason += ": " + raw
	}
	entry.lastErr = reason
}

// ensureStarted starts the member once, in the background. Failure is recorded
// on the entry; callers keep running.
func (s *Server) ensureStarted(ctx context.Context) {
	if !s.wantsStart() {
		return
	}
	go func() {
		_ = s.entry.start(ctx)
	}()
}

func (r *Registry) isStopped() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.stopped
}

// start performs a blocking start and records the outcome. It also replaces a
// crashed member while the automatic-recovery budget lasts (L2: the policy is
// an implementation detail, the outcome stays observable through Status() and
// the Observer seam).
func (e *registryEntry) start(ctx context.Context) error {
	e.mu.Lock()
	if e.starting {
		e.mu.Unlock()
		return nil
	}
	if e.client != nil {
		if e.client.Status().State != StateCrashed {
			e.mu.Unlock()
			return nil
		}
		if e.restarts >= e.parent.cfg.RestartLimitValue() {
			e.mu.Unlock()
			return fmt.Errorf("lsp: %s crashed; restart budget exhausted (restartLimit=%d)", e.spec.Name, e.parent.cfg.RestartLimitValue())
		}
		// Count the replacement before it starts so concurrent observers see
		// the budget move immediately.
		e.restarts++
		e.lastRestartAt = e.parent.now()
	}
	e.starting = true
	e.lastErr = ""
	e.startedAt = time.Now()
	e.mu.Unlock()

	opts := e.parent.opts
	client, err := NewClient(ClientOptions{
		Spec:             e.spec,
		Root:             e.parent.root,
		Dial:             opts.Dial,
		Log:              opts.Logger,
		Observer:         opts.Observer,
		StartupTimeout:   e.spec.StartupTimeout,
		ShutdownTimeout:  e.spec.ShutdownTimeout,
		ClientName:       opts.ClientName,
		ClientVersion:    opts.ClientVersion,
		EmptyEarlyAccept: e.parent.cfg.Diagnostics.EmptyEarlyAcceptValue() && e.spec.EmptyPublishConclusiveValue(),
		EmptyConfirm:     time.Duration(e.parent.cfg.Diagnostics.EmptyConfirmMS) * time.Millisecond,
		MaxTrackedDocs:   e.parent.cfg.MaxTrackedDocsValue(),
	})
	if err == nil {
		err = client.Start(ctx)
	}

	e.mu.Lock()
	e.starting = false
	if err != nil {
		e.client = nil
		e.lastErr = judgeReason(err)
		e.mu.Unlock()
		return err
	}
	e.client = client
	e.readyAt = time.Now()
	e.mu.Unlock()
	return nil
}

// stop shuts the member down, waiting for the client's bounded shutdown.
func (e *registryEntry) stop(ctx context.Context) {
	e.mu.Lock()
	client := e.client
	e.client = nil
	e.starting = false
	e.mu.Unlock()
	if client == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	done := make(chan struct{})
	go func() {
		client.Shutdown(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-ctx.Done():
	}
}
