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

	"github.com/wwsheng009/ai-agent-runtime/internal/executor"
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
	DefaultMaxItems = 20
	DefaultMaxChars = 2000
	DefaultWaitMS   = 1000
	// DefaultStartWaitMS bounds how long one request waits for a member that
	// is still handshaking (cold start). A cold gopls usually cannot finish
	// inside a single edit; waiting the full diagnostics budget just adds
	// latency, so the request degrades early and the background start keeps
	// going for the next edit.
	DefaultStartWaitMS = 250
	// DefaultEmptyConfirmMS is the debounce for conclusive empty publishes.
	DefaultEmptyConfirmMS  = 150
	DefaultStartupTimeout  = 15 * time.Second
	DefaultShutdownTimeout = 5 * time.Second
	// DefaultColdStartGraceMS extends the first diagnostics wait for a path
	// whose server has not published anything yet: gopls may spend seconds
	// loading a module view before the first publish, and the normal budget
	// would degrade that edit (live evidence: backend view took ~85s under
	// heavy load; medium cold starts land inside the grace).
	DefaultColdStartGraceMS = 1500
	// DefaultColdRetryMS is the reduced wait used once a path is known cold:
	// the grace was already spent and the connection still never published.
	// Re-waiting the full budget on every edit during a long view load only
	// adds latency; the first edit after the publish gets the real result.
	DefaultColdRetryMS = 250
	// DefaultColdProbeMS bounds the one-time first-probe wait for a path
	// with no snapshot. The grace extension (wait_ms + cold_start_grace_ms)
	// reaches 2.5s with the defaults, and the cross-session baseline showed
	// 96% of no_fresh requests are such first probes
	// (lsp_cold_first_probe_ratio 0.9608) while the per-path cold fast-fail
	// covers only the repeats. 1500ms equals the grace value: it still
	// covers medium cold starts (live evidence: 1.33s first publish on a
	// warm connection), and the extreme tail (~85s module views) never
	// landed inside the budget anyway — that is why the path is then
	// marked known-cold.
	DefaultColdProbeMS = 1500
	// DefaultMaxTrackedDocs caps the per-client didOpen document set. Long
	// sessions touch many files; without a bound the docs map (full content
	// copies) grows forever and servers keep analyzing closed files.
	DefaultMaxTrackedDocs = 128
)

// EmptyStyle values for DiagnosticsConfig.EmptyStyle. compact keeps the
// "checked, no problems" signal while dropping the redundant scope/servers
// attributes; full preserves the pre-optimization block verbatim.
const (
	EmptyStyleCompact = "compact"
	EmptyStyleFull    = "full"
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
	// StartWaitMS bounds the wait for a member that is still starting. <=0
	// falls back to DefaultStartWaitMS; it is capped by WaitMS (a request
	// never waits longer than the diagnostics budget).
	StartWaitMS int `yaml:"startWaitMs,omitempty" json:"startWaitMs,omitempty"`
	// EmptyEarlyAccept trusts a version-stamped empty publish as a conclusive
	// "no problems" answer, ending the wait immediately instead of burning the
	// full WaitMS. nil = default true. It only takes effect for servers that
	// declare EmptyPublishConclusive (gopls preset); servers that publish
	// interim empty sets (rust-analyzer) keep the conservative behavior.
	EmptyEarlyAccept *bool `yaml:"emptyEarlyAccept,omitempty" json:"emptyEarlyAccept,omitempty"`
	// EmptyConfirmMS is the debounce applied to a version-stamped empty
	// publish before it is accepted: a real diagnostic set published moments
	// later supersedes it. <=0 falls back to DefaultEmptyConfirmMS.
	EmptyConfirmMS int `yaml:"emptyConfirmMs,omitempty" json:"emptyConfirmMs,omitempty"`
	// HintOnce limits the inline degradation note to the first occurrence per
	// (server, reason) pair. Repeated identical notes are pure token noise;
	// pool state stays observable through `lsp_servers` / `/lsp status`.
	// nil = default true.
	HintOnce *bool `yaml:"hintOnce,omitempty" json:"hintOnce,omitempty"`
	// ColdStartGraceMS extends the wait once per path while that path has no
	// snapshot yet (cold view / first analysis of a file). It is bounded to
	// one grant per path per server instance and repeats are covered by the
	// per-path cold fast-fail, so steady-state latency is untouched. A warm
	// connection still grants it to a path it has never published for: the
	// first analysis of a newly created file can outlive the plain budget
	// (live evidence: 1.71s publish vs 1.0s budget).
	// 0/unset = DefaultColdStartGraceMS; negative disables.
	ColdStartGraceMS int `yaml:"coldStartGraceMs,omitempty" json:"coldStartGraceMs,omitempty"`
	// ColdRetryMS is the reduced wait once a path is known cold: the
	// connection has never published and a grace-extended wait for this path
	// already failed with no snapshot. The view load continues in the
	// background; failing fast keeps a long cold window (live evidence: ~85s)
	// from burning the full budget on every edit. 0/unset = DefaultColdRetryMS;
	// negative disables (always wait the full budget).
	ColdRetryMS int `yaml:"coldRetryMs,omitempty" json:"coldRetryMs,omitempty"`
	// ColdProbeMS bounds the one-time first-probe wait: the budget a
	// path with no snapshot gets on its first edit, after the grace
	// extension. Without the cap the first probe pays wait_ms +
	// cold_start_grace_ms (2.5s with the defaults) on every
	// never-published path; the baseline showed those first probes
	// are 96% of all no_fresh requests. The cap never goes below
	// wait_ms: a caller that raises wait_ms explicitly wants the
	// cold path to keep paying it.
	// 0/unset = DefaultColdProbeMS; negative disables the cap (first
	// probe pays the full grace-extended budget, pre-O12 behavior).
	ColdProbeMS int `yaml:"coldProbeMs,omitempty" json:"coldProbeMs,omitempty"`
	// EmptyStyle controls the clean-result inline block: "compact" (default)
	// emits only the self-closing marker, "full" also carries scope/servers.
	// Only the empty branch changes; diagnostic blocks are untouched.
	EmptyStyle string `yaml:"emptyStyle,omitempty" json:"emptyStyle,omitempty"`
}

// EmptyStyleValue resolves the effective empty-block style (default compact).
func (d DiagnosticsConfig) EmptyStyleValue() string {
	if strings.EqualFold(strings.TrimSpace(d.EmptyStyle), EmptyStyleFull) {
		return EmptyStyleFull
	}
	return EmptyStyleCompact
}

// DefaultDiagnosticsConfig returns the documented defaults. Scope defaults to
// "all" (crush parity); A6 carries the data-driven decision to switch it.
func DefaultDiagnosticsConfig() DiagnosticsConfig {
	return DiagnosticsConfig{
		Scope:            ScopeAll,
		MaxItems:         DefaultMaxItems,
		MaxChars:         DefaultMaxChars,
		WaitMS:           DefaultWaitMS,
		DegradeMode:      DegradeHint,
		StartWaitMS:      DefaultStartWaitMS,
		EmptyConfirmMS:   DefaultEmptyConfirmMS,
		ColdStartGraceMS: DefaultColdStartGraceMS,
		ColdRetryMS:      DefaultColdRetryMS,
		ColdProbeMS:      DefaultColdProbeMS,
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
	if d.StartWaitMS <= 0 || d.StartWaitMS > d.WaitMS {
		d.StartWaitMS = DefaultStartWaitMS
		if d.StartWaitMS > d.WaitMS {
			d.StartWaitMS = d.WaitMS
		}
	}
	if d.EmptyConfirmMS <= 0 || d.EmptyConfirmMS > d.WaitMS {
		d.EmptyConfirmMS = DefaultEmptyConfirmMS
		if d.EmptyConfirmMS > d.WaitMS {
			d.EmptyConfirmMS = d.WaitMS
		}
	}
	if d.ColdStartGraceMS == 0 {
		d.ColdStartGraceMS = DefaultColdStartGraceMS
	}
	if d.ColdStartGraceMS < 0 {
		d.ColdStartGraceMS = 0
	}
	if d.ColdStartGraceMS > 5000 {
		d.ColdStartGraceMS = 5000
	}
	if d.ColdRetryMS == 0 {
		d.ColdRetryMS = DefaultColdRetryMS
	}
	if d.ColdRetryMS < 0 {
		d.ColdRetryMS = 0
	}
	if d.ColdRetryMS > 5000 {
		d.ColdRetryMS = 5000
	}
	// Negative stays negative: it is the "cap disabled" sentinel; the
	// bridge treats probe <= 0 as "no cap" (unlike grace/retry, where
	// negative means "feature off" and is normalized to 0).
	if d.ColdProbeMS == 0 {
		d.ColdProbeMS = DefaultColdProbeMS
	}
	if d.ColdProbeMS > 5000 {
		d.ColdProbeMS = 5000
	}
	switch d.DegradeMode {
	case DegradeNone, DegradeHint, DegradeError:
	default:
		d.DegradeMode = DegradeHint
	}
	return d
}

// EmptyEarlyAcceptValue resolves the effective early-accept policy (nil =
// true).
func (d DiagnosticsConfig) EmptyEarlyAcceptValue() bool {
	return d.EmptyEarlyAccept == nil || *d.EmptyEarlyAccept
}

// HintOnceValue resolves the effective hint dedupe policy (nil = true).
func (d DiagnosticsConfig) HintOnceValue() bool {
	return d.HintOnce == nil || *d.HintOnce
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
	// EmptyPublishConclusive declares that a version-stamped empty publish
	// from this server is a final "no problems" answer (gopls), not an
	// interim result. nil = false (conservative wait).
	EmptyPublishConclusive *bool `yaml:"emptyPublishConclusive,omitempty" json:"emptyPublishConclusive,omitempty"`
	// StartupTimeout bounds initialize; <=0 uses DefaultStartupTimeout.
	StartupTimeout time.Duration `yaml:"startupTimeout,omitempty" json:"startupTimeout,omitempty"`
	// ShutdownTimeout bounds shutdown/exit; <=0 uses DefaultShutdownTimeout.
	ShutdownTimeout time.Duration `yaml:"shutdownTimeout,omitempty" json:"shutdownTimeout,omitempty"`
}

// IsEnabled reports whether the spec participates in the pool.
func (s ServerSpec) IsEnabled() bool {
	return s.Enabled == nil || *s.Enabled
}

// EmptyPublishConclusiveValue resolves the per-server early-accept opt-in.
func (s ServerSpec) EmptyPublishConclusiveValue() bool {
	return s.EmptyPublishConclusive != nil && *s.EmptyPublishConclusive
}

func boolPtr(value bool) *bool { return &value }

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
	// RestartWindow is the sliding window the automatic-recovery budget is
	// counted over. A member that crashes after a quiet period longer than
	// this window gets a fresh budget instead of staying degraded for the
	// rest of the process lifetime. <=0 uses DefaultRestartWindow.
	RestartWindow time.Duration `yaml:"restartWindow,omitempty" json:"restartWindow,omitempty"`
	// MaxTrackedDocs bounds the per-client open-document set (LRU eviction
	// sends didClose). <=0 uses DefaultMaxTrackedDocs.
	MaxTrackedDocs int `yaml:"maxTrackedDocs,omitempty" json:"maxTrackedDocs,omitempty"`
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

// DefaultRestartWindow is the sliding window the restart budget is counted
// over when lsp.restartWindow is unset: one replacement per 10 minutes.
const DefaultRestartWindow = 10 * time.Minute

// RestartLimitValue resolves the effective automatic-recovery cap. nil and
// negative values fall back to DefaultRestartLimit; 0 disables automatic
// recovery (L2).
func (c Config) RestartLimitValue() int {
	if c.RestartLimit == nil || *c.RestartLimit < 0 {
		return DefaultRestartLimit
	}
	return *c.RestartLimit
}

// RestartWindowValue resolves the effective recovery window.
func (c Config) RestartWindowValue() time.Duration {
	if c.RestartWindow <= 0 {
		return DefaultRestartWindow
	}
	return c.RestartWindow
}

// MaxTrackedDocsValue resolves the effective open-document cap.
func (c Config) MaxTrackedDocsValue() int {
	if c.MaxTrackedDocs <= 0 {
		return DefaultMaxTrackedDocs
	}
	return c.MaxTrackedDocs
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
			// gopls 的 publishDiagnostics 在分析完成后发布（带版本），空集即
			// 结论；rust-analyzer 等会先发中间空集，因此默认不开启。
			EmptyPublishConclusive: boolPtr(true),
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

// PresetServersNamed returns the subset of PresetServers() whose Name is in
// names, preserving preset order. Unknown names are ignored, so callers can
// pass a project-scan result (server names) without validating it first. The
// returned specs are the full preset definitions (command/args/selectors), so
// they can be persisted into a config layer or fed to NewBridge directly.
func PresetServersNamed(names []string) []ServerSpec {
	if len(names) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		if trimmed := strings.TrimSpace(name); trimmed != "" {
			wanted[trimmed] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return nil
	}
	selected := make([]ServerSpec, 0, len(wanted))
	for _, spec := range PresetServers() {
		if _, ok := wanted[spec.Name]; ok {
			selected = append(selected, spec)
		}
	}
	return selected
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
	tail := &stderrTail{}
	cmd.Stderr = io.MultiWriter(cmd.Stderr, tail)
	// ADR-0005 §4.1：LSP 子进程必须纳入 internal/executor 的进程树守卫
	// （Windows Job Object + KILL_ON_JOB_CLOSE；Unix Setpgid），禁止在别处
	// 自建 Job Object。绑定失败不阻断启动，但必须可观测（rep.Mode）。
	guard := executor.NewProcessGuard()
	if err := guard.Bind(cmd); err != nil {
		guard.Close()
		return nil, fmt.Errorf("lsp: %s: process guard bind: %w", spec.Name, err)
	}
	if err := cmd.Start(); err != nil {
		guard.Close()
		return nil, fmt.Errorf("lsp: %s: start: %w", spec.Name, err)
	}
	if err := guard.Attach(cmd.Process); err != nil {
		// 沿用既有降级语义：绑定失败时 terminate 会退到 taskkill/direct kill。
		guard.NoteAttachError(err)
	}
	conn := &processConn{reader: stdout, writer: stdin}
	return &DialResult{
		Conn:       conn,
		PID:        cmd.Process.Pid,
		Kill:       func() error { guard.Terminate(); return nil },
		Wait:       cmd.Wait,
		Guard:      guard,
		StderrTail: tail.String,
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
