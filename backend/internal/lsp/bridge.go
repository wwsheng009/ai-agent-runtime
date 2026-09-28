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

// NewBridge builds the facade. dial may be nil (SpawnProcess is used).
func NewBridge(cfg Config, root string, logger Logger, dial DialFunc) *Bridge {
	cfg = cfg.Normalize()
	return &Bridge{
		cfg:      cfg,
		registry: NewRegistry(cfg, root, RegistryOptions{Dial: dial, Logger: logger}),
		logger:   LoggerOrNop(logger),
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
		outcome := b.Diagnose(ctx, resolved)
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
	outcome := b.Diagnose(ctx, resolved)
	if !outcome.Handled {
		return "", false, nil
	}
	switch {
	case outcome.Degraded:
		cfg := b.registry.DiagnosticsConfig()
		note := DegradeNote(resolved, outcome.Reason, cfg.DegradeMode)
		if note == "" {
			note = "LSP diagnostics unavailable: " + outcome.Reason + "\n"
		}
		return note, true, nil
	case len(outcome.Items) == 0:
		return "No diagnostics reported for " + resolved + ".\n", true, nil
	default:
		return outcome.Text, true, nil
	}
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
