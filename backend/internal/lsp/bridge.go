package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
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
	// toolCallIDFromContext / turnIDFromContext 解析请求事件 join 键（可选）。
	toolCallIDFromContext func(context.Context) string
	turnIDFromContext     func(context.Context) string
	// hinted 记录已展示过的降级提示（HintOnce），避免每次编辑重复同一段
	// 噪声；池状态仍可通过 lsp_servers / /lsp status 查看。
	hintMu sync.Mutex
	hinted map[string]struct{}
	// prewarmMu guards prewarming: when a request degrades while the server
	// is still handshaking, a background task opens+saves the document as soon
	// as the server is ready, so the first module analysis starts immediately
	// instead of waiting for the next edit (live evidence: a cold gopls view
	// can take tens of seconds to publish its first diagnostics).
	prewarmMu  sync.Mutex
	prewarming map[string]struct{}
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
	// DiagFingerprint identifies the diagnostic set ("" for clean/degraded).
	// It lets the offline baseline compute edit→diagnostic closure without
	// persisting diagnostic text (observability plan §3.3).
	DiagFingerprint string
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
	// ToolCallIDFromContext / TurnIDFromContext 解析工具调用与回合归属
	// （可选）：观测方案 §3.1 要求请求事件可 join 到 tool receipt。
	ToolCallIDFromContext func(context.Context) string
	TurnIDFromContext     func(context.Context) string
	// Now is the injectable clock (restart-window tests). nil = time.Now.
	Now func() time.Time
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
		cfg:                   cfg,
		registry:              NewRegistry(cfg, root, RegistryOptions{Dial: opts.Dial, Logger: opts.Logger, Observer: observer, Now: opts.Now}),
		logger:                logger,
		observer:              opts.Observer,
		metrics:               NewMetrics(),
		sessionIDFromContext:  opts.SessionIDFromContext,
		toolCallIDFromContext: opts.ToolCallIDFromContext,
		turnIDFromContext:     opts.TurnIDFromContext,
		hinted:                map[string]struct{}{},
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
		if outcome.Degraded && !b.claimDegradeHint(outcome) {
			// The same failure was already explained once in this session;
			// keep later edit results free of repeated notes.
			outcome.Text = ""
		}
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
	startWait := time.Duration(cfg.StartWaitMS) * time.Millisecond
	if startWait <= 0 || startWait > wait {
		startWait = wait
	}

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
		// Each member gets its own budget: a slow or absent first server must
		// not starve the others (multi-server workspaces, docs/lsp 02 §6 Q4).
		serverDeadline := time.Now().Add(wait)
		client, reason := b.readyClient(ctx, server, serverDeadline, startWait)
		if client == nil {
			degradeReason = firstNonEmpty(degradeReason, reason)
			if strings.HasPrefix(reason, "server still starting") {
				// Fast-fail is right for this edit, but the view load should
				// not wait for the next one: prewarm it in the background.
				b.prewarm(server, path)
			}
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
		remaining := time.Until(serverDeadline)
		if remaining <= 0 {
			degradeReason = firstNonEmpty(degradeReason, "diagnostics wait budget exhausted")
			continue
		}
		// Cold view handling. First sight of a cold path (nothing published
		// yet) gets one bounded extension so a medium view load lands inside
		// this request. Once the grace is spent and the connection still has
		// never published, the path is known cold: later edits use a reduced
		// retry budget instead of re-paying the full wait on every edit during
		// a long view load (live evidence: ~85s), and the first edit after the
		// publish gets the real result.
		if cold := time.Duration(cfg.ColdRetryMS) * time.Millisecond; cold > 0 && client.ColdWait(path) {
			if cold < remaining {
				remaining = cold
			}
		} else if grace := time.Duration(cfg.ColdStartGraceMS) * time.Millisecond; grace > 0 &&
			!client.HasSnapshot(path) && client.TakeColdGrace(path) {
			remaining += grace
		}
		items, itemsFresh := client.WaitDiagnostics(ctx, path, version, remaining)
		if !itemsFresh {
			if !client.HasSnapshot(path) && client.ColdGraceUsed(path) && !client.EverPublished() {
				client.MarkColdWait(path)
			}
			reason := "no fresh diagnostics within " + wait.String()
			// Distinguish "server analyzed and had nothing" from "server never
			// published anything": the latter usually means the file is outside
			// the server's module or inside a directory the toolchain ignores
			// (dot/underscore dirs for gopls) — a live-debugging trap that a
			// bare timeout reason hides.
			if !client.HasSnapshot(path) {
				reason += ": server published nothing (file may be outside its module, in an ignored directory, or the server is still loading its first analysis; retry shortly)"
			}
			degradeReason = firstNonEmpty(degradeReason, reason)
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
	outcome.DiagFingerprint = DiagnosticsFingerprint(outcome.Items)
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
// 原因折叠复用 ReasonCategory：outcome 与 reason_category 两个落盘字段共用同一
// 事实源，不会各自漂移；所有可操作的降级原因都必须有细分类别，裸 "degraded"
// 只兜底真正未知的原因（live 证据：缺二进制曾被折叠成裸 degraded，无法与崩溃/
// 传输关闭区分，基线与告警都无法据此行动）。
func classifyOutcome(outcome Outcome) string {
	switch {
	case !outcome.Handled:
		return "no_server"
	case outcome.Degraded:
		switch ReasonCategory(outcome.Reason) {
		case "no_publish", "wait_timeout":
			return "degraded_no_fresh"
		case "starting":
			return "degraded_starting"
		case "read_error":
			return "degraded_read_error"
		case "binary_missing":
			return "degraded_binary_missing"
		case "crashed", "restart_budget_exhausted":
			return "degraded_crashed"
		case "transport_closed":
			return "degraded_transport_closed"
		case "canceled":
			return "degraded_canceled"
		default:
			return "degraded"
		}
	case len(outcome.Items) == 0:
		return "clean"
	default:
		return "injected"
	}
}

// appendSplit classifies appended bytes into diagnostic value / degradation
// note / empty-result block. Only the first kind is LSP value; the other two
// are protocol overhead that the readout keeps separate (plan §3.3).
func appendSplit(outcome Outcome, appended int) (diag, note, empty int) {
	switch {
	case appended <= 0:
		return 0, 0, 0
	case outcome.Degraded:
		return 0, appended, 0
	case len(outcome.Items) == 0:
		return 0, 0, appended
	default:
		return appended, 0, 0
	}
}

// firstServer 返回参与本次请求的第一个 server（多 server 折叠为单个低敏枚举）。
func firstServer(names []string) string {
	if len(names) == 0 {
		return ""
	}
	return strings.TrimSpace(names[0])
}

// claimDegradeHint reports whether the inline path may append the degradation
// note for this outcome. With HintOnce effective, the same (server-set,
// reason) pair is explained once per session; repeating it on every edit is
// token noise, while the pool state stays observable through `lsp_servers` /
// `/lsp status`. Explicit `lsp_diagnostics` calls always answer (Report).
func (b *Bridge) claimDegradeHint(outcome Outcome) bool {
	if b == nil || !b.cfg.Diagnostics.HintOnceValue() {
		return true
	}
	key := strings.Join(outcome.Servers, ",") + "|" + truncateReason(strings.TrimSpace(outcome.Reason))
	b.hintMu.Lock()
	_, seen := b.hinted[key]
	if !seen {
		b.hinted[key] = struct{}{}
	}
	b.hintMu.Unlock()
	return !seen
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
	toolCallID, turnID := "", ""
	if ctx != nil {
		if b.toolCallIDFromContext != nil {
			toolCallID = strings.TrimSpace(b.toolCallIDFromContext(ctx))
		}
		if b.turnIDFromContext != nil {
			turnID = strings.TrimSpace(b.turnIDFromContext(ctx))
		}
	}
	pathFingerprint := FingerprintPath(path)
	diagBytes, noteBytes, emptyBytes := appendSplit(outcome, appendedBytes)
	// Low-sensitivity degrade category (plan §3.1/§3.3): the free-form reason
	// may embed stderr chatter, so only the enum is persisted.
	reasonCategory := ReasonCategory(outcome.Reason)
	record := RequestRecord{
		Time:               time.Now().UTC(),
		Trigger:            trigger,
		Server:             firstServer(outcome.Servers),
		Outcome:            classifyOutcome(outcome),
		DurationMS:         elapsed.Milliseconds(),
		DiagCount:          len(outcome.Items),
		AppendedBytes:      appendedBytes,
		OmittedItems:       outcome.OmittedItems,
		OmittedByChars:     outcome.OmittedByChars,
		ToolCallID:         toolCallID,
		TurnID:             turnID,
		PathFingerprint:    pathFingerprint,
		DiagFingerprint:    outcome.DiagFingerprint,
		AppendedDiagBytes:  diagBytes,
		AppendedNoteBytes:  noteBytes,
		AppendedEmptyBytes: emptyBytes,
		ReasonCategory:     reasonCategory,
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
			Kind:            EventRequest,
			Time:            record.Time,
			SessionID:       sessionID,
			ToolCallID:      toolCallID,
			TurnID:          turnID,
			Server:          record.Server,
			Path:            path,
			PathFingerprint: pathFingerprint,
			DiagFingerprint: outcome.DiagFingerprint,
			Count:           record.DiagCount,
			Trigger:         record.Trigger,
			Outcome:         record.Outcome,
			DurationMS:      record.DurationMS,
			DiagCount:       record.DiagCount,
			AppendedBytes:   record.AppendedBytes,
			OmittedItems:    record.OmittedItems,
			OmittedByChars:  record.OmittedByChars,
			ReasonCategory:  reasonCategory,
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

// readyClient waits (inside the per-server wait budget) for a server to become
// ready. A member that is still handshaking gets at most startWait, so a cold
// start degrades quickly instead of burning the full diagnostics budget; the
// background start keeps going for the next edit.
func (b *Bridge) readyClient(ctx context.Context, server *Server, deadline time.Time, startWait time.Duration) (*Client, string) {
	status := server.Status()
	var startDeadline time.Time
	for {
		switch status.State {
		case StateReady:
			client := server.Client()
			if client != nil {
				return client, ""
			}
			return nil, "client unavailable"
		case StateStarting:
			if startDeadline.IsZero() {
				startDeadline = time.Now().Add(startWait)
			}
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
		waitUntil := deadline
		if status.State == StateStarting && startDeadline.Before(waitUntil) {
			waitUntil = startDeadline
		}
		remaining := time.Until(waitUntil)
		if remaining <= 0 {
			switch status.State {
			case StateStarting:
				return nil, "server still starting: " + server.Name()
			case StateCrashed:
				return nil, firstNonEmpty(status.Reason, status.LastError, "crashed: "+server.Name())
			default:
				return nil, firstNonEmpty(status.Reason, status.LastError, string(status.State))
			}
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

// prewarmTimeout bounds the background wait for a starting server plus the
// open/save round trip. Prewarming exists only to start the first analysis
// earlier; it must never outlive its usefulness.
const prewarmTimeout = 20 * time.Second

// prewarm opens and saves path once a starting server becomes ready, in the
// background, so the first module analysis begins immediately instead of
// waiting for the next edit. Concurrent prewarms for the same (server, path)
// are deduped; failures are silent (the foreground path stays authoritative).
func (b *Bridge) prewarm(server *Server, path string) {
	if b == nil || server == nil || strings.TrimSpace(path) == "" {
		return
	}
	key := server.Name() + "|" + path
	b.prewarmMu.Lock()
	if b.prewarming == nil {
		b.prewarming = map[string]struct{}{}
	}
	if _, ok := b.prewarming[key]; ok {
		b.prewarmMu.Unlock()
		return
	}
	b.prewarming[key] = struct{}{}
	b.prewarmMu.Unlock()

	go func() {
		defer func() {
			b.prewarmMu.Lock()
			delete(b.prewarming, key)
			b.prewarmMu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(context.Background(), prewarmTimeout)
		defer cancel()
		client, _ := b.readyClient(ctx, server, time.Now().Add(prewarmTimeout), prewarmTimeout)
		if client == nil {
			return
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return
		}
		if _, err := client.OpenOrUpdate(ctx, path, content); err != nil {
			return
		}
		_ = client.Save(ctx, path)
	}()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
