package commands

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/wwsheng009/ai-agent-runtime/internal/acp"
	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

// The shared recordingACPEmitter (agent_stdio_bridge_test.go) captures
// session/update notifications; these helpers select from one of its snapshots.
func acpUpdatesOfKind(updates []acp.SessionUpdate, kind string) []acp.SessionUpdate {
	var out []acp.SessionUpdate
	for _, update := range updates {
		if update.SessionUpdate == kind {
			out = append(out, update)
		}
	}
	return out
}

func acpUpdateKinds(updates []acp.SessionUpdate) []string {
	out := make([]string, 0, len(updates))
	for _, update := range updates {
		out = append(out, update.SessionUpdate)
	}
	return out
}

func configOptionByID(t *testing.T, update acp.SessionUpdate, id string) acp.SessionConfigOption {
	t.Helper()
	for _, option := range update.ConfigOptions {
		if option.ID == id {
			return option
		}
	}
	t.Fatalf("option %q missing from %+v", id, update.ConfigOptions)
	return acp.SessionConfigOption{}
}

// TestBroadcastACPModeChangeEmitsBothChannels pins that an out-of-band mode
// change refreshes both advertised channels: config_option_update carries the
// authoritative currentValue for the "mode" config option, and
// current_mode_update drives the legacy modes selector. Clients that only read
// one of the two must not be left with a stale picker.
func TestBroadcastACPModeChangeEmitsBothChannels(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	setChatPermissionMode(chat, runtimepolicy.ModeAcceptEdits)

	rec := &recordingACPEmitter{}
	host.SetSessionEmitter(rec)
	host.broadcastACPModeChange("sess-1", chat)

	optionUpdates := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateConfigOptionUpdate)
	if len(optionUpdates) != 1 {
		t.Fatalf("config_option_update count = %d, want 1 (%v)", len(optionUpdates), acpUpdateKinds(rec.snapshot()))
	}
	modeOption := configOptionByID(t, optionUpdates[0], acpModeConfigOptionID)
	if modeOption.CurrentValue != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("mode option currentValue = %q, want %q", modeOption.CurrentValue, runtimepolicy.ModeAcceptEdits)
	}

	modeUpdates := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateCurrentModeUpdate)
	if len(modeUpdates) != 1 {
		t.Fatalf("current_mode_update count = %d, want 1 (%v)", len(modeUpdates), acpUpdateKinds(rec.snapshot()))
	}
	if modeUpdates[0].CurrentModeID != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("currentModeId = %q, want %q", modeUpdates[0].CurrentModeID, runtimepolicy.ModeAcceptEdits)
	}
}

// TestACPHostSetSessionModeEmitsModeChannel pins the legacy session/set_mode
// contract: the RPC response is the empty object ACP v1 types for the method,
// and the refreshed state reaches the client only through the two mode-channel
// notifications pushed right after the switch.
func TestACPHostSetSessionModeEmitsModeChannel(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	hostSess := host.sess["sess-1"]
	// Mid-turn is the interesting case: the in-flight variant must still notify
	// both channels even though it deliberately skips the durable snapshot sync.
	hostSess.mu.Lock()
	hostSess.prompting = true
	hostSess.mu.Unlock()

	rec := &recordingACPEmitter{}
	host.SetSessionEmitter(rec)

	resp, err := host.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionID: "sess-1",
		ModeID:    string(runtimepolicy.ModeBypassPermissions),
	})
	if err != nil {
		t.Fatalf("SetSessionMode: %v", err)
	}
	if chat.PermissionMode != runtimepolicy.ModeBypassPermissions {
		t.Fatalf("permission mode = %q, want %q", chat.PermissionMode, runtimepolicy.ModeBypassPermissions)
	}
	if len(acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateConfigOptionUpdate)) != 1 {
		t.Fatalf("config_option_update missing after set_mode: %v", acpUpdateKinds(rec.snapshot()))
	}
	modeUpdates := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateCurrentModeUpdate)
	if len(modeUpdates) != 1 || modeUpdates[0].CurrentModeID != string(runtimepolicy.ModeBypassPermissions) {
		t.Fatalf("current_mode_update = %+v, want %q", modeUpdates, runtimepolicy.ModeBypassPermissions)
	}
	// The response itself stays empty: modes are not carried by set_mode.
	encoded, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("SetSessionModeResponse = %s, want {}", encoded)
	}
}

// TestACPHostSetSessionModeRejectsUnknownMode pins that an unsupported mode id
// is an invalid-params error and emits nothing, so a client cannot end up with a
// selector showing a mode the session never applied.
func TestACPHostSetSessionModeRejectsUnknownMode(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	rec := &recordingACPEmitter{}
	host.SetSessionEmitter(rec)
	before := chat.PermissionMode

	if _, err := host.SetSessionMode(context.Background(), acp.SetSessionModeRequest{
		SessionID: "sess-1",
		ModeID:    "not-a-mode",
	}); err == nil {
		t.Fatal("unknown mode id must be rejected")
	}
	if chat.PermissionMode != before {
		t.Fatalf("permission mode changed from %q to %q", before, chat.PermissionMode)
	}
	if len(rec.snapshot()) != 0 {
		t.Fatalf("rejected switch must not notify: %v", acpUpdateKinds(rec.snapshot()))
	}
}

// TestACPConfigOptionCommandEmitsOnlyConfigOptionUpdate pins the slash-command
// emission path for a non-mode option: the change is pushed as a
// config_option_update, and the mode channel stays silent because the mode did
// not change.
func TestACPConfigOptionCommandEmitsOnlyConfigOptionUpdate(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1", "m2")
	hostSess := host.sess["sess-1"]
	rec := &recordingACPEmitter{}
	host.SetSessionEmitter(rec)

	host.emitACPConfigOptionCommand(context.Background(), "sess-1", hostSess, "model", "m2", rec)

	if chat.Model != "m2" {
		t.Fatalf("session model = %q, want m2", chat.Model)
	}
	optionUpdates := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateConfigOptionUpdate)
	if len(optionUpdates) != 1 {
		t.Fatalf("config_option_update count = %d, want 1 (%v)", len(optionUpdates), acpUpdateKinds(rec.snapshot()))
	}
	modelOption := configOptionByID(t, optionUpdates[0], acpModelConfigOptionID)
	if modelOption.CurrentValue != "m2" {
		t.Fatalf("model option currentValue = %q, want m2", modelOption.CurrentValue)
	}
	if got := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateCurrentModeUpdate); len(got) != 0 {
		t.Fatalf("model switch must not touch the mode channel: %+v", got)
	}
	if got := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateAgentMessageChunk); len(got) == 0 {
		t.Fatalf("command must confirm the switch to the client: %v", acpUpdateKinds(rec.snapshot()))
	}
}

// TestACPConfigOptionCommandModeEmitsBothChannels pins that "/mode <id>" routes
// through the mode-channel broadcast (both channels) instead of the generic
// config-option update, because the legacy selector needs the second
// notification to stay in sync.
func TestACPConfigOptionCommandModeEmitsBothChannels(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	hostSess := host.sess["sess-1"]
	rec := &recordingACPEmitter{}
	host.SetSessionEmitter(rec)

	host.emitACPConfigOptionCommand(context.Background(), "sess-1", hostSess, "mode", string(runtimepolicy.ModeAcceptEdits), rec)

	if chat.PermissionMode != runtimepolicy.ModeAcceptEdits {
		t.Fatalf("permission mode = %q, want %q", chat.PermissionMode, runtimepolicy.ModeAcceptEdits)
	}
	optionUpdates := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateConfigOptionUpdate)
	if len(optionUpdates) != 1 {
		t.Fatalf("config_option_update count = %d, want 1 (%v)", len(optionUpdates), acpUpdateKinds(rec.snapshot()))
	}
	if got := configOptionByID(t, optionUpdates[0], acpModeConfigOptionID).CurrentValue; got != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("mode option currentValue = %q, want %q", got, runtimepolicy.ModeAcceptEdits)
	}
	modeUpdates := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateCurrentModeUpdate)
	if len(modeUpdates) != 1 || modeUpdates[0].CurrentModeID != string(runtimepolicy.ModeAcceptEdits) {
		t.Fatalf("current_mode_update = %+v, want %q", modeUpdates, runtimepolicy.ModeAcceptEdits)
	}
}

// TestACPConfigOptionCommandBareModeListsChoices pins the bare-command
// behaviour: without an argument the host answers with the selectable values
// (a headless client cannot render the TUI picker) and changes nothing.
func TestACPConfigOptionCommandBareModeListsChoices(t *testing.T) {
	host, chat := newConfigOptionTestHost("sess-1", "m1", "m1")
	hostSess := host.sess["sess-1"]
	rec := &recordingACPEmitter{}
	host.SetSessionEmitter(rec)
	before := chat.PermissionMode

	host.emitACPConfigOptionCommand(context.Background(), "sess-1", hostSess, "mode", "", rec)

	if chat.PermissionMode != before {
		t.Fatalf("bare command changed the mode from %q to %q", before, chat.PermissionMode)
	}
	if len(rec.snapshot()) == 0 {
		t.Fatal("bare command must answer with the selectable values")
	}
	if got := acpUpdatesOfKind(rec.snapshot(), acp.SessionUpdateConfigOptionUpdate); len(got) != 0 {
		t.Fatalf("bare command must not push a state change: %+v", got)
	}
}
