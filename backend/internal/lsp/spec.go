package lsp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- configuration surface (docs/lsp 02 §4, 03 §4) ----
//
// Every threshold the pipeline uses lives here and can be changed from
// config alone: docs/lsp 03 invariant I6 forbids hard-coded limits.
// A5 requires that each knob changes behavior without a code change.

// DiagnosticScope selects between crush-parity full diagnostics ("all") and
// baseline-diffed diagnostics ("changed", docs/lsp 03 Q1/A6).
type DiagnosticScope string

const (
	// ScopeAll returns every diagnostic of the current version (crush parity).
	ScopeAll DiagnosticScope = "all"
	// ScopeChanged returns only diagnostics that were not present before the
	// edit (Q1 baseline diff).
	ScopeChanged DiagnosticScope = "changed"
)

// DegradeMode is the docs/lsp 03 §4 `diagnostics.degrade_mode` knob.
type DegradeMode string

const (
	// DegradeNone silently skips inline diagnostics when a wait fails.
	DegradeNone DegradeMode = "none"
	// DegradeHint appends a short note explaining why no diagnostics came
	// back (default; the edit itself always succeeds — A8).
	DegradeHint DegradeMode = "hint"
	// DegradeError appends an explicit error-styled marker. It is still
	// append-only text on a successful edit, never a tool failure.
	DegradeError DegradeMode = "error"
)

// Built-in fallbacks. Config may override every one of them.
const (
	DefaultMaxItems        = 20
	DefaultMaxChars        = 2000
	DefaultWaitMS          = 1000
	DefaultStartupTimeout  = 15 * time.Second
	DefaultShutdownTimeout = 5 * time.Second
)

// DiagnosticsConfig mirrors the `diagnostics.*` keys from docs/lsp 03 §4.
type DiagnosticsConfig struct {
	// Scope is "all" (crush behavior) or "changed" (baseline diff).
	Scope DiagnosticScope `yaml:"scope,omitempty" json:"scope,omitempty"`
	// MaxItems caps the number of inline diagnostics. <=0 falls back to the
	// built-in default (never "unlimited").
	MaxItems int `yaml:"maxItems,omitempty" json:"maxItems,omitempty"`
	// MaxChars caps the rendered character budget of the appended block.
	MaxChars int `yaml:"maxChars,omitempty" json:"maxChars,omitempty"`
	// WaitMS bounds how long an edit may block waiting for fresh diagnostics.
	WaitMS int `yaml:"waitMs,omitempty" json:"waitMs,omitempty"`
	// DegradeMode is one of none|hint|error.
	DegradeMode DegradeMode `yaml:"degradeMode,omitempty" json:"degradeMode,omitempty"`
	// ToolEnabled registers the optional `diagnostics` tool (W7). Default
	// false: the inline loop is the primary feedback path.
	ToolEnabled bool `yaml:"toolEnabled,omitempty" json:"toolEnabled,omitempty"`
}

// DefaultDiagnosticsConfig returns the documented defaults. Scope defaults to
// "all" (crush parity); A6 carries the data-driven decision to switch it.
func DefaultDiagnosticsConfig() DiagnosticsConfig {
	return DiagnosticsConfig{
		Scope:       ScopeAll,
		MaxItems:    DefaultMaxItems,
		MaxChars:    DefaultMaxChars,
		WaitMS:      DefaultWaitMS,
		DegradeMode: DegradeHint,
	}
}

// Normalize replaces zero/invalid values with defaults.
func (d DiagnosticsConfig) Normalize() DiagnosticsConfig {
	if d.Scope != ScopeAll && d.Scope != ScopeChanged {
		d.Scope = ScopeAll
	}
	if d.MaxItems <= 0 {
		d.MaxItems = DefaultMaxItems
	}
	if d.MaxChars <= 0 {
		d.MaxChars = DefaultMaxChars
	}
	if d.WaitMS <= 0 {
		d.WaitMS = DefaultWaitMS
	}
	switch d.DegradeMode {
	case DegradeNone, DegradeHint, DegradeError:
	default:
		d.DegradeMode = DegradeHint
	}
	return d
}

// ServerSpec declares one language server, its ownership metadata and the
// files it handles (docs/lsp 03 W1: one `HandlesFile` decision shared by the
// notification path and the diagnostics path).
type ServerSpec struct {
	Name    string   `yaml:"name" json:"name"`
	Command string   `yaml:"command" json:"command"`
	Args    []string `yaml:"args,omitempty" json:"args,omitempty"`
	// Env augments the inherited environment (KEY=VALUE).
	Env map[string]string `yaml:"env,omitempty" json:"env,omitempty"`
	// Languages matches LSP language ids (go, typescript, python...).
	Languages []string `yaml:"languages,omitempty" json:"languages,omitempty"`
	// Extensions matches file extensions, case-insensitive, with dot.
	Extensions []string `yaml:"extensions,omitempty" json:"extensions,omitempty"`
	// Filenames matches exact base names (go.mod, package.json...).
	Filenames []string `yaml:"filenames,omitempty" json:"filenames,omitempty"`
	// InitializationOptions is forwarded verbatim in `initialize`.
	InitializationOptions map[string]interface{} `yaml:"initializationOptions,omitempty" json:"initializationOptions,omitempty"`
	// Enabled defaults to true when nil.
	Enabled *bool `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	// StartupTimeout bounds initialize; <=0 uses DefaultStartupTimeout.
	StartupTimeout time.Duration `yaml:"startupTimeout,omitempty" json:"startupTimeout,omitempty"`
	// ShutdownTimeout bounds shutdown/exit; <=0 uses DefaultShutdownTimeout.
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout,omitempty" json:"shutdownTimeout,omitempty"`
}

// IsEnabled reports whether the spec participates in the pool.
func (s ServerSpec) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// HandlesFile is the W1 ownership decision. It is path/metadata based only —
// never content heuristics (docs/lsp 02 §2.3) — and a spec with no selector
// handles nothing, so a misconfigured server cannot silently claim the whole
// workspace.
func (s ServerSpec) HandlesFile(path string) bool {
	if strings.TrimSpace(path) == "" {
		return false
	}
	ext := strings.ToLower(filepath.Ext(path))
	base := filepath.Base(path)
	for _, candidate := range s.Extensions {
		if strings.EqualFold(strings.TrimSpace(candidate), ext) && ext != "" {
			return true
		}
	}
	for _, candidate := range s.Filenames {
		if strings.EqualFold(strings.TrimSpace(candidate), base) {
			return true
		}
	}
	if len(s.Languages) > 0 {
		language := languageIDForPath(path)
		if language == "" {
			return false
		}
		for _, candidate := range s.Languages {
			if strings.EqualFold(strings.TrimSpace(candidate), language) {
				return true
			}
		}
	}
	return false
}

// languageIDForPath maps an extension to the LSP language id. It is the same
// mapping the client uses for didOpen, so routing and didOpen cannot drift.
func languageIDForPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	return extensionLanguageID[ext]
}

func (s ServerSpec) normalized() ServerSpec {
	s.Name = strings.TrimSpace(s.Name)
	s.Command = strings.TrimSpace(s.Command)
	s.Args = append([]string(nil), s.Args...)
	if len(s.Env) > 0 {
		env := make(map[string]string, len(s.Env))
		for key, value := range s.Env {
			env[key] = value
		}
		s.Env = env
	}
	if s.StartupTimeout <= 0 {
		s.StartupTimeout = DefaultStartupTimeout
	}
	if s.ShutdownTimeout <= 0 {
		s.ShutdownTimeout = DefaultShutdownTimeout
	}
	return s
}

// Config is the root `lsp` section of RuntimeConfig.
type Config struct {
	// Enabled defaults to false: no language server is spawned unless the
	// host explicitly opts in. This keeps the documented "LSP missing is
	// normal" degradation the default (docs/lsp 02 §6.3, A11).
	Enabled bool `yaml:"enabled" json:"enabled"`
	// Servers lists the pool members. PresetServers() is the default catalog.
	Servers []ServerSpec `yaml:"servers,omitempty" json:"servers,omitempty"`
	// Diagnostics carries the inline-diagnostics knobs.
	Diagnostics DiagnosticsConfig `yaml:"diagnostics,omitempty" json:"diagnostics,omitempty"`
	// Prewarm starts every configured member when the pool is constructed
	// instead of waiting for first use. Default false keeps the lazy start
	// (W3); enabling it trades idle processes for a cold-start-free first
	// edit.
	Prewarm bool `yaml:"prewarm,omitempty" json:"prewarm,omitempty"`
	// RestartLimit caps automatic crash recovery per member and session
	// (L2). nil or a negative value means DefaultRestartLimit; 0 disables
	// automatic recovery (manual Restart stays available).
	RestartLimit *int `yaml:"restartLimit,omitempty" json:"restartLimit,omitempty"`
}

// DefaultConfig is the inert default: presets are declared but the pool stays
// off until `lsp.enabled: true` is configured.
func DefaultConfig() Config {
	return Config{
		Enabled:     false,
		Servers:     PresetServers(),
		Diagnostics: DefaultDiagnosticsConfig(),
	}
}

// DefaultRestartLimit is the automatic crash-recovery cap applied when
// lsp.restartLimit is unset: one replacement per member per session.
const DefaultRestartLimit = 1

// RestartLimitValue resolves the effective automatic-recovery cap. nil and
// negative values fall back to DefaultRestartLimit; 0 disables automatic
// recovery (L2).
func (c Config) RestartLimitValue() int {
	if c.RestartLimit == nil || *c.RestartLimit < 0 {
		return DefaultRestartLimit
	}
	return *c.RestartLimit
}

// Normalize fills defaults and drops malformed entries. Duplicate names keep
// the first declaration (stable precedence, A9).
func (c Config) Normalize() Config {
	c.Diagnostics = c.Diagnostics.Normalize()
	if c.RestartLimit != nil {
		limit := *c.RestartLimit
		c.RestartLimit = &limit
	}
	if c.Servers == nil {
		c.Servers = PresetServers()
	}
	servers := make([]ServerSpec, 0, len(c.Servers))
	seen := make(map[string]struct{}, len(c.Servers))
	for _, spec := range c.Servers {
		spec = spec.normalized()
		if spec.Name == "" || spec.Command == "" {
			continue
		}
		if _, exists := seen[spec.Name]; exists {
			continue
		}
		seen[spec.Name] = struct{}{}
		servers = append(servers, spec)
	}
	c.Servers = servers
	return c
}

// PresetServers is the built-in catalog: well-known servers with their
// standard stdio invocations and selectors. A missing binary only degrades
// that server (L2), it never blocks a session.
func PresetServers() []ServerSpec {
	return []ServerSpec{
		{
			Name:       "gopls",
			Command:    "gopls",
			Args:       []string{"serve"},
			Extensions: []string{".go"},
			Filenames:  []string{"go.mod", "go.work"},
		},
		{
			Name:       "typescript",
			Command:    "typescript-language-server",
			Args:       []string{"--stdio"},
			Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"},
		},
		{
			Name:       "pyright",
			Command:    "pyright-langserver",
			Args:       []string{"--stdio"},
			Extensions: []string{".py", ".pyi"},
		},
		{
			Name:       "rust-analyzer",
			Command:    "rust-analyzer",
			Extensions: []string{".rs"},
		},
		{
			Name:       "clangd",
			Command:    "clangd",
			Extensions: []string{".c", ".cc", ".cpp", ".cxx", ".h", ".hh", ".hpp", ".hxx"},
		},
	}
}

// ---- process dialect ----

// SpawnProcess is the default DialFunc: it starts the server as a child
// process speaking LSP over stdio.
func SpawnProcess(ctx context.Context, spec ServerSpec, root string, logger Logger) (*DialResult, error) {
	logger = LoggerOrNop(logger)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	executable, err := exec.LookPath(spec.Command)
	if err != nil {
		return nil, fmt.Errorf("lsp: %s: executable %q not found", spec.Name, spec.Command)
	}
	cmd := exec.Command(executable, spec.Args...)
	cmd.Dir = root
	if len(spec.Env) > 0 {
		cmd.Env = append(os.Environ(), envPairs(spec.Env)...)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: %s: stdin: %w", spec.Name, err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("lsp: %s: stdout: %w", spec.Name, err)
	}
	cmd.Stderr = &lineLogWriter{logger: logger, prefix: spec.Name}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("lsp: %s: start: %w", spec.Name, err)
	}
	conn := &processConn{reader: stdout, writer: stdin}
	return &DialResult{
		Conn: conn,
		PID:  cmd.Process.Pid,
		Kill: cmd.Process.Kill,
		Wait: cmd.Wait,
	}, nil
}

func envPairs(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+env[key])
	}
	return pairs
}

// processConn adapts the two stdio pipes to one io.ReadWriteCloser.
type processConn struct {
	reader io.ReadCloser
	writer io.WriteCloser
	once   sync.Once
	err    error
}

func (c *processConn) Read(p []byte) (int, error)  { return c.reader.Read(p) }
func (c *processConn) Write(p []byte) (int, error) { return c.writer.Write(p) }

func (c *processConn) Close() error {
	c.once.Do(func() {
		readErr := c.reader.Close()
		writeErr := c.writer.Close()
		if readErr != nil {
			c.err = readErr
		} else {
			c.err = writeErr
		}
	})
	return c.err
}

// lineLogWriter forwards server stderr to the runtime debug log line by line.
type lineLogWriter struct {
	logger Logger
	prefix string
	mu     sync.Mutex
	buf    []byte
}

func (w *lineLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	for {
		index := strings.IndexByte(string(w.buf), '\n')
		if index < 0 {
			break
		}
		line := strings.TrimRight(string(w.buf[:index]), "\r")
		w.buf = w.buf[index+1:]
		if strings.TrimSpace(line) != "" {
			w.logger.Debugf("lsp: %s: stderr: %s", w.prefix, line)
		}
	}
	return len(p), nil
}
