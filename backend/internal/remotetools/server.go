package remotetools

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
)

// loadClientCAPool reads a PEM bundle used to verify client certificates.
func loadClientCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read http.clientCAFile: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("http.clientCAFile %q contains no PEM certificates", path)
	}
	return pool, nil
}

// Server is one remote tool service instance: a workspace jail, a prefixed tool
// registry and the MCP server that exposes it.
type Server struct {
	cfg       *Config
	workspace *Workspace
	registry  *toolkit.Registry
	mcp       *toolkit.ToolkitMCPServer
	audit     AuditSink
	access    AccessLogSink
	jobs      *JobManager
	metrics   *Metrics
	roots     *rootsResolver
	token     string
	principal string
	// slots bounds concurrent MCP requests in HTTP mode. A streaming tool call
	// holds its slot until the response completes.
	slots chan struct{}
}

// NewServer builds the service and validates the tool surface invariants.
//
// It returns an error rather than starting in a degraded state: an invalid
// workspace, an unknown profile, a prefix that would overflow the canonical name
// budget, or an HTTP listener without a token are all startup failures.
func NewServer(cfg *Config, serverName string) (*Server, error) {
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}
	workspace, err := NewWorkspace(cfg.Workspace, cfg.AllowedRoots)
	if err != nil {
		return nil, err
	}
	built, err := buildRegistry(cfg, workspace)
	if err != nil {
		return nil, err
	}
	names := exposedToolNames(built.registry)
	if len(names) == 0 {
		return nil, fmt.Errorf("profile %q exposes no tools; check --tools / --profile", cfg.Profile)
	}
	if err := validateToolNames(serverName, names); err != nil {
		return nil, err
	}
	audit, err := NewAuditSink(cfg.Audit)
	if err != nil {
		return nil, err
	}
	access, err := NewAccessLogSink(cfg.HTTP.AccessLog, cfg.HTTP.AccessLogMaxSizeMB, cfg.HTTP.AccessLogMaxBackups)
	if err != nil {
		_ = audit.Close()
		return nil, err
	}
	token := ""
	if cfg.HTTP.TokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(cfg.HTTP.TokenEnv))
	}
	principal := principalFor(cfg, token)
	metrics := NewMetrics()
	slots := make(chan struct{}, cfg.Limits.MaxConcurrentCalls)
	audited := wrapRegistryWithAudit(built.registry, audit, metrics, cfg, workspace, transportOf(cfg))
	mcpServer := toolkit.NewToolkitMCPServer(audited)
	// Roots narrowing is a per-request context concern: the decorator resolves the
	// client's declaration once per session and every tool then resolves its paths
	// through the narrowed workspace (roots_transport.go).
	resolver := newRootsResolver(workspace, principal)
	mcpServer.SetToolContextDecorator(resolver.decorate)
	return &Server{
		cfg:       cfg,
		workspace: workspace,
		registry:  audited,
		mcp:       mcpServer,
		audit:     audit,
		access:    access,
		jobs:      built.jobs,
		metrics:   metrics,
		roots:     resolver,
		token:     token,
		principal: principal,
		slots:     slots,
	}, nil
}

// wrapRegistryWithAudit rebuilds the registry with one audit wrapper per tool.
//
// The MCP server must be built from the wrapped registry, otherwise the exposed
// tools bypass auditing. Keeping the wrapper outside the tool implementations
// means a tool added later cannot forget to emit a record.
func wrapRegistryWithAudit(registry *toolkit.Registry, sink AuditSink, metrics *Metrics, cfg *Config, workspace *Workspace, transport string) *toolkit.Registry {
	if registry == nil || (sink == nil && metrics == nil) {
		return registry
	}
	wrapped := toolkit.NewRegistry()
	for _, tool := range registry.List() {
		if err := wrapped.Register(newAuditTool(tool, sink, metrics, cfg, workspace, transport)); err != nil {
			// Registration of an already-validated tool cannot fail; if it does,
			// keep the unaudited tool rather than losing the surface.
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: audit wrapper skipped for %s: %v\n", tool.Name(), err)
			_ = wrapped.Register(tool)
		}
	}
	return wrapped
}

// transportOf reports the configured transport for audit labels.
func transportOf(cfg *Config) string {
	if cfg != nil && cfg.Transport != "" {
		return cfg.Transport
	}
	return "mcp"
}

// Close releases the audit sink.
func (s *Server) Close() error {
	if s == nil {
		return nil
	}
	// Running jobs are children of this process: leaving them behind after the
	// daemon stops would orphan a build on the target.
	if s.jobs != nil {
		s.jobs.Close()
	}
	var errs []error
	if s.access != nil {
		if err := s.access.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.audit != nil {
		if err := s.audit.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// ToolNames returns the model-visible tool names for diagnostics and doctor.
func (s *Server) ToolNames() []string {
	if s == nil {
		return nil
	}
	return exposedToolNames(s.registry)
}

// Metrics returns the per-server counter set for the status surface.
func (s *Server) Metrics() *Metrics {
	if s == nil {
		return nil
	}
	return s.metrics
}

// RunStdio serves MCP over stdin/stdout until the parent closes stdin.
//
// stdio is the SSH transport: sshd launches one process per MCP connection, so
// the process lifetime equals the connection lifetime. Logs must go to stderr;
// anything written to stdout corrupts the JSON-RPC stream.
func (s *Server) RunStdio(ctx context.Context) error {
	if s == nil || s.mcp == nil {
		return fmt.Errorf("server is not initialized")
	}
	fmt.Fprintf(os.Stderr, "aicli-remote-tools: stdio transport, workspace=%s tools=%s\n",
		s.workspace.Root(), strings.Join(s.ToolNames(), ","))
	return s.mcp.Start(ctx)
}

// ServeStdioOn runs the MCP server on an arbitrary transport.
//
// It exists so tests can drive the real server over an in-memory pipe instead of
// spawning a process; the production stdio path is RunStdio.
func (s *Server) ServeStdioOn(ctx context.Context, transport mcp.Transport) error {
	if s == nil || s.mcp == nil {
		return fmt.Errorf("server is not initialized")
	}
	return s.mcp.GetMCPServer().Run(ctx, transport)
}

// RunHTTP serves MCP over Streamable HTTP until ctx is cancelled.
//
// The listener binds cfg.HTTP.Listen. A non-loopback address without a bearer
// token is refused: an unauthenticated remote shell/file server is a remote root
// equivalent (design §5.5).
func (s *Server) RunHTTP(ctx context.Context) error {
	return s.RunHTTPWithListener(ctx, nil)
}

// RunHTTPWithListener is RunHTTP with an optional listener callback.
//
// onListening receives the bound address once the socket is open, which lets
// tests use port 0 and connect to the real endpoint. A nil callback is the
// production path.
func (s *Server) RunHTTPWithListener(ctx context.Context, onListening func(addr string)) error {
	if s == nil || s.mcp == nil {
		return fmt.Errorf("server is not initialized")
	}
	if err := s.validateHTTPAuth(); err != nil {
		return err
	}

	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server {
		return s.mcp.GetMCPServer()
	}, &mcp.StreamableHTTPOptions{
		// Stateless keeps deployment simple and avoids session affinity
		// requirements; the read-only P1 surface has no cross-request state.
		Stateless: true,
		// SSE is kept so a single call can stream progress. JSONResponse would
		// collapse every long call into one blocking response.
		JSONResponse: false,
	})

	mux := http.NewServeMux()
	mux.Handle(s.cfg.HTTP.Path, s.authMiddleware(s.concurrencyMiddleware(handler)))
	// Counter readings live in this process, so they are served next to the MCP
	// endpoint rather than through a separate CLI command (a fresh process would
	// always report zeros).
	mux.HandleFunc("/metrics", s.metricsHandler)

	server := &http.Server{
		Addr: s.cfg.HTTP.Listen,
		// Access logging is outermost so 401s and /metrics are recorded too;
		// request info is injected before the SDK handler derives the tool context.
		Handler: s.accessLogMiddleware(s.requestInfoMiddleware(mux)),
		// Bounded timeouts: a stalled client must not pin the daemon.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// mTLS is configured on the server's TLSConfig so the standard library does
	// the certificate verification; the application layer then only decides
	// between token and certificate (see authMiddleware).
	if caFile := strings.TrimSpace(s.cfg.HTTP.ClientCAFile); caFile != "" {
		pool, err := loadClientCAPool(caFile)
		if err != nil {
			return err
		}
		clientAuth := tls.VerifyClientCertIfGiven
		if s.cfg.HTTP.RequireClientCert {
			clientAuth = tls.RequireAndVerifyClientCert
		}
		server.TLSConfig = &tls.Config{
			MinVersion: tls.VersionTLS12,
			ClientCAs:  pool,
			ClientAuth: clientAuth,
		}
	}

	listener, err := net.Listen("tcp", s.cfg.HTTP.Listen)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", s.cfg.HTTP.Listen, err)
	}
	if onListening != nil {
		onListening(listener.Addr().String())
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()

	fmt.Fprintf(os.Stderr, "aicli-remote-tools: http transport on %s%s workspace=%s tools=%s\n",
		s.cfg.HTTP.Listen, s.cfg.HTTP.Path, s.workspace.Root(), strings.Join(s.ToolNames(), ","))

	if s.cfg.HTTP.TLSCert != "" && s.cfg.HTTP.TLSKey != "" {
		err := server.ServeTLS(listener, s.cfg.HTTP.TLSCert, s.cfg.HTTP.TLSKey)
		if err == http.ErrServerClosed {
			return nil
		}
		return err
	}
	err = server.Serve(listener)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// metricsHandler renders the counter snapshot as JSON.
//
// It reuses the same bearer-token middleware as the MCP endpoint: call counts and
// byte totals reveal usage patterns and must not be world-readable on a shared
// host.
func (s *Server) metricsHandler(w http.ResponseWriter, r *http.Request) {
	if !s.authorized(r) {
		w.Header().Set("WWW-Authenticate", `Bearer realm="aicli-remote-tools"`)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	payload := struct {
		Target    string   `json:"target"`
		Transport string   `json:"transport"`
		Tools     []string `json:"tools"`
		Metrics   Snapshot `json:"metrics"`
	}{
		Target:    s.cfg.TargetName,
		Transport: transportOf(s.cfg),
		Tools:     s.ToolNames(),
		Metrics:   s.metrics.Snapshot(),
	}
	w.Header().Set("Content-Type", "application/json")
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		http.Error(w, "encode metrics: "+err.Error(), http.StatusInternalServerError)
		return
	}
	_, _ = w.Write(encoded)
}

// validateHTTPAuth enforces the token requirement before binding.
func (s *Server) validateHTTPAuth() error {
	host, _, err := net.SplitHostPort(s.cfg.HTTP.Listen)
	if err != nil {
		return fmt.Errorf("invalid http.listen %q: %w", s.cfg.HTTP.Listen, err)
	}
	if isLoopbackHost(host) {
		return nil
	}
	if strings.TrimSpace(s.token) == "" {
		return fmt.Errorf(
			"refusing to listen on non-loopback %q without a token: set %s or bind 127.0.0.1",
			s.cfg.HTTP.Listen, s.cfg.HTTP.TokenEnv)
	}
	if len(s.token) < 32 {
		return fmt.Errorf("token from %s is shorter than 32 bytes; use a long random value", s.cfg.HTTP.TokenEnv)
	}
	return nil
}

// authMiddleware requires a bearer token on every request when one is
// configured. Comparison is constant time.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="aicli-remote-tools"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// concurrencyMiddleware rejects excess in-flight MCP requests with 429 instead
// of letting them pile up and exhaust the target. /metrics intentionally bypasses
// the limit so operators can still inspect a saturated server.
func (s *Server) concurrencyMiddleware(next http.Handler) http.Handler {
	if s == nil || s.slots == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case s.slots <- struct{}{}:
			defer func() { <-s.slots }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			http.Error(w, "too many concurrent requests", http.StatusTooManyRequests)
		}
	})
}

// requestInfoMiddleware carries the HTTP peer address into the tool context. The
// MCP tool-context decorator adds the session id and call id; the audit wrapper
// reads all of them.
func (s *Server) requestInfoMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		info := RequestInfo{
			Principal:  s.principal,
			RemoteAddr: r.RemoteAddr,
		}
		next.ServeHTTP(w, r.WithContext(WithRequestInfo(r.Context(), info)))
	})
}

// accessLogMiddleware records every HTTP request, including rejected ones.
func (s *Server) accessLogMiddleware(next http.Handler) http.Handler {
	if s == nil || s.access == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w}
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if err := s.access.Write(AccessRecord{
			TS:         time.Now().Format(time.RFC3339Nano),
			RemoteAddr: r.RemoteAddr,
			Method:     r.Method,
			Path:       r.URL.Path,
			Status:     status,
			Bytes:      recorder.bytes,
			DurationMS: time.Since(started).Milliseconds(),
			UserAgent:  r.UserAgent(),
		}); err != nil {
			fmt.Fprintf(os.Stderr, "aicli-remote-tools: access log write failed: %v\n", err)
		}
	})
}

// statusRecorder captures the response status and byte count without changing
// streaming behavior.
type statusRecorder struct {
	http.ResponseWriter
	status int
	bytes  int64
}

func (r *statusRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(p)
	r.bytes += int64(n)
	return n, err
}

func (r *statusRecorder) Flush() {
	if flusher, ok := r.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// Unwrap lets http.ResponseController reach the original writer.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// authorized accepts either a valid bearer token or a verified client
// certificate.
//
// Two authentication modes are supported because deployments differ: an internal
// network with ssh local forwarding uses the token, while a reverse proxy that
// terminates TLS and strips headers can only pass the client certificate. When
// neither is configured (loopback-only default) every request is allowed.
func (s *Server) authorized(r *http.Request) bool {
	tokenConfigured := strings.TrimSpace(s.token) != ""
	certAccepted := strings.TrimSpace(s.cfg.HTTP.ClientCAFile) != ""

	// A verified client certificate is sufficient when mTLS is configured.
	if certAccepted && r != nil && r.TLS != nil && len(r.TLS.VerifiedChains) > 0 {
		if !s.cfg.HTTP.RequireClientCert {
			return true
		}
		// RequireClientCert is enforced by crypto/tls before the handler runs, so
		// reaching this point already means the certificate was verified.
		return true
	}

	if !tokenConfigured {
		// No token and no mandatory certificate: only the loopback default is
		// allowed here. validateHTTPAuth already refused a non-loopback bind
		// without a token.
		return true
	}
	presented := bearerToken(r)
	return subtle.ConstantTimeCompare([]byte(presented), []byte(s.token)) == 1
}

func bearerToken(r *http.Request) string {
	if r == nil {
		return ""
	}
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(header) > 7 && strings.EqualFold(header[:7], "bearer ") {
		return strings.TrimSpace(header[7:])
	}
	return ""
}

// isLoopbackHost reports whether host names the local machine.
func isLoopbackHost(host string) bool {
	trimmed := strings.Trim(strings.TrimSpace(host), "[]")
	switch strings.ToLower(trimmed) {
	case "localhost", "127.0.0.1", "::1":
		return true
	}
	if ip := net.ParseIP(trimmed); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
