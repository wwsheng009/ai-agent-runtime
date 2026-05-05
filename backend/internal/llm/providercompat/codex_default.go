package providercompat

import "strings"

type codexDefaultAdapter struct {
	BaseAdapter
}

func (codexDefaultAdapter) Name() string {
	return "codex-default"
}

func (codexDefaultAdapter) Match(ctx Context) bool {
	return ctx.Protocol == "codex"
}

func (codexDefaultAdapter) DefaultLoginReasoningEfforts(Context) ([]string, bool) {
	return []string{"low", "medium", "high", "xhigh", "none"}, true
}

func (codexDefaultAdapter) LoginUsesWildcardReasoningEfforts(Context) (bool, bool) {
	return true, true
}

type codexPathAdapter struct {
	BaseAdapter
}

func (codexPathAdapter) Name() string {
	return "codex-path"
}

func (codexPathAdapter) Match(ctx Context) bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(ctx.BaseURL)), "/codex")
}

func (codexPathAdapter) LoginUsesWildcardReasoningEfforts(Context) (bool, bool) {
	return true, true
}
