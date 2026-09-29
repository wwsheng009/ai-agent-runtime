package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Bridge is the tool-layer facade described by docs/lsp 02 §3-§4. It owns the
// client pool (L1-L3), performs the post-write synchronization (L4), reads the
// current diagnostics (L5) and renders the append-only block (L6).
type Bridge struct {
	cfg      Config
	registry *Registry
	logger   Logger
	// observer 是 host 侧观察者（BridgeOptions.Observer 原样保存）：Bridge 级
	// 请求事件（request.finished）直接从 Bridge 发出，不经过 registry 内
	// client 的观察链（那条链只承载生命周期/诊断发布事件）。
	observer Observer
	// metrics 是进程内请求读数（web /overview 与 /events 的单一来源）。
	metrics *Metrics
	// sessionIDFromContext 从工具执行 ctx 解析会话归属（可选；runtime-server
	// 的共享工具管理器靠它把事件关联到具体会话）。
	sessionIDFromContext func(context.Context) string
}

// Outcome is the result of one diagnostics pass for one file.
type Outcome struct {
	Path string
	// Text is the rendered, append-ready block ("" when nothing should be
	// appended).
	Text string
	// Items are the surviving diagnostics after scope/truncation.
	Items []Diagnostic
	// Handled reports whether at least one configured server claims the file
	// (W1). Handled=false means "silent skip" (A2), not a degradation.
	Handled bool
	// Fresh reports whether the items came from a post-change publish.
	Fresh bool
	// Degraded reports that a handling server could not deliver fresh
	// diagnostics (missing binary, crash, timeout).
	Degraded bool
	Reason   string
	// Broken down truncation facts (A7).
	OmittedItems   int
	OmittedByChars int
	// Servers lists the names that participated.
	Servers []string
}

// BridgeOptions carries the injectable seams of the facade. Logger feeds both
// the registry (server stderr, lifecycle logs) and the default event observer;
// Observer adds a host-side consumer on top of the logging.
type BridgeOptions struct {
	Logger   Logger
	Dial     DialFunc
	Observer Observer
	// SessionIDFromContext 从工具执行 ctx 解析会话归属（可选）：runtime-server
	// 等宿主共享一个工具管理器服务多会话，事件必须按执行上下文归属，否则
	// observe 侧无法关联到具体会话。返回空串表示该事件无会话归属（生命周期
	// 事件天然如此）；aicli 单会话宿主可不设置，由 host 侧回退补会话 id。
	SessionIDFromContext func(context.Context) string
}

// NewBridge builds the facade. dial may be nil (SpawnProcess is used).
func NewBridge(cfg Config, root string, logger Logger, dial DialFunc) *Bridge {
	return NewBridgeWithOptions(cfg, root, BridgeOptions{Logger: logger, Dial: dial})
}

// NewBridgeWithOptions builds the facade from explicit seams.
func NewBridgeWithOptions(cfg Config, root string, opts BridgeOptions) *Bridge {
	cfg = cfg.Normalize()
	logger := LoggerOrNop(opts.Logger)
	observer := composeObservers(opts.Observer, logObserver(logger))
	return &Bridge{
		cfg:                  cfg,
		registry:             NewRegistry(cfg, root, RegistryOptions{Dial: opts.Dial, Logger: opts.Logger, Observer: observer}),
		logger:               logger,
		observer:             opts.Observer,
		metrics:              NewMetrics(),
		sessionIDFromContext: opts.SessionIDFromContext,
	}
}

// Enabled reports whether the pool has any usable member.
func (b *Bridge) Enabled() bool {
	return b != nil && b.cfg.Enabled && !b.registry.Empty()
}

// Config returns the normalized configuration.
func (b *Bridge) Config() Config {
	if b == nil {
		return Config{}
	}
	return b.cfg
}

// Root is the workspace root bound to the pool.
func (b *Bridge) Root() string {
	if b == nil {
		return ""
	}
	return b.registry.Root()
}

// Statuses exposes the `lsp_servers`-shaped observability records (L2/A4).
func (b *Bridge) Statuses() []ServerStatus {
	if b == nil {
		return nil
	}
	return b.registry.Statuses()
}

// StartAll prewarms the pool. Optional; first use starts lazily.
func (b *Bridge) StartAll(ctx context.Context) {
	if b == nil {
		return
	}
	b.registry.StartAll(ctx)
}

// Stop shuts the pool down.
func (b *Bridge) Stop(ctx context.Context) {
	if b == nil {
		return
	}
	b.registry.Stop(ctx)
}

// Restart is the recovery entry point (L2).
func (b *Bridge) Restart(ctx context.Context, name string) error {
	if b == nil {
		return nil
	}
	return b.registry.Restart(ctx, name)
}

// StartServer is the manual lazy-start entry point: it starts one member
// synchronously (ready members are left untouched).
func (b *Bridge) StartServer(ctx context.Context, name string) error {
	if b == nil {
		return nil
	}
	return b.registry.StartServer(ctx, name)
}

// Handles reports whether any server claims the file (W1).
func (b *Bridge) Handles(path string) bool {
	if b == nil {
		return false
	}
	return len(b.registry.ServersForPath(b.resolve(path))) > 0
}

// AppendToResult appends diagnostics for every mutated path to the untouched
// tool output. It never returns an error: LSP problems degrade to text (A8).
func (b *Bridge) AppendToResult(ctx context.Context, output string, paths []string) string {
	if !b.Enabled() || len(paths) == 0 {
		return output
	}
	var blocks []string
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		resolved := b.resolve(path)
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		start := time.Now()
		outcome := b.Diagnose(ctx, resolved)
		appended := 0
		if outcome.Text != "" {
			appended = len(outcome.Text)
		}
		b.observeRequest(ctx, "inline", resolved, outcome, appended, time.Since(start))
		if outcome.Text == "" {
			continue
		}
		blocks = append(blocks, strings.TrimRight(outcome.Text, "\n"))
	}
	if len(blocks) == 0 {
		return output
	}
	suffix := strings.Join(blocks, "\n") + "\n"
	if output == "" {
		return suffix
	}
	if !strings.HasSuffix(output, "\n") {
		output += "\n"
	}
	return output + suffix
}

// Diagnose runs the full post-write pipeline for one file: W1 filter → W4
// didChange/didSave → W5 wait for the current version → scope filter →
// truncation → render. The wait is bounded by diagnostics.wait_ms across all
// servers (A10); every failure path degrades instead of erroring.
func (b *Bridge) Diagnose(ctx context.Context, path string) Outcome {
	outcome := Outcome{Path: path}
	if !b.Enabled() {
		return outcome
	}
	if ctx == nil {
		ctx = context.Background()
	}
	servers := b.registry.ServersForPath(path)
	if len(servers) == 0 {
		return outcome
	}
	outcome.Handled = true

	cfg := b.registry.DiagnosticsConfig()
	wait := time.Duration(cfg.WaitMS) * time.Millisecond
	deadline := time.Now().Add(wait)

	content, err := os.ReadFile(path)
	if err != nil {
		outcome.Degraded = true
		outcome.Reason = "read file: " + err.Error()
		outcome.Text = DegradeNote(path, outcome.Reason, cfg.DegradeMode)
		return outcome
	}

	collected := make(map[string]Diagnostic)
	baseline := make(map[string]struct{})
	fresh := false
	degradeReason := ""
	for _, server := range servers {
		outcome.Servers = append(outcome.Servers, server.Name())
		client, reason := b.readyClient(ctx, server, deadline)
		if client == nil {
			degradeReason = firstNonEmpty(degradeReason, reason)
			continue
		}
		// Baseline for scope=changed is the last snapshot *before* the
		// didChange below; it may be superseded, which is exactly what makes
		// it a baseline (docs/lsp 03 A6).
		for _, item := range client.CurrentDiagnostics(path) {
			baseline[item.Key()] = struct{}{}
		}

		version, err := client.OpenOrUpdate(ctx, path, content)
		if err != nil {
			degradeReason = firstNonEmpty(degradeReason, judgeReason(err))
			continue
		}
		if err := client.Save(ctx, path); err != nil {
			degradeReason = firstNonEmpty(degradeReason, judgeReason(err))
			continue
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			degradeReason = firstNonEmpty(degradeReason, "diagnostics wait budget exhausted")
			continue
		}
		items, itemsFresh := client.WaitDiagnostics(ctx, path, version, remaining)
		if !itemsFresh {
			degradeReason = firstNonEmpty(degradeReason, "no fresh diagnostics within "+wait.String())
			continue
		}
		fresh = true
		for _, item := range items {
			if cfg.Scope == ScopeChanged {
				if _, existed := baseline[item.Key()]; existed {
					continue
				}
			}
			collected[item.Key()] = item
		}
	}

	if !fresh {
		outcome.Degraded = true
		outcome.Reason = firstNonEmpty(degradeReason, "diagnostics unavailable")
		outcome.Text = DegradeNote(path, outcome.Reason, cfg.DegradeMode)
		return outcome
	}

	items := make([]Diagnostic, 0, len(collected))
	for _, item := range collected {
		items = append(items, item)
	}
	rendered := RenderDiagnostics(items, RenderOptions{
		File:     path,
		Servers:  outcome.Servers,
		MaxItems: cfg.MaxItems,
		MaxChars: cfg.MaxChars,
		Scope:    cfg.Scope,
	})
	outcome.Items = SortDiagnostics(items)
	outcome.Fresh = true
	outcome.Text = rendered.Text
	outcome.OmittedItems = rendered.OmittedItems
	outcome.OmittedByChars = rendered.OmittedByChars
	if rendered.OmittedItems > 0 || rendered.OmittedByChars > 0 {
		b.logger.Debugf(
			"lsp: %s diagnostics truncated: items_omitted=%d chars_omitted=%d (max_items=%d max_chars=%d)",
			path, rendered.OmittedItems, rendered.OmittedByChars, cfg.MaxItems, cfg.MaxChars,
		)
	}
	return outcome
}

// Report renders the diagnostics for the optional `diagnostics` tool (W7).
// handled=false means no configured server claims the file.
func (b *Bridge) Report(ctx context.Context, path string) (text string, handled bool, err error) {
	if !b.Enabled() {
		return "", false, nil
	}
	resolved := b.resolve(path)
	start := time.Now()
	outcome := b.Diagnose(ctx, resolved)
	if !outcome.Handled {
		b.observeRequest(ctx, "tool", resolved, outcome, 0, time.Since(start))
		return "", false, nil
	}
	text = ""
	switch {
	case outcome.Degraded:
		cfg := b.registry.DiagnosticsConfig()
		note := DegradeNote(resolved, outcome.Reason, cfg.DegradeMode)
		if note == "" {
			note = "LSP diagnostics unavailable: " + outcome.Reason + "\n"
		}
		text = note
	case len(outcome.Items) == 0:
		text = "No diagnostics reported for " + resolved + ".\n"
	default:
		text = outcome.Text
	}
	b.observeRequest(ctx, "tool", resolved, outcome, len(text), time.Since(start))
	return text, true, nil
}

// classifyOutcome 把 Outcome 折叠为低敏枚举（写进事件与读数；前端据此分桶）。
func classifyOutcome(outcome Outcome) string {
	switch {
	case !outcome.Handled:
		return "no_server"
	case outcome.Degraded:
		switch {
		case strings.Contains(outcome.Reason, "no fresh diagnostics"),
			strings.Contains(outcome.Reason, "wait budget"):
			return "degraded_no_fresh"
		case strings.Contains(outcome.Reason, "read file"):
			return "degraded_read_error"
		default:
			return "degraded"
		}
	case len(outcome.Items) == 0:
		return "clean"
	default:
		return "injected"
	}
}

// firstServer 返回参与本次请求的第一个 server（多 server 折叠为单个低敏枚举）。
func firstServer(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimSpace(names[0])
}

// observeRequest 记录并发布一次请求事实：metrics 恒记录（web 页面读数不依赖
// host 接线），host observer 存在时额外投递 EventRequest（observe 平面入口）。
func (b *Bridge) observeRequest(ctx context.Context, trigger, path string, outcome Outcome, appendedBytes int, elapsed time.Duration) {
	if b == nil {
		return
	}
	sessionID := ""
	if b.sessionIDFromContext != nil && ctx != nil {
		sessionID = strings.TrimSpace(b.sessionIDFromContext(ctx))
	}
	record := RequestRecord{
		Time:           time.Now().UTC(),
		Trigger:        trigger,
		Server:         firstServer(outcome.Servers),
		Outcome:        classifyOutcome(outcome),
		DurationMS:     elapsed.Milliseconds(),
		DiagCount:      len(outcome.Items),
		AppendedBytes:  appendedBytes,
		OmittedItems:   outcome.OmittedItems,
		OmittedByChars: outcome.OmittedByChars,
	}
	if b.metrics != nil {
		b.metrics.Observe(record)
	}
	if b.logger != nil {
		b.logger.Debugf(
			"lsp: %s request %s outcome=%s duration_ms=%d diagnostics=%d appended_bytes=%d",
			trigger, path, record.Outcome, record.DurationMS, record.DiagCount, record.AppendedBytes,
		)
	}
	if b.observer != nil {
		b.observer(Event{
			Kind:           EventRequest,
			Time:           record.Time,
			SessionID:      sessionID,
			Server:         record.Server,
			Path:           path,
			Count:          record.DiagCount,
			Trigger:        record.Trigger,
			Outcome:        record.Outcome,
			DurationMS:     record.DurationMS,
			DiagCount:      record.DiagCount,
			AppendedBytes:  record.AppendedBytes,
			OmittedItems:   record.OmittedItems,
			OmittedByChars: record.OmittedByChars,
		})
	}
}

// MetricsSnapshot 返回进程内请求读数（含最近请求明细，最新在前）。
func (b *Bridge) MetricsSnapshot() MetricsSnapshot {
	if b == nil || b.metrics == nil {
		return MetricsSnapshot{}
	}
	return b.metrics.Snapshot()
}

// readyClient waits (inside the shared wait budget) for a server to become
// ready. It never exceeds the deadline and never blocks on a crashed server.
func (b *Bridge) readyClient(ctx context.Context, server *Server, deadline time.Time) (*Client, string) {
	status := server.Status()
	for {
		switch status.State {
		case StateReady:
			client := server.Client()
			if client != nil {
				return client, ""
			}
			return nil, "client unavailable"
		case StateStarting:
		case StateCrashed:
			// A crashed member is replaced automatically while the restart
			// budget lasts (L2); kick the replacement and wait for it inside
			// the shared deadline instead of degrading on first sight of the
			// crash.
			if !server.recoverCrashed(ctx) {
				// Re-check: the replacement may have finished between the
				// status read and the recovery decision.
				if latest := server.Status(); latest.State == StateCrashed {
					return nil, firstNonEmpty(latest.Reason, latest.LastError, string(latest.State))
				}
			}
		default:
			return nil, firstNonEmpty(status.Reason, status.LastError, string(status.State))
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, "server still starting: " + server.Name()
		}
		pause := 20 * time.Millisecond
		if remaining < pause {
			pause = remaining
		}
		select {
		case <-ctx.Done():
			return nil, judgeReason(ctx.Err())
		case <-time.After(pause):
		}
		status = server.Status()
	}
}

func (b *Bridge) resolve(path string) string {
	path = strings.TrimSpace(path)
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(b.registry.Root(), path)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
