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
	// releasePool 归还共享池的引用（私有池为 nil）。最后一个 release 会关停
	// 进程，所以"关停池"从此不再是 Bridge.Stop 的无条件动作。
	releasePool func()
	// detachObserver 退订本 Bridge 在共享 hub 上的观测者。
	detachObserver func()
	// sharedPool 记录本 Bridge 用的是共享池（状态面据此显示"进程内共享 N 个
	// 会话"，而不是让用户以为每个会话都有一份）。
	sharedPool bool
	// stopOnce 保证 Stop 幂等：共享模式下一次 Stop 只能减一次引用计数，
	// 重复 Stop 会把别人在用的池减到 0 并提前关停。
	stopOnce sync.Once
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
	// ColdFastFail records that at least one member used the reduced
	// cold-path retry budget instead of the full diagnostics budget
	// (perf attribution; never rendered into the model-facing text).
	ColdFastFail bool
	// TotalDiagCount / NewDiagCount are the A6 decision data: how many
	// diagnostics the file carried in total and how many of them were new
	// (absent from the pre-change snapshot). With scope=all the model still
	// sees every item, but the pair lets the offline baseline measure how much
	// of the injected payload is pre-existing noise that a scope=changed
	// default would drop. Never rendered into the model-facing text.
	TotalDiagCount int
	NewDiagCount   int
}

// BridgeOptions carries the injectable seams of the facade. Logger feeds both
// the registry (server stderr, lifecycle logs) and the default event observer;
// Observer adds a host-side consumer on top of the logging.
type BridgeOptions struct {
	Logger   Logger
	Dial     DialFunc
	Observer Observer
	// LookPath is the executable preflight seam (nil = exec.LookPath);
	// Dial-based transports skip the preflight and are always available.
	LookPath func(string) (string, error)
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
	registry, hub, release, shared := AcquireSharedRegistry(cfg, root, RegistryOptions{
		Dial:     opts.Dial,
		Logger:   opts.Logger,
		Observer: composeObservers(opts.Observer, logObserver(logger)),
		Now:      opts.Now,
		LookPath: opts.LookPath,
	})
	bridge := &Bridge{
		cfg:                   cfg,
		registry:              registry,
		logger:                logger,
		observer:              opts.Observer,
		metrics:               NewMetrics(),
		sessionIDFromContext:  opts.SessionIDFromContext,
		toolCallIDFromContext: opts.ToolCallIDFromContext,
		turnIDFromContext:     opts.TurnIDFromContext,
		hinted:                map[string]struct{}{},
		releasePool:           release,
		sharedPool:            shared,
	}
	if hub != nil {
		// 共享池的 client 只在构造时拿到一个 Observer 函数，所以事件必须经
		// hub 扇出到每一个借用方。退订与归还引用走同一条路径：否则会话关闭后
		// hub 里还留着一个指向已死会话的观测者。
		bridge.detachObserver = hub.Subscribe(opts.Observer)
	}
	return bridge
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
	// 共享池的所有权在这里第一次变得有条件：私有池由本 Bridge 关停，共享池
	// 只是归还一次引用，最后一个归还者才真正关停进程。stopOnce 保证重复
	// Stop 不会多减一次引用——那会把别人还在用的池提前杀掉。
	b.stopOnce.Do(func() {
		if b.detachObserver != nil {
			b.detachObserver()
		}
		if b.releasePool != nil {
			b.releasePool()
			return
		}
		b.registry.Stop(ctx)
	})
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

// SharedClient hands out a live member client by server name so other in-process
// links (the knowledge semantic channel) can reuse this pool's process instead
// of spawning a second server for the same language. It never takes ownership:
// the caller must not shut the returned client down.
//
// ("", false) means "this pool cannot serve that server right now" — the caller
// decides between degrading and spawning its own process. Only the first is safe
// once this pool owns a member for that language, so the knowledge adapter
// degrades instead of duplicating (see knowledge/lsp Manager.Ensure).
func (b *Bridge) SharedClient(ctx context.Context, serverName string) (*Client, bool) {
	if b == nil {
		return nil, false
	}
	return b.registry.SharedClient(ctx, serverName)
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
		// Deleted or moved-away paths (apply_patch delete/move) have nothing to
		// diagnose: os.ReadFile would fail and turn a normal delete into an
		// "LSP diagnostics unavailable: read file ..." note. Directories are not
		// documents either. Skip both silently (no request, no note).
		if info, statErr := os.Stat(resolved); statErr != nil || info.IsDir() {
			continue
		}
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
	allCollected := make(map[string]Diagnostic)
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
		// this request. Once the grace is spent and *this path* still has no
		// snapshot, the path is known cold: later edits use a reduced retry
		// budget instead of re-paying the full wait on every edit during a long
		// view load (live evidence: ~85s), and the first publish for the path
		// restores the normal route. The signal is per path, not per
		// connection: live window had 11/20 no_fresh requests on paths that had
		// never published anything while the connection was already warm for
		// other paths (one path burned the full budget six times), which the
		// old connection-level condition let through.
		coldBudget := time.Duration(cfg.ColdRetryMS) * time.Millisecond
		if cold := coldBudget; cold > 0 && client.ColdWait(path) {
			if cold < remaining {
				remaining = cold
			}
			outcome.ColdFastFail = true
		} else if grace := time.Duration(cfg.ColdStartGraceMS) * time.Millisecond; grace > 0 &&
			!client.HasSnapshot(path) && client.TakeColdGrace(path) {
			remaining += grace
		}
		// 实际等待预算（可能被冷快速失败缩减、或被冷启动宽限延长）：降级
		// 文案必须报告真实预算，否则"within 1s"会在只等 250ms 时误导模型与日志。
		budget := remaining
		items, itemsFresh := client.WaitDiagnostics(ctx, path, version, budget)
		if !itemsFresh {
			// A path with no snapshot that just spent more than the reduced
			// budget is known cold; later edits fail fast until the path's own
			// publish clears the mark. The signal is per path, not per
			// connection: live window had 11/20 no_fresh requests on paths that
			// had never published anything while the connection was already
			// warm for other paths (one path burned the full budget six times).
			if coldBudget > 0 && !client.HasSnapshot(path) && remaining > coldBudget {
				client.MarkColdWait(path)
			}
			reason := "no fresh diagnostics within " + budget.String()
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
			allCollected[item.Key()] = item
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
	outcome.TotalDiagCount = len(allCollected)
	for key := range allCollected {
		if _, existed := baseline[key]; !existed {
			outcome.NewDiagCount++
		}
	}

	items := make([]Diagnostic, 0, len(collected))
	for _, item := range collected {
		items = append(items, item)
	}
	rendered := RenderDiagnostics(items, RenderOptions{
		File:         path,
		Servers:      outcome.Servers,
		MaxItems:     cfg.MaxItems,
		MaxChars:     cfg.MaxChars,
		Scope:        cfg.Scope,
		EmptyCompact: cfg.EmptyStyleValue() == EmptyStyleCompact,
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
		TotalDiagCount:     outcome.TotalDiagCount,
		NewDiagCount:       outcome.NewDiagCount,
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
		ColdFastFail:       outcome.ColdFastFail,
		AttemptedMembers:   len(outcome.Servers),
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
			Kind:               EventRequest,
			Time:               record.Time,
			SessionID:          sessionID,
			ToolCallID:         toolCallID,
			TurnID:             turnID,
			Server:             record.Server,
			Path:               path,
			PathFingerprint:    pathFingerprint,
			DiagFingerprint:    outcome.DiagFingerprint,
			Count:              record.DiagCount,
			Trigger:            record.Trigger,
			Outcome:            record.Outcome,
			DurationMS:         record.DurationMS,
			DiagCount:          record.DiagCount,
			TotalDiagCount:     record.TotalDiagCount,
			NewDiagCount:       record.NewDiagCount,
			AppendedBytes:      record.AppendedBytes,
			OmittedItems:       record.OmittedItems,
			OmittedByChars:     record.OmittedByChars,
			ReasonCategory:     reasonCategory,
			AppendedDiagBytes:  record.AppendedDiagBytes,
			AppendedNoteBytes:  record.AppendedNoteBytes,
			AppendedEmptyBytes: record.AppendedEmptyBytes,
			ColdFastFail:       record.ColdFastFail,
			AttemptedMembers:   record.AttemptedMembers,
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
	client, reason := waitReadyClientDetailed(ctx, server, deadline, startWait)
	return client, reason
}

// waitReadyClient is the reason-free form of waitReadyClientDetailed, used by
// the single-instance borrow seam (Registry.SharedClient) where the reason is
// not surfaced to the model.
func waitReadyClient(ctx context.Context, server *Server, deadline time.Time, startWait time.Duration) *Client {
	client, _ := waitReadyClientDetailed(ctx, server, deadline, startWait)
	return client
}

func waitReadyClientDetailed(ctx context.Context, server *Server, deadline time.Time, startWait time.Duration) (*Client, string) {
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
