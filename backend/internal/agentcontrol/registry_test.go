package agentcontrol

import "testing"

type testContext map[string]interface{}

func (c testContext) GetContext(key string) (interface{}, bool) {
	value, ok := c[key]
	return value, ok
}

func (c testContext) SetContext(key string, value interface{}) {
	c[key] = value
}

func TestRegistryProjectionHelpers(t *testing.T) {
	parent := testContext{
		SessionContextPath:  "/root/parent",
		SessionContextDepth: "2",
	}

	if got := ChildDepth(parent); got != 3 {
		t.Fatalf("unexpected child depth: %d", got)
	}
	if got := ChildPath(parent, "parent-session", "child session", true); got != "/root/parent/child-session" {
		t.Fatalf("unexpected child path: %s", got)
	}
	if got := TeamTeammatePath("team 1", "member.1", "Member One", "session-1"); got != "/root/teams/team-1/member-1" {
		t.Fatalf("unexpected teammate path: %s", got)
	}
}

func TestSetContextIfChanged(t *testing.T) {
	ctx := testContext{}
	if !SetContextIfChanged(ctx, SessionContextDepth, 1) {
		t.Fatal("expected first context write")
	}
	if SetContextIfChanged(ctx, SessionContextDepth, 1) {
		t.Fatal("expected equivalent numeric context to be ignored")
	}
	if !SetContextIfChanged(ctx, SessionContextPath, " /root/agent ") {
		t.Fatal("expected path context write")
	}
	if ctx[SessionContextPath] != "/root/agent" {
		t.Fatalf("expected trimmed path, got %#v", ctx[SessionContextPath])
	}
}

func TestTaskRecordNormalizeTrimsFields(t *testing.T) {
	record := TaskRecord{
		ID:           " task-1 ",
		Workflow:     " spawn_team ",
		TeamID:       " team-1 ",
		ParentTaskID: " parent ",
		Assignee:     " member-1 ",
		SessionID:    " session-1 ",
		Path:         " /root/teams/team-1/member-1 ",
		Title:        " Review docs ",
		Summary:      " Done ",
		Status:       " running ",
	}.Normalize()

	if record.ID != "task-1" ||
		record.Workflow != WorkflowSpawnTeam ||
		record.TeamID != "team-1" ||
		record.ParentTaskID != "parent" ||
		record.Assignee != "member-1" ||
		record.SessionID != "session-1" ||
		record.Path != "/root/teams/team-1/member-1" ||
		record.Title != "Review docs" ||
		record.Summary != "Done" ||
		record.Status != "running" {
		t.Fatalf("unexpected normalized task record: %#v", record)
	}
}

func TestTaskClaimRequestNormalizeTrimsFields(t *testing.T) {
	request := TaskClaimRequest{
		ID:            " task-1 ",
		Workflow:      " spawn_team ",
		TeamID:        " team-1 ",
		Assignee:      " member-1 ",
		WorkspaceRoot: " workspace ",
	}.Normalize()

	if request.ID != "task-1" ||
		request.Workflow != WorkflowSpawnTeam ||
		request.TeamID != "team-1" ||
		request.Assignee != "member-1" ||
		request.WorkspaceRoot != "workspace" {
		t.Fatalf("unexpected normalized task claim request: %#v", request)
	}
}

func TestTaskTerminalUpdateRequestNormalizeTrimsFields(t *testing.T) {
	request := TaskTerminalUpdateRequest{
		ID:         " task-1 ",
		Workflow:   " spawn_team ",
		TeamID:     " team-1 ",
		Status:     " done ",
		Summary:    " Finished ",
		TeammateID: " member-1 ",
	}.Normalize()

	if request.ID != "task-1" ||
		request.Workflow != WorkflowSpawnTeam ||
		request.TeamID != "team-1" ||
		request.Status != "done" ||
		request.Summary != "Finished" ||
		request.TeammateID != "member-1" {
		t.Fatalf("unexpected normalized terminal task request: %#v", request)
	}
}

func TestTaskBlockRequestNormalizeTrimsFields(t *testing.T) {
	request := TaskBlockRequest{
		ID:         " task-1 ",
		Workflow:   " spawn_team ",
		Summary:    " Waiting ",
		TeammateID: " member-1 ",
	}.Normalize()

	if request.ID != "task-1" ||
		request.Workflow != WorkflowSpawnTeam ||
		request.Summary != "Waiting" ||
		request.TeammateID != "member-1" {
		t.Fatalf("unexpected normalized task block request: %#v", request)
	}
}
