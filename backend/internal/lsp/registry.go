package lsp

import (
	"context"
	"fmt"
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
	ClientName    string
	ClientVersion string
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
}

type registryEntry struct {
	spec   ServerSpec
	parent *Registry

	mu        sync.Mutex
	client    *Client
	starting  bool
	lastErr   string
	restarts  int
	startedAt time.Time
	readyAt   time.Time
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
	entry.stop(ctx)
	entry.mu.Lock()
	entry.restarts++
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
	case entry.parent.isStopped():
		status.State = StateStopped
		status.Reason = "shut down"
	default:
		status.State = StateStarting
		status.Reason = "pending first use"
	}
	return status
}

// wantsStart reports whether a lazy start should be attempted.
func (s *Server) wantsStart() bool {
	if s.entry.parent.isStopped() {
		return false
	}
	s.entry.mu.Lock()
	defer s.entry.mu.Unlock()
	return s.entry.client == nil && !s.entry.starting && s.entry.lastErr == ""
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

// start performs a blocking start and records the outcome.
func (e *registryEntry) start(ctx context.Context) error {
	e.mu.Lock()
	if e.client != nil || e.starting {
		e.mu.Unlock()
		return nil
	}
	e.starting = true
	e.lastErr = ""
	e.startedAt = time.Now()
	e.mu.Unlock()

	opts := e.parent.opts
	client, err := NewClient(ClientOptions{
		Spec:            e.spec,
		Root:            e.parent.root,
		Dial:            opts.Dial,
		Log:             opts.Logger,
		StartupTimeout:  e.spec.StartupTimeout,
		ShutdownTimeout: e.spec.ShutdownTimeout,
		ClientName:      opts.ClientName,
		ClientVersion:   opts.ClientVersion,
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
