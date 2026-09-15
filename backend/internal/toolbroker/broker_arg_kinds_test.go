package toolbroker

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestValidateBrokerToolArgKindsRejectsMismatchedKinds pins the fail-closed
// behaviour for the keys whose default is the unsafe direction: a wrong JSON
// kind must fail the call instead of silently substituting that default.
func TestValidateBrokerToolArgKindsRejectsMismatchedKinds(t *testing.T) {
	cases := []struct {
		name     string
		tool     string
		key      string
		value    interface{}
		wantKind string
	}{
		{"apply keep as string", ToolApplyAgentWorktree, "keep", "true", toolArgFieldBool},
		{"resolve allow as string", ToolResolveAgentApproval, "allow", "true", toolArgFieldBool},
		{"digest mark_read as string", ToolReadMailboxDigest, "mark_read", "true", toolArgFieldBool},
		{"task context mark_read as string", ToolReadTaskContext, "mark_read", "1", toolArgFieldBool},
		{"list include_closed as string", ToolListAgents, "include_closed", "true", toolArgFieldBool},
		{"snapshot include_resolved as string", ToolSupervisionSnapshot, "include_resolved", "1", toolArgFieldBool},
		{"outcome notify_lead as string", ToolReportTaskOutcome, "notify_lead", "true", toolArgFieldBool},
		{"block auto_replan as number", ToolBlockCurrentTask, "auto_replan", 1, toolArgFieldBool},
		{"wait team require_summary as string", ToolWaitTeam, "require_summary", "yes", toolArgFieldBool},
		{"ask required as string", ToolAskUserQuestion, "required", "false", toolArgFieldBool},
		{"wait agent id as number", ToolWaitAgent, "id", 42, toolArgFieldString},
		{"read events session_id as bool", ToolReadAgentEvents, "session_id", true, toolArgFieldString},
		{"task_output job_id as number", ToolTaskOutput, "job_id", 7, toolArgFieldString},
		{"wait_team team_id as number", ToolWaitTeam, "team_id", 3, toolArgFieldString},
		{"resolve request_id as number", ToolResolveAgentApproval, "request_id", 9, toolArgFieldString},
		{"control notification_id as number", ToolControlDescendant, "notification_id", 1, toolArgFieldString},
		{"ack decision as number", ToolAckLifecycle, "decision", 1, toolArgFieldString},
		{"patched_args as stringified object", ToolResolveAgentApproval, "patched_args", `{"command":"rm -rf /"}`, toolArgFieldObject},
		{"startup_acceptance as stringified object", ToolBackgroundTask, "startup_acceptance", `{"probe":"process"}`, toolArgFieldObject},
		{"send_team_message metadata as string", ToolSendTeamMessage, "metadata", "kind=note", toolArgFieldObject},
		{"ids with non-string item", ToolWaitAgent, "ids", []interface{}{"child-1", 2}, toolArgFieldStringOrList},
		{"paths as number", ToolApplyAgentWorktree, "paths", 3, toolArgFieldStringOrList},
		{"suggestions with non-string item", ToolAskUserQuestion, "suggestions", []interface{}{1}, toolArgFieldStringOrList},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBrokerToolArgKinds(tc.tool, map[string]interface{}{tc.key: tc.value})
			if err == nil {
				t.Fatalf("%s %s=%v must fail closed", tc.tool, tc.key, tc.value)
			}
			for _, want := range []string{normalizeToolName(tc.tool), `"` + tc.key + `"`, tc.wantKind} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q must mention %q", err.Error(), want)
				}
			}
		})
	}
}

// TestValidateBrokerToolArgKindsRejectsNestedStartupAcceptanceKinds covers the
// nested object: a wrong kind on the probe/address/url keys used to launch the
// job without any startup acceptance gate.
func TestValidateBrokerToolArgKindsRejectsNestedStartupAcceptanceKinds(t *testing.T) {
	cases := []struct {
		name    string
		startup map[string]interface{}
		wantKey string
	}{
		{"probe as number", map[string]interface{}{"probe": 1}, "probe"},
		{"address as bool", map[string]interface{}{"address": true}, "address"},
		{"url as number", map[string]interface{}{"url": 8080}, "url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBrokerToolArgKinds(ToolBackgroundTask, map[string]interface{}{
				"command":            "echo hi",
				"startup_acceptance": tc.startup,
			})
			if err == nil {
				t.Fatalf("startup_acceptance %s must fail closed", tc.name)
			}
			for _, want := range []string{"startup_acceptance", `"` + tc.wantKey + `"`, toolArgFieldString} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q must mention %q", err.Error(), want)
				}
			}
		})
	}
}

func TestValidateBrokerToolArgKindsAcceptsDocumentedKinds(t *testing.T) {
	cases := []struct {
		name string
		tool string
		args map[string]interface{}
	}{
		{"apply keep true", ToolApplyAgentWorktree, map[string]interface{}{"id": "child", "keep": true}},
		{"apply keep false", ToolApplyAgentWorktree, map[string]interface{}{"id": "child", "keep": false}},
		{"resolve allow false", ToolResolveAgentApproval, map[string]interface{}{"allow": false, "request_id": "req-1"}},
		{"list include_closed", ToolListAgents, map[string]interface{}{"include_closed": true, "path_prefix": "/root"}},
		{"snapshot include_resolved", ToolSupervisionSnapshot, map[string]interface{}{"include_resolved": true}},
		{"wait agent id", ToolWaitAgent, map[string]interface{}{"id": "child", "timeout_ms": 1500}},
		{"read events view", ToolReadAgentEvents, map[string]interface{}{"id": "child", "view": "tool_progress"}},
		{"task context flags", ToolReadTaskContext, map[string]interface{}{"include_mailbox": true, "include_dependencies": false}},
		{"ack until duration", ToolAckLifecycle, map[string]interface{}{"notification_id": "n-1", "decision": "defer", "until": "30m"}},
		{"patched_args object", ToolResolveAgentApproval, map[string]interface{}{"allow": true, "request_id": "req-1", "patched_args": map[string]interface{}{"command": "echo hi"}}},
		{"startup_acceptance object", ToolBackgroundTask, map[string]interface{}{"command": "echo hi", "startup_acceptance": map[string]interface{}{"probe": "process", "timeout_ms": 1500}}},
		{"ids array", ToolWaitAgent, map[string]interface{}{"ids": []interface{}{"child-1", "child-2"}}},
		{"session_ids string", ToolWaitAgent, map[string]interface{}{"session_ids": "/root/worker"}},
		{"paths string slice", ToolApplyAgentWorktree, map[string]interface{}{"paths": []string{"a.go"}}},
		{"suggestions array", ToolAskUserQuestion, map[string]interface{}{"prompt": "which?", "suggestions": []interface{}{"a", "b"}}},
		{"metadata object", ToolSendTeamMessage, map[string]interface{}{"body": "hi", "metadata": map[string]interface{}{"kind": "note"}}},
		{"explicit null stays lenient", ToolTaskOutput, map[string]interface{}{"job_id": nil}},
		{"empty args stay lenient", ToolReadMailboxDigest, map[string]interface{}{}},
		{"unknown key stays advisory", ToolReadMailboxDigest, map[string]interface{}{"mark_read": true, "extra": 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateBrokerToolArgKinds(tc.tool, tc.args); err != nil {
				t.Fatalf("documented kinds must pass, got %v", err)
			}
		})
	}
}

// TestBrokerToolArgKindsStayWithinDeclaredToolKeys keeps the kind table and the
// consumed-key audit table from drifting apart, and documents that the tools
// with dedicated validators own their keys there.
func TestBrokerToolArgKindsStayWithinDeclaredToolKeys(t *testing.T) {
	for tool, kinds := range brokerToolArgKinds {
		declared, ok := brokerToolArgKeys[tool]
		if !ok {
			t.Fatalf("tool %s has a kind table but no declared argument keys", tool)
		}
		declaredSet := make(map[string]struct{}, len(declared))
		for _, key := range declared {
			declaredSet[key] = struct{}{}
		}
		for key := range kinds {
			if _, ok := declaredSet[key]; !ok {
				t.Fatalf("tool %s validates %q but the audit table does not declare it", tool, key)
			}
		}
	}
	for _, tool := range []string{ToolSpawnAgent, ToolSpawnTeam, ToolSendInput} {
		if _, ok := brokerToolArgKinds[tool]; ok {
			t.Fatalf("%s kinds must stay in its dedicated validator", tool)
		}
	}
}

// TestExecuteValidatesBrokerToolArgKindsBeforeDispatch proves the validator runs
// ahead of the tool dispatcher: a zero-value Broker (no services configured)
// still reports the kind mismatch instead of a "not configured" error.
func TestExecuteValidatesBrokerToolArgKindsBeforeDispatch(t *testing.T) {
	broker := &Broker{}
	_, _, err := broker.execute(context.Background(), "session-1", ToolApplyAgentWorktree,
		map[string]interface{}{"id": "child", "keep": "true"}, "")
	if err == nil {
		t.Fatal("keep=\"true\" must fail closed instead of deleting the worktree")
	}
	if !strings.Contains(err.Error(), "keep") || !strings.Contains(err.Error(), toolArgFieldBool) {
		t.Fatalf("unexpected error: %v", err)
	}

	_, _, err = broker.execute(context.Background(), "session-1", ToolWaitAgent,
		map[string]interface{}{"timeout_ms": "1500"}, "")
	if err == nil {
		t.Fatal("timeout_ms=\"1500\" must fail closed instead of waiting on the default budget")
	}
	if !strings.Contains(err.Error(), "timeout_ms") || !strings.Contains(err.Error(), toolArgFieldNumber) {
		t.Fatalf("unexpected error: %v", err)
	}

	_, _, err = broker.execute(context.Background(), "session-1", ToolSupervisionSnapshot,
		map[string]interface{}{"after_seq": "10"}, "")
	if err == nil {
		t.Fatal("after_seq=\"10\" must fail closed instead of re-reading from seq 0")
	}
	if !strings.Contains(err.Error(), "after_seq") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBrokerToolArgIntAcceptsEveryWholeNumberSpelling(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
		want  int
	}{
		{"int", 12, 12},
		{"int32", int32(1500), 1500},
		{"int64", int64(30000), 30000},
		{"uint", uint(5), 5},
		{"uint32", uint32(600), 600},
		{"float64 whole", float64(250), 250},
		{"float32 whole", float32(90), 90},
		{"json.Number", json.Number("4096"), 4096},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok, err := brokerToolArgInt("test_tool", map[string]interface{}{"limit": tc.value}, "limit")
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !ok || got != tc.want {
				t.Fatalf("got (%d, %v), want (%d, true)", got, ok, tc.want)
			}
		})
	}
}

func TestBrokerToolArgIntRejectsNonWholeNumbers(t *testing.T) {
	cases := []struct {
		name  string
		value interface{}
	}{
		{"stringified", "10"},
		{"fractional", 10.5},
		{"json.Number fractional", json.Number("10.5")},
		{"bool", true},
		{"nan", math.NaN()},
		{"positive infinity", math.Inf(1)},
		{"beyond int64", json.Number("9223372036854775808")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := brokerToolArgInt("test_tool", map[string]interface{}{"limit": tc.value}, "limit"); err == nil {
				t.Fatalf("limit=%v must be rejected", tc.value)
			}
		})
	}
	if _, ok, err := brokerToolArgInt("test_tool", map[string]interface{}{}, "limit"); err != nil || ok {
		t.Fatalf("absent key must read as (0,false,nil), got (%v,%v)", ok, err)
	}
	if _, ok, err := brokerToolArgInt("test_tool", map[string]interface{}{"limit": nil}, "limit"); err != nil || ok {
		t.Fatalf("null must read as (0,false,nil), got (%v,%v)", ok, err)
	}
}

func TestParseSupervisionSnapshotArgsReadsEveryNumberSpelling(t *testing.T) {
	parsed, err := parseSupervisionSnapshotArgs(map[string]interface{}{
		"after_seq":        json.Number("17"),
		"limit":            float64(5),
		"include_resolved": true,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.AfterSeq != 17 || parsed.Limit != 5 || !parsed.IncludeResolved {
		t.Fatalf("unexpected parse: %+v", parsed)
	}

	for _, key := range []string{"after_seq", "limit"} {
		if _, err := parseSupervisionSnapshotArgs(map[string]interface{}{key: "10"}); err == nil {
			t.Fatalf("%s=\"10\" must fail closed", key)
		}
	}

	// A non-positive limit keeps the compact default instead of an empty page.
	parsed, err = parseSupervisionSnapshotArgs(map[string]interface{}{"limit": 0})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if parsed.Limit != 0 {
		t.Fatalf("limit=0 must keep the default, got %d", parsed.Limit)
	}
}

func TestParseAckLifecycleReadsExpectedVersionAsJSONNumber(t *testing.T) {
	parsed, err := parseAckLifecycleArgs(map[string]interface{}{
		"notification_id":  "n-1",
		"decision":         "acknowledge",
		"note":             "handled",
		"expected_version": json.Number("3"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !parsed.HasExpectedVersion || parsed.ExpectedVersion != 3 {
		t.Fatalf("expected_version was dropped: %+v", parsed)
	}

	if _, err := parseAckLifecycleArgs(map[string]interface{}{
		"notification_id":  "n-1",
		"decision":         "acknowledge",
		"note":             "handled",
		"expected_version": "3",
	}); err == nil {
		t.Fatal("a stringified expected_version must fail closed instead of skipping the CAS guard")
	}
}
