package toolbroker

// Tool naming contract (tool-surface-inventory-and-naming-review.md §6).
//
// Every advertised broker tool name must either match the domain-prefix shape
// `<domain>_<verb>[_<object>]` with a closed-set domain, or belong to the core
// primitive allowlist (§4.6). The contract exists so future tools are rejected
// by test instead of letting naming drift again (N1–N5).

import (
	"regexp"
	"strings"
	"testing"
)

// namingContractDomains is the closed domain set from §3.1.
var namingContractDomains = map[string]bool{
	"job":      true,
	"schedule": true,
	"team":     true,
	"subagent": true,
	"plan":     true,
}

// namingContractVerbs is the closed verb set from §3.1 rule 4, plus the
// noun-reading objects §3.1 rule 3 allows for pure reads (job_output,
// team_mailbox, subagent_events).
var namingContractVerbs = map[string]bool{
	"start": true, "read": true, "list": true, "search": true,
	"create": true, "spawn": true, "update": true, "delete": true,
	"cancel": true, "wait": true, "send": true, "input": true,
	"monitor": true, "control": true, "close": true, "resume": true,
	"approve": true, "apply": true, "discard": true, "report": true,
	"block": true, "inspect": true, "ack": true, "enter": true, "exit": true,
	"review": true, "followup": true, "events": true, "mailbox": true,
	"output": true,
}

// namingContractCorePrimitives is the no-prefix allowlist from §4.6: high-
// frequency generic tools stay unprefixed on purpose.
var namingContractCorePrimitives = map[string]bool{
	"ask_user_question": true,
	"todos":             true,
}

var namingContractDomainToolShape = regexp.MustCompile(`^([a-z]+)_([a-z0-9_]+)$`)

// TestAdvertisedToolNamesFollowNamingContract walks every advertised broker
// definition and enforces the naming contract.
func TestAdvertisedToolNamesFollowNamingContract(t *testing.T) {
	broker := &Broker{}
	for _, def := range broker.Definitions() {
		name := def.Name
		if name == "" {
			t.Fatal("advertised tool definition with empty name")
		}
		if len(name) > 64 {
			t.Fatalf("tool name %q exceeds 64 characters", name)
		}
		if strings.ToLower(name) != name {
			t.Fatalf("tool name %q must be lowercase", name)
		}
		if strings.Contains(name, "--") || strings.Contains(name, "__") {
			t.Fatalf("tool name %q must not contain consecutive separators", name)
		}
		if namingContractCorePrimitives[name] {
			continue
		}
		match := namingContractDomainToolShape.FindStringSubmatch(name)
		if match == nil {
			t.Fatalf("tool name %q must match <domain>_<verb>[_<object>] or be a core primitive", name)
		}
		domain, rest := match[1], match[2]
		if !namingContractDomains[domain] {
			t.Fatalf("tool name %q uses domain %q outside the closed set {job,schedule,team,subagent,plan}", name, domain)
		}
		verb := strings.SplitN(rest, "_", 2)[0]
		if !namingContractVerbs[verb] {
			t.Fatalf("tool name %q uses verb %q outside the closed verb set (extend the set deliberately if a new verb is required)", name, verb)
		}
	}
}

// TestLegacyToolAliasesStayCallable pins the migration invariant: every legacy
// spelling renamed in B1–B4 must still normalize onto its canonical constant,
// while the advertised surface carries only the canonical name.
func TestLegacyToolAliasesStayCallable(t *testing.T) {
	legacyAliases := map[string]string{
		// B1 job family.
		"background_task": ToolJobStart,
		"task_output":     ToolJobOutput,
		"task_kill":       ToolJobCancel,
		"task_monitor":    ToolJobMonitor,
		"task_control":    ToolJobControl,
		"write_stdin":     ToolJobInput,
		"task_input":      ToolJobInput,
		// B2 team family.
		"spawn_team":          ToolTeamSpawn,
		"wait_team":           ToolTeamWait,
		"send_team_message":   ToolTeamSend,
		"read_mailbox_digest": ToolTeamMailbox,
		"read_task_spec":      ToolTeamTaskSpec,
		"read_task_context":   ToolTeamTaskContext,
		"report_task_outcome": ToolTeamTaskReport,
		"block_current_task":  ToolTeamTaskBlock,
		// B3 subagent family.
		"spawn_agent":            ToolSubagentSpawn,
		"list_agents":            ToolSubagentList,
		"send_message":           ToolSubagentSend,
		"followup_task":          ToolSubagentFollowup,
		"send_input":             ToolSubagentInput,
		"resolve_agent_approval": ToolSubagentApprove,
		"wait_agent":             ToolSubagentWait,
		"read_agent_events":      ToolSubagentEvents,
		"close_agent":            ToolSubagentClose,
		"resume_agent":           ToolSubagentResume,
		"apply_agent_worktree":   ToolSubagentApplyWorktree,
		"discard_agent_worktree": ToolSubagentDiscardWorktree,
		// B4 plan family.
		"enter_plan_mode": ToolPlanEnter,
		"exit_plan_mode":  ToolPlanExit,
	}
	for legacy, canonical := range legacyAliases {
		if got := normalizeToolName(legacy); got != canonical {
			t.Fatalf("legacy alias %q must normalize to %q, got %q", legacy, canonical, got)
		}
	}
}

// TestAdvertisedSurfaceCarriesOnlyCanonicalNames pins "one capability, one
// advertised name": no legacy alias may appear in Definitions().
func TestAdvertisedSurfaceCarriesOnlyCanonicalNames(t *testing.T) {
	broker := &Broker{}
	legacyNames := []string{
		"background_task", "task_output", "task_kill", "task_monitor", "task_control", "write_stdin", "task_input",
		"spawn_team", "wait_team", "send_team_message", "read_mailbox_digest",
		"read_task_spec", "read_task_context", "report_task_outcome", "block_current_task",
		"spawn_agent", "list_agents", "send_message", "followup_task", "send_input",
		"resolve_agent_approval", "wait_agent", "read_agent_events", "close_agent", "resume_agent",
		"apply_agent_worktree", "discard_agent_worktree",
		"enter_plan_mode", "exit_plan_mode",
	}
	legacy := make(map[string]bool, len(legacyNames))
	for _, name := range legacyNames {
		legacy[name] = true
	}
	for _, def := range broker.Definitions() {
		if legacy[def.Name] {
			t.Fatalf("legacy name %q must not be advertised; advertise the canonical job_*/team_*/subagent_*/plan_* spelling and keep the legacy spelling alias-only", def.Name)
		}
	}
}

// TestCanonicalFamilyRoundTrip asserts every canonical constant is its own
// fixed point in normalizeToolName (no accidental shadowing by a later case).
func TestCanonicalFamilyRoundTrip(t *testing.T) {
	canonical := []string{
		ToolJobStart, ToolJobOutput, ToolJobCancel, ToolJobMonitor, ToolJobControl, ToolJobInput, ToolJobList, ToolJobSearch,
		ToolScheduleCreate, ToolScheduleList, ToolScheduleUpdate, ToolScheduleDelete,
		ToolTeamSpawn, ToolTeamWait, ToolTeamSend, ToolTeamMailbox, ToolTeamTaskSpec, ToolTeamTaskContext, ToolTeamTaskReport, ToolTeamTaskBlock,
		ToolSubagentSpawn, ToolSubagentList, ToolSubagentSend, ToolSubagentFollowup, ToolSubagentInput, ToolSubagentApprove,
		ToolSubagentWait, ToolSubagentEvents, ToolSubagentClose, ToolSubagentResume, ToolSubagentApplyWorktree, ToolSubagentDiscardWorktree,
		ToolPlanEnter, ToolPlanExit, ToolPlanReview,
	}
	for _, name := range canonical {
		if got := normalizeToolName(name); got != name {
			t.Fatalf("canonical name %q must be a normalizeToolName fixed point, got %q", name, got)
		}
	}
}

// TestIsBrokerToolCoversEveryAdvertisedName pins the schema-vs-execution
// contract: every name advertised through Definitions() must also be classified
// as a broker tool by IsBrokerTool. A missing case makes the loop skip the
// broker path and fall into the MCP catalog lookup, so the advertised tool
// fails at execution with "tool not found" despite being in the model schema
// (regression found live: schedule_* were advertised but not classified after
// the B1 rename).
func TestIsBrokerToolCoversEveryAdvertisedName(t *testing.T) {
	broker := &Broker{}
	for _, def := range broker.Definitions() {
		if !broker.IsBrokerTool(def.Name) {
			t.Fatalf("advertised tool %q is not classified by IsBrokerTool; the model schema offers it but execution would fail with tool-not-found", def.Name)
		}
	}
}
