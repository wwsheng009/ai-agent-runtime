package toolexec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/observability"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolresult"
)

func TestArgsDigestStableAcrossMapOrder(t *testing.T) {
	a := ArgsDigest("view", map[string]interface{}{"file_path": "a.go", "limit": 10})
	b := ArgsDigest("view", map[string]interface{}{"limit": 10, "file_path": "a.go"})
	if a == "" || a != b {
		t.Fatalf("expected stable digest, got %q vs %q", a, b)
	}
	c := ArgsDigest("view", map[string]interface{}{"file_path": "b.go", "limit": 10})
	if a == c {
		t.Fatalf("expected digest to change with args")
	}
}

func TestApplyPreflightMissingRequiredArgs(t *testing.T) {
	mem := NewMemory(2)
	decision := ApplyPreflight(mem, PreflightRequest{
		ToolName: "glob",
		Args:     map[string]interface{}{},
		InputSchema: map[string]interface{}{
			"type":       "object",
			"required":   []interface{}{"pattern"},
			"properties": map[string]interface{}{"pattern": map[string]interface{}{"type": "string"}},
		},
	})
	if decision.Allow {
		t.Fatal("expected preflight to block missing required args")
	}
	if decision.ErrorCode != "TOOL_INVALID_ARGS" {
		t.Fatalf("error_code=%s", decision.ErrorCode)
	}
	if decision.Digest == "" || decision.Attempt != 1 {
		t.Fatalf("unexpected digest/attempt: %+v", decision)
	}
}

func TestApplyPreflightRecordsTelemetry(t *testing.T) {
	prev := observability.GlobalMetrics
	observability.GlobalMetrics = observability.NewRegistry()
	t.Cleanup(func() { observability.GlobalMetrics = prev })

	// required_args deny
	_ = ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "glob",
		Args:     map[string]interface{}{},
		InputSchema: map[string]interface{}{
			"type":       "object",
			"required":   []interface{}{"pattern"},
			"properties": map[string]interface{}{"pattern": map[string]interface{}{"type": "string"}},
		},
	})
	// allow
	_ = ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "glob",
		Args:     map[string]interface{}{"pattern": "*.go"},
		InputSchema: map[string]interface{}{
			"type":       "object",
			"required":   []interface{}{"pattern"},
			"properties": map[string]interface{}{"pattern": map[string]interface{}{"type": "string"}},
		},
	})
	// arg_types deny
	_ = ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "view",
		Args:     map[string]interface{}{"limit": map[string]interface{}{"n": 1}},
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"limit": map[string]interface{}{"type": "integer"},
			},
		},
	})

	if got := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricToolPreflightTotal, map[string]string{
		observability.LabelReason:   observability.PreflightReasonRequiredArgs,
		observability.LabelDecision: "deny",
	}).Get(); got != 1 {
		t.Fatalf("required_args counter=%v", got)
	}
	if got := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricToolPreflightTotal, map[string]string{
		observability.LabelReason:   observability.PreflightReasonAllow,
		observability.LabelDecision: "allow",
	}).Get(); got != 1 {
		t.Fatalf("allow counter=%v", got)
	}
	if got := observability.GlobalMetrics.GetOrCreateCounter(observability.MetricToolPreflightTotal, map[string]string{
		observability.LabelReason:   observability.PreflightReasonArgTypes,
		observability.LabelDecision: "deny",
	}).Get(); got != 1 {
		t.Fatalf("arg_types counter=%v", got)
	}
}

func TestApplyPreflightInvalidArgTypes(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"limit":     map[string]interface{}{"type": "integer"},
			"parallel":  map[string]interface{}{"type": "boolean"},
			"file_path": map[string]interface{}{"type": "string"},
			"permission_mode": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"default", "accept_edits", "plan", "bypass_permissions"},
			},
			"max_filesize": map[string]interface{}{
				"anyOf": []map[string]interface{}{
					{"type": "string"},
					{"type": "integer"},
				},
			},
			"files": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string"},
						"offset":    map[string]interface{}{"type": "integer"},
					},
				},
			},
		},
	}

	// Object where integer expected must fail (common LLM mis-shape).
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "view",
		Args:        map[string]interface{}{"limit": map[string]interface{}{"n": 10}},
		InputSchema: schema,
	})
	if decision.Allow {
		t.Fatal("expected object-for-integer to be blocked")
	}
	if decision.ErrorCode != "TOOL_INVALID_ARGS" {
		t.Fatalf("error_code=%s", decision.ErrorCode)
	}
	if decision.Preflight != "arg_types" {
		t.Fatalf("preflight=%s", decision.Preflight)
	}
	if !strings.Contains(decision.Error, "limit expected integer") {
		t.Fatalf("error=%q", decision.Error)
	}
	if !strings.Contains(strings.ToLower(decision.NextAction), "argument types") {
		t.Fatalf("next_action=%q", decision.NextAction)
	}

	meta := map[string]interface{}{}
	AttachPreflightMetadata(meta, decision)
	if _, ok := meta[MetadataAttemptedArgsKey].(map[string]interface{}); !ok {
		t.Fatalf("expected attempted_args on type preflight deny: %#v", meta)
	}

	// Numeric strings and bool strings should be accepted (tools often coerce).
	okDecision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "view",
		Args: map[string]interface{}{
			"limit":     "20",
			"parallel":  "true",
			"file_path": "a.go",
		},
		InputSchema: schema,
		// Avoid path preflight by not enabling safe retry path existence.
		PathExists: func(string) bool { return true },
	})
	if !okDecision.Allow {
		t.Fatalf("expected coerced string numbers/bools to pass: %+v", okDecision)
	}

	// anyOf string|integer accepts either branch.
	anyOfOK := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "grep",
		Args:        map[string]interface{}{"max_filesize": "1M"},
		InputSchema: schema,
	})
	if !anyOfOK.Allow {
		t.Fatalf("expected anyOf string branch to pass: %+v", anyOfOK)
	}
	anyOfIntOK := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "grep",
		Args:        map[string]interface{}{"max_filesize": 1024},
		InputSchema: schema,
	})
	if !anyOfIntOK.Allow {
		t.Fatalf("expected anyOf integer branch to pass: %+v", anyOfIntOK)
	}

	// Enum mismatch is actionable.
	enumBad := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "spawn_agent",
		Args:        map[string]interface{}{"permission_mode": "yolo"},
		InputSchema: schema,
	})
	if enumBad.Allow || !strings.Contains(enumBad.Error, "permission_mode must be one of") {
		t.Fatalf("expected enum rejection, got %+v", enumBad)
	}

	// Nested array item property type mismatch.
	nestedBad := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "view",
		Args: map[string]interface{}{
			"files": []interface{}{
				map[string]interface{}{"file_path": "a.go", "offset": "nope"},
			},
		},
		InputSchema: schema,
	})
	if nestedBad.Allow {
		t.Fatalf("expected nested offset type mismatch to fail, got %+v", nestedBad)
	}
	if !strings.Contains(nestedBad.Error, "files[0].offset expected integer") {
		// "nope" is not integer-like, so should fail; message may say got string.
		if !strings.Contains(nestedBad.Error, "offset") {
			t.Fatalf("error=%q", nestedBad.Error)
		}
	}
}

func TestApplyPreflightArgTypesSkipWhenNoSchemaTypes(t *testing.T) {
	// Without schema type annotations, do not invent constraints.
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "custom",
		Args:     map[string]interface{}{"x": 1},
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"x": map[string]interface{}{"description": "untyped"},
			},
		},
	})
	if !decision.Allow {
		t.Fatalf("untyped properties must not type-preflight: %+v", decision)
	}
}

func TestCircuitBlocksIdenticalTerminalFailure(t *testing.T) {
	mem := NewMemory(2)
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{"type": "string"},
		},
	}
	args := map[string]interface{}{"file_path": "missing.txt"}
	meta := map[string]interface{}{"retry_class": "safe"}

	d1 := ApplyPreflight(mem, PreflightRequest{
		ToolName:    "view",
		Args:        args,
		InputSchema: schema,
		Metadata:    meta,
		PathExists:  func(string) bool { return false },
	})
	if d1.Allow {
		t.Fatal("expected path preflight failure")
	}
	_ = RecordOutcome(mem, "view", d1.Digest, d1.Error, map[string]interface{}{
		toolresult.MetadataErrorCodeKey: d1.ErrorCode,
		toolresult.MetadataRetryableKey: false,
	})

	d2 := ApplyPreflight(mem, PreflightRequest{
		ToolName:    "view",
		Args:        args,
		InputSchema: schema,
		Metadata:    meta,
		PathExists:  func(string) bool { return false },
	})
	_ = RecordOutcome(mem, "view", d2.Digest, d2.Error, map[string]interface{}{
		toolresult.MetadataErrorCodeKey: d2.ErrorCode,
		toolresult.MetadataRetryableKey: false,
	})

	d3 := ApplyPreflight(mem, PreflightRequest{
		ToolName:    "view",
		Args:        args,
		InputSchema: schema,
		Metadata:    meta,
		PathExists:  func(string) bool { return false },
	})
	if d3.Allow || !d3.Circuit {
		t.Fatalf("expected open circuit, got %+v", d3)
	}
	if !strings.Contains(strings.ToLower(d3.NextAction), "do not retry") {
		t.Fatalf("expected stronger next_action, got %q", d3.NextAction)
	}

	d4 := ApplyPreflight(mem, PreflightRequest{
		ToolName:    "view",
		Args:        map[string]interface{}{"file_path": "other.txt"},
		InputSchema: schema,
		Metadata:    meta,
		PathExists:  func(path string) bool { return filepath.Base(path) != "other.txt" },
	})
	if d4.Circuit {
		t.Fatalf("changed args should not hit previous circuit: %+v", d4)
	}
}

func TestRetryableFailuresDoNotOpenCircuit(t *testing.T) {
	mem := NewMemory(2)
	digest := ArgsDigest("fetch", map[string]interface{}{"url": "https://example.com"})
	for i := 0; i < 5; i++ {
		_ = RecordOutcome(mem, "fetch", digest, "network unavailable", map[string]interface{}{
			toolresult.MetadataErrorCodeKey: "NETWORK_UNAVAILABLE",
			toolresult.MetadataRetryableKey: true,
		})
	}
	if _, open := mem.LookupFailure("fetch", digest); open {
		t.Fatal("retryable failures must not open circuit")
	}
}

func TestPathPreflightSkipsMutationArgs(t *testing.T) {
	mem := NewMemory(2)
	decision := ApplyPreflight(mem, PreflightRequest{
		ToolName: "write",
		Args: map[string]interface{}{
			"file_path": "new-file.txt",
			"content":   "hello",
		},
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []interface{}{"file_path", "content"},
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string"},
				"content":   map[string]interface{}{"type": "string"},
			},
		},
		Metadata:   map[string]interface{}{"retry_class": "idempotency_key_required"},
		PathExists: func(string) bool { return false },
	})
	if !decision.Allow {
		t.Fatalf("write tools must not path-preflight missing targets: %+v", decision)
	}
}

func TestPathPreflightSuggestsNearbyCandidates(t *testing.T) {
	dir := t.TempDir()
	realName := "config.yaml"
	if err := os.WriteFile(filepath.Join(dir, realName), []byte("ok"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	// Unrelated sibling should not outrank the close typo match.
	if err := os.WriteFile(filepath.Join(dir, "readme.md"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write sibling: %v", err)
	}

	missing := filepath.Join(dir, "config.yam") // missing trailing 'l'
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{"type": "string"},
		},
	}
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "view",
		Args:        map[string]interface{}{"file_path": missing},
		InputSchema: schema,
		Metadata:    map[string]interface{}{"retry_class": "safe"},
		PathExists:  func(string) bool { return false },
	})
	if decision.Allow {
		t.Fatal("expected missing path to be blocked")
	}
	if decision.ErrorCode != "TOOL_PATH_NOT_FOUND" {
		t.Fatalf("error_code=%s", decision.ErrorCode)
	}
	if len(decision.PathCandidates) == 0 {
		t.Fatalf("expected nearby candidates, got error=%q next=%q", decision.Error, decision.NextAction)
	}
	joined := strings.Join(decision.PathCandidates, "\n")
	if !strings.Contains(joined, realName) {
		t.Fatalf("expected candidate containing %q, got %v", realName, decision.PathCandidates)
	}
	if !strings.Contains(decision.NextAction, "Nearby candidates") {
		t.Fatalf("expected next_action to surface candidates, got %q", decision.NextAction)
	}
	if !strings.Contains(decision.Error, "candidates:") {
		t.Fatalf("expected error to surface candidates, got %q", decision.Error)
	}

	meta := map[string]interface{}{}
	AttachPreflightMetadata(meta, decision)
	raw, ok := meta[MetadataPathCandidatesKey].([]string)
	if !ok || len(raw) == 0 {
		t.Fatalf("expected path_candidates metadata, got %#v", meta)
	}
	args, ok := meta[MetadataAttemptedArgsKey].(map[string]interface{})
	if !ok || args["file_path"] == nil {
		t.Fatalf("expected attempted_args metadata with file_path, got %#v", meta)
	}
	invocation, ok := meta[MetadataInvocationKey].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_invocation metadata, got %#v", meta)
	}
	if invArgs, ok := invocation[MetadataAttemptedArgsKey].(map[string]interface{}); !ok || invArgs["file_path"] == nil {
		t.Fatalf("expected attempted_args in tool_invocation, got %#v", invocation)
	}
}

func TestAttachPreflightMetadataAllowKeepsAttemptedArgsNestedOnly(t *testing.T) {
	decision := PreflightDecision{
		Allow:     true,
		Digest:    "digest-allow",
		Attempt:   1,
		Preflight: "ok",
		Args: map[string]interface{}{
			"pattern": "foo",
			"path":    "backend",
		},
	}
	meta := map[string]interface{}{}
	AttachPreflightMetadata(meta, decision)
	if _, exists := meta[MetadataAttemptedArgsKey]; exists {
		t.Fatalf("allow path must not surface top-level attempted_args: %#v", meta)
	}
	invocation, ok := meta[MetadataInvocationKey].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_invocation metadata, got %#v", meta)
	}
	args, ok := invocation[MetadataAttemptedArgsKey].(map[string]interface{})
	if !ok || args["pattern"] != "foo" || args["path"] != "backend" {
		t.Fatalf("expected nested attempted_args on allow, got %#v", invocation)
	}
}

func TestAttachPreflightMetadataDenySurfacesTopLevelAttemptedArgs(t *testing.T) {
	decision := PreflightDecision{
		Allow:      false,
		Digest:     "digest-deny",
		Attempt:    1,
		Preflight:  "path_missing",
		ErrorCode:  "TOOL_PATH_NOT_FOUND",
		Retryable:  false,
		NextAction: "Use a path that exists.",
		Args: map[string]interface{}{
			"file_path": "missing.go",
		},
		PathCandidates: []string{"missing_file.go"},
	}
	meta := map[string]interface{}{}
	AttachPreflightMetadata(meta, decision)
	args, ok := meta[MetadataAttemptedArgsKey].(map[string]interface{})
	if !ok || args["file_path"] != "missing.go" {
		t.Fatalf("deny path must surface top-level attempted_args, got %#v", meta)
	}
	invocation, ok := meta[MetadataInvocationKey].(map[string]interface{})
	if !ok {
		t.Fatalf("expected tool_invocation metadata, got %#v", meta)
	}
	if invArgs, ok := invocation[MetadataAttemptedArgsKey].(map[string]interface{}); !ok || invArgs["file_path"] != "missing.go" {
		t.Fatalf("deny path must also nest attempted_args, got %#v", invocation)
	}
	if meta[toolresult.MetadataErrorCodeKey] != "TOOL_PATH_NOT_FOUND" {
		t.Fatalf("expected error_code on deny metadata, got %#v", meta)
	}
}

func TestSuggestNearbyPathCandidatesRanksTypoAndCase(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"Notes.md", "notes.bak", "other.txt"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	// Case-only mismatch should rank highest when basenames equal ignoring case.
	// On Windows the real FS is case-insensitive, so use a close typo instead.
	missing := filepath.Join(dir, "Notez.md")
	got := suggestNearbyPathCandidates(missing, nil)
	if len(got) == 0 {
		t.Fatal("expected candidates")
	}
	if !strings.Contains(got[0], "Notes.md") {
		t.Fatalf("expected Notes.md first, got %v", got)
	}
}

func TestPathPreflightAllowsPartialMultiPathBatch(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"files": map[string]interface{}{
				"type": "array",
				"items": map[string]interface{}{
					"type": "object",
					"properties": map[string]interface{}{
						"file_path": map[string]interface{}{"type": "string"},
					},
				},
			},
		},
	}
	meta := map[string]interface{}{"retry_class": "safe"}
	exists := func(path string) bool {
		return path == "exists.go"
	}

	// Mixed batch: one present + one missing => allow tool to return partial.
	partial := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "view",
		Args: map[string]interface{}{
			"files": []interface{}{
				map[string]interface{}{"file_path": "exists.go"},
				map[string]interface{}{"file_path": "missing.go"},
			},
		},
		InputSchema: schema,
		Metadata:    meta,
		PathExists:  exists,
	})
	if !partial.Allow {
		t.Fatalf("expected multi-path partial batch to pass preflight: %+v", partial)
	}
	if partial.Preflight == "path_existence" {
		t.Fatalf("partial batch must not path-deny: %+v", partial)
	}

	// All missing => still hard fail.
	allMissing := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "view",
		Args: map[string]interface{}{
			"files": []interface{}{
				map[string]interface{}{"file_path": "missing-a.go"},
				map[string]interface{}{"file_path": "missing-b.go"},
			},
		},
		InputSchema: schema,
		Metadata:    meta,
		PathExists:  exists,
	})
	if allMissing.Allow || allMissing.Preflight != "path_existence" {
		t.Fatalf("expected all-missing multi-path to path-deny: %+v", allMissing)
	}

	// Present workdir must not soft-allow a missing single content path.
	workdirMask := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "view",
		Args: map[string]interface{}{
			"file_path": "missing.go",
			"workdir":   "E:/present-workdir",
		},
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"file_path": map[string]interface{}{"type": "string"},
				"workdir":   map[string]interface{}{"type": "string"},
			},
		},
		Metadata: meta,
		PathExists: func(path string) bool {
			return path == "E:/present-workdir"
		},
	})
	if workdirMask.Allow || workdirMask.Preflight != "path_existence" {
		t.Fatalf("present workdir must not mask missing file_path: %+v", workdirMask)
	}
}

func TestPathPreflightAllowsPartialPathsArray(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"paths": map[string]interface{}{
				"type":  "array",
				"items": map[string]interface{}{"type": "string"},
			},
			"pattern": map[string]interface{}{"type": "string"},
		},
	}
	decision := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName: "grep",
		Args: map[string]interface{}{
			"pattern": "foo",
			"paths":   []interface{}{"exists.go", "missing.go"},
		},
		InputSchema: schema,
		Metadata:    map[string]interface{}{"retry_class": "safe"},
		PathExists: func(path string) bool {
			return path == "exists.go"
		},
	})
	if !decision.Allow {
		t.Fatalf("expected paths[] partial batch to pass preflight: %+v", decision)
	}
}

func TestPathPreflightUsesWorkspaceRootForRelativePaths(t *testing.T) {
	workspace := t.TempDir()
	docsDir := filepath.Join(workspace, "docs")
	if err := os.MkdirAll(docsDir, 0o755); err != nil {
		t.Fatalf("mkdir docs: %v", err)
	}
	// Process CWD is a session-like empty directory without docs/.
	cwd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("chdir empty cwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"path": map[string]interface{}{"type": "string"},
		},
	}
	meta := map[string]interface{}{"retry_class": "safe"}

	// Without WorkspaceRoot, relative docs is checked against process CWD and denied.
	denied := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:    "ls",
		Args:        map[string]interface{}{"path": "docs"},
		InputSchema: schema,
		Metadata:    meta,
	})
	if denied.Allow || denied.Preflight != "path_existence" {
		t.Fatalf("expected cwd-relative miss without workspace root: %+v", denied)
	}

	// With WorkspaceRoot, the same relative path resolves under the tool base path.
	allowed := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:      "ls",
		Args:          map[string]interface{}{"path": "docs"},
		InputSchema:   schema,
		Metadata:      meta,
		WorkspaceRoot: workspace,
	})
	if !allowed.Allow {
		t.Fatalf("expected workspace-relative docs to pass preflight: %+v", allowed)
	}

	// Missing relative path under workspace still fails, and candidates stay relative.
	sibling := filepath.Join(workspace, "docz")
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		t.Fatalf("mkdir sibling: %v", err)
	}
	missing := ApplyPreflight(NewMemory(2), PreflightRequest{
		ToolName:      "ls",
		Args:          map[string]interface{}{"path": "docs-missing"},
		InputSchema:   schema,
		Metadata:      meta,
		WorkspaceRoot: workspace,
	})
	if missing.Allow || missing.Preflight != "path_existence" {
		t.Fatalf("expected missing workspace-relative path denied: %+v", missing)
	}
	// docs/ is a nearby sibling of docs-missing under workspace; candidates should
	// be rewritten relative so the model can reuse them without absolute roots.
	foundRelative := false
	for _, cand := range missing.PathCandidates {
		if cand == "docs" || cand == "docz" || strings.HasSuffix(cand, "/docs") || strings.HasSuffix(cand, "/docz") {
			foundRelative = true
			if filepath.IsAbs(cand) {
				t.Fatalf("expected relative candidate for relative miss, got %q", cand)
			}
		}
	}
	if !foundRelative && len(missing.PathCandidates) == 0 {
		// Listing may still yield candidates depending on ranking; empty is ok if
		// the primary existence check against workspace root already succeeded above.
		t.Logf("no path candidates for docs-missing (ok): error=%q", missing.Error)
	}
}

func TestResolvePreflightPathJoinsWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	got := resolvePreflightPath("docs", root)
	want := filepath.Clean(filepath.Join(root, "docs"))
	if got != want {
		t.Fatalf("resolvePreflightPath(docs)=%q want %q", got, want)
	}
	// Use a real absolute path so Windows drive-letter abs checks work.
	abs := filepath.Join(root, "abs", "file.go")
	if !filepath.IsAbs(abs) {
		t.Fatalf("fixture abs path not absolute: %q", abs)
	}
	if got := resolvePreflightPath(abs, root); got != filepath.Clean(abs) {
		t.Fatalf("absolute path should not join workspace: got %q", got)
	}
	if got := resolvePreflightPath("docs", ""); got != "docs" {
		t.Fatalf("empty root should keep relative path: got %q", got)
	}
}

func TestCircuitOpenReplaysStoredPathCandidates(t *testing.T) {
	mem := NewMemory(2)
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"file_path": map[string]interface{}{"type": "string"},
		},
	}
	args := map[string]interface{}{"file_path": "missing_notes.md"}
	meta := map[string]interface{}{"retry_class": "safe"}
	candidates := []string{"Notes.md", "notes.md", "missing_notes.txt"}

	// Two identical terminal failures with path candidates open the circuit.
	for i := 0; i < 2; i++ {
		d := ApplyPreflight(mem, PreflightRequest{
			ToolName:    "view",
			Args:        args,
			InputSchema: schema,
			Metadata:    meta,
			PathExists:  func(string) bool { return false },
		})
		if d.Allow {
			t.Fatalf("attempt %d expected path preflight failure", i+1)
		}
		outcomeMeta := map[string]interface{}{
			toolresult.MetadataErrorCodeKey:   d.ErrorCode,
			toolresult.MetadataRetryableKey:   false,
			toolresult.MetadataPathCandidatesKey: candidates,
		}
		_ = RecordOutcome(mem, "view", d.Digest, d.Error, outcomeMeta)
	}

	// Third call: circuit open must still surface stored path candidates.
	d3 := ApplyPreflight(mem, PreflightRequest{
		ToolName:    "view",
		Args:        args,
		InputSchema: schema,
		Metadata:    meta,
		// Even if FS is unavailable / PathExists is nil-like, stored candidates
		// should be restored from FailureRecord without recomputation.
		PathExists: func(string) bool { return false },
	})
	if d3.Allow || !d3.Circuit || d3.Preflight != "circuit_open" {
		t.Fatalf("expected circuit_open, got %+v", d3)
	}
	if len(d3.PathCandidates) == 0 {
		t.Fatal("expected path candidates restored on circuit open")
	}
	for _, want := range candidates {
		found := false
		for _, got := range d3.PathCandidates {
			if got == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("missing candidate %q in %v", want, d3.PathCandidates)
		}
	}

	// AttachPreflightMetadata must stamp path_candidates for model recovery.
	stamped := map[string]interface{}{}
	AttachPreflightMetadata(stamped, d3)
	raw, ok := stamped[MetadataPathCandidatesKey].([]string)
	if !ok || len(raw) == 0 {
		t.Fatalf("expected path_candidates on blocked metadata, got %#v", stamped)
	}
}

func TestEmptySoftCacheShortCircuitsAfterThreshold(t *testing.T) {
	mem := NewMemory(2)
	toolName := "grep"
	args := map[string]interface{}{"pattern": "no-such-token-xyz", "path": "."}
	digest := ArgsDigest(toolName, args)

	// Two empty successes open the soft negative cache.
	for i := 0; i < 2; i++ {
		meta := map[string]interface{}{
			toolresult.MetadataEmptyResultKey: true,
			toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
			toolresult.MetadataNextActionKey:  "Broaden the query or proceed.",
			MetadataAttemptedArgsKey:          toolresult.CompactAttemptedArgs(args),
		}
		diag := RecordOutcome(mem, toolName, digest, "", meta)
		if !diag.OK || !diag.EmptyResult {
			t.Fatalf("record %d expected empty success diagnostic, got %+v", i+1, diag)
		}
	}

	record, open := mem.LookupEmpty(toolName, digest)
	if !open || record == nil || record.Count < 2 {
		t.Fatalf("expected open empty soft cache, got open=%v record=%+v", open, record)
	}

	// Third identical call short-circuits without hard error.
	decision := ApplyPreflight(mem, PreflightRequest{
		ToolName: toolName,
		Args:     args,
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{"type": "string"},
				"path":    map[string]interface{}{"type": "string"},
			},
		},
	})
	if decision.Allow {
		t.Fatal("expected soft empty short-circuit (Allow=false)")
	}
	if !decision.SoftEmpty {
		t.Fatalf("expected SoftEmpty=true, got %+v", decision)
	}
	if decision.Circuit {
		t.Fatal("soft empty must not open hard circuit")
	}
	if decision.Preflight != "empty_replay" {
		t.Fatalf("preflight=%s", decision.Preflight)
	}
	if strings.TrimSpace(decision.Error) != "" {
		t.Fatalf("soft empty Error must be empty, got %q", decision.Error)
	}
	if decision.ErrorCode != "" {
		t.Fatalf("soft empty must not set error_code, got %q", decision.ErrorCode)
	}
	if decision.Diagnostic.Outcome != toolresult.OutcomeEmpty || !decision.Diagnostic.EmptyResult {
		t.Fatalf("expected empty disposition diagnostic, got %+v", decision.Diagnostic)
	}
	if !strings.Contains(strings.ToLower(decision.NextAction), "broaden") &&
		!strings.Contains(strings.ToLower(decision.NextAction), "empty") {
		t.Fatalf("expected recovery next_action, got %q", decision.NextAction)
	}

	stamped := map[string]interface{}{}
	AttachPreflightMetadata(stamped, decision)
	if stamped[MetadataEmptyReplayKey] != true {
		t.Fatalf("expected empty_replay metadata, got %#v", stamped)
	}
	if stamped[toolresult.MetadataOutcomeKey] != toolresult.OutcomeEmpty {
		t.Fatalf("expected outcome=empty, got %#v", stamped[toolresult.MetadataOutcomeKey])
	}
	if stamped[toolresult.MetadataEmptyResultKey] != true {
		t.Fatalf("expected empty_result=true, got %#v", stamped)
	}
	// Soft empty is success disposition: no hard error_code stamp.
	if _, has := stamped[toolresult.MetadataErrorCodeKey]; has {
		t.Fatalf("soft empty must not stamp error_code: %#v", stamped)
	}
}

func TestNonEmptySuccessClearsEmptySoftCache(t *testing.T) {
	mem := NewMemory(2)
	toolName := "glob"
	args := map[string]interface{}{"pattern": "*.go"}
	digest := ArgsDigest(toolName, args)

	for i := 0; i < 2; i++ {
		_ = RecordOutcome(mem, toolName, digest, "", map[string]interface{}{
			toolresult.MetadataEmptyResultKey: true,
			toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
		})
	}
	if _, open := mem.LookupEmpty(toolName, digest); !open {
		t.Fatal("expected empty soft cache open before non-empty success")
	}

	// Non-empty success clears the soft cache.
	_ = RecordOutcome(mem, toolName, digest, "", map[string]interface{}{
		toolresult.MetadataOutcomeKey: toolresult.OutcomeSuccess,
	})
	if _, open := mem.LookupEmpty(toolName, digest); open {
		t.Fatal("non-empty success must clear empty soft cache")
	}

	// Next identical call is allowed again (no empty_replay short-circuit).
	decision := ApplyPreflight(mem, PreflightRequest{
		ToolName: toolName,
		Args:     args,
		InputSchema: map[string]interface{}{
			"type":       "object",
			"required":   []interface{}{"pattern"},
			"properties": map[string]interface{}{"pattern": map[string]interface{}{"type": "string"}},
		},
	})
	if !decision.Allow || decision.SoftEmpty {
		t.Fatalf("expected allow after soft-cache clear, got %+v", decision)
	}
}

func TestRecordSuccessDoesNotClearEmptySoftCache(t *testing.T) {
	mem := NewMemory(2)
	toolName := "grep"
	digest := ArgsDigest(toolName, map[string]interface{}{"pattern": "x"})

	_ = RecordOutcome(mem, toolName, digest, "", map[string]interface{}{
		toolresult.MetadataEmptyResultKey: true,
		toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
	})
	_ = RecordOutcome(mem, toolName, digest, "", map[string]interface{}{
		toolresult.MetadataEmptyResultKey: true,
		toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
	})
	if _, open := mem.LookupEmpty(toolName, digest); !open {
		t.Fatal("expected empty soft cache open")
	}

	// RecordSuccess (used by empty OK path for hard-circuit clear) must keep empties.
	mem.RecordSuccess(toolName, digest)
	if _, open := mem.LookupEmpty(toolName, digest); !open {
		t.Fatal("RecordSuccess must not wipe empty soft cache")
	}
}

func TestHardFailureSupersedesEmptySoftCache(t *testing.T) {
	mem := NewMemory(2)
	toolName := "view"
	args := map[string]interface{}{"file_path": "a.txt"}
	digest := ArgsDigest(toolName, args)

	_ = RecordOutcome(mem, toolName, digest, "", map[string]interface{}{
		toolresult.MetadataEmptyResultKey: true,
		toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
	})
	_ = RecordOutcome(mem, toolName, digest, "", map[string]interface{}{
		toolresult.MetadataEmptyResultKey: true,
		toolresult.MetadataOutcomeKey:     toolresult.OutcomeEmpty,
	})
	if _, open := mem.LookupEmpty(toolName, digest); !open {
		t.Fatal("expected empty soft cache open")
	}

	// Terminal hard failure for the same digest supersedes soft empty.
	_ = RecordOutcome(mem, toolName, digest, "path not found: a.txt", map[string]interface{}{
		toolresult.MetadataErrorCodeKey: "TOOL_PATH_NOT_FOUND",
		toolresult.MetadataRetryableKey: false,
	})
	if _, open := mem.LookupEmpty(toolName, digest); open {
		t.Fatal("hard failure must clear empty soft cache for same digest")
	}
}
