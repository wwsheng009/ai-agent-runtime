package agentconfig

import (
	"reflect"
	"testing"
)

func TestMatchResponseMarkerRule_EmptyModelsMatchesAll(t *testing.T) {
	rule := ResponseMarkerRule{Markers: []string{"]<]minimax[>["}}
	if !MatchResponseMarkerRule(rule, "minimax-m3") {
		t.Fatal("empty models must match every model")
	}
	if !MatchResponseMarkerRule(rule, "gpt-5") {
		t.Fatal("empty models must match every model")
	}
	if MatchResponseMarkerRule(rule, "") {
		t.Fatal("empty model must not match")
	}
}

func TestMatchResponseMarkerRule_GlobPattern(t *testing.T) {
	rule := ResponseMarkerRule{Models: []string{"*minimax*"}}
	for _, model := range []string{"minimax-m3", "MiniMax-M3", "abab6-minimax"} {
		if !MatchResponseMarkerRule(rule, model) {
			t.Fatalf("glob *minimax* must match %q", model)
		}
	}
	for _, model := range []string{"gpt-5", "deepseek-v4"} {
		if MatchResponseMarkerRule(rule, model) {
			t.Fatalf("glob *minimax* must not match %q", model)
		}
	}
}

func TestMatchResponseMarkerRule_ExactAndAnyOf(t *testing.T) {
	rule := ResponseMarkerRule{Models: []string{"deepseek-v4-flash", "gpt-*"}}
	if !MatchResponseMarkerRule(rule, "deepseek-v4-flash") {
		t.Fatal("exact model must match")
	}
	if !MatchResponseMarkerRule(rule, "gpt-5.1") {
		t.Fatal("glob pattern must match")
	}
	if MatchResponseMarkerRule(rule, "claude-3") {
		t.Fatal("unrelated model must not match")
	}
}

func TestResolveResponseMarkers_UnionOfMatchingRules(t *testing.T) {
	rules := []ResponseMarkerRule{
		{Models: []string{"*minimax*"}, Markers: []string{"]<]minimax[>["}},
		{Models: []string{"minimax-m3"}, Markers: []string{"<|minimax|>"}},
		{Models: []string{"deepseek*"}, Markers: []string{"<|deepseek|>"}},
	}
	got := ResolveResponseMarkers(rules, "minimax-m3")
	want := []string{"]<]minimax[>[", "<|minimax|>"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveResponseMarkers = %#v, want %#v", got, want)
	}
	if got := ResolveResponseMarkers(rules, "deepseek-v4"); len(got) != 1 {
		t.Fatalf("deepseek rules = %#v, want 1 marker", got)
	}
	if got := ResolveResponseMarkers(rules, "gpt-5"); got != nil {
		t.Fatalf("unmatched model must resolve nil, got %#v", got)
	}
	if got := ResolveResponseMarkers(nil, "minimax-m3"); got != nil {
		t.Fatalf("nil rules must resolve nil, got %#v", got)
	}
}

func TestResolveResponseMarkers_SkipsEmptyMarkers(t *testing.T) {
	rules := []ResponseMarkerRule{
		{Models: nil, Markers: []string{" ", "ok"}},
	}
	got := ResolveResponseMarkers(rules, "any")
	if !reflect.DeepEqual(got, []string{"ok"}) {
		t.Fatalf("ResolveResponseMarkers = %#v, want [ok]", got)
	}
}

func TestStripMarkers(t *testing.T) {
	markers := []string{"]<]minimax[>["}
	got := StripMarkers("a]<]minimax[>[b]<]minimax[>[c", markers)
	if got != "abc" {
		t.Fatalf("StripMarkers = %q, want abc", got)
	}
	if got := StripMarkers("no marker", markers); got != "no marker" {
		t.Fatalf("StripMarkers unchanged = %q", got)
	}
	if got := StripMarkers("x", nil); got != "x" {
		t.Fatalf("nil markers must pass through")
	}
}

func TestStripMarkersBytes_MultipleMarkers(t *testing.T) {
	markers := []string{"<a>", "<b>"}
	got := string(StripMarkersBytes([]byte("1<a>2<b>3<a>4"), markers))
	if got != "1234" {
		t.Fatalf("StripMarkersBytes = %q, want 1234", got)
	}
	if got := string(StripMarkersBytes([]byte("abc"), nil)); got != "abc" {
		t.Fatalf("nil markers must pass through bytes")
	}
}

func TestStripMarkersBytes_PreservesUTF8(t *testing.T) {
	markers := []string{"]<]minimax[>["}
	input := "中文]<]minimax[>[测试"
	got := StripMarkersBytes([]byte(input), markers)
	if string(got) != "中文测试" {
		t.Fatalf("StripMarkersBytes = %q, want 中文测试", got)
	}
}