package commands

import (
	"strings"
	"testing"
	"time"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

func TestGatewaySubcommand(t *testing.T) {
	cases := []struct {
		command string
		want    string
	}{
		{"/gateway", ""},
		{"/gateway   ", ""},
		{"/gateway status", "status"},
		{"/gateway Connect", "connect"},
		{"/gateway url --json", "url"},
		{"/gateway disconnect", "disconnect"},
	}
	for _, tc := range cases {
		if got := gatewaySubcommand(tc.command); got != tc.want {
			t.Fatalf("gatewaySubcommand(%q) = %q, want %q", tc.command, got, tc.want)
		}
	}
}

func TestFormatGatewayStatusConnected(t *testing.T) {
	st := gatewayagent.Status{
		State:          gatewayagent.StateConnected,
		NodeID:         "node-aicli-9f3a",
		GatewayURL:     "wss://gw.example.com",
		ConsoleURL:     "https://gw.example.com/n/node-aicli-9f3a/web/",
		ConnectedAt:    time.Unix(1_700_000_000, 0).UTC(),
		LastHeartbeat:  time.Unix(1_700_000_001, 0).UTC(),
		ReconnectCount: 2,
		Sessions:       3,
		Mode:           gatewayagent.ModeAuto,
	}
	got := formatGatewayStatus(st)
	for _, want := range []string{"connected", "node-aicli-9f3a", "https://gw.example.com/n/", "sessions    3", "reconnects  2", "mode        auto"} {
		if !strings.Contains(got, want) {
			t.Fatalf("status text missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "抑制") {
		t.Fatalf("connected status must not mention suppression:\n%s", got)
	}
}

func TestFormatGatewayStatusSuppressed(t *testing.T) {
	st := gatewayagent.Status{State: gatewayagent.StateDisconnected, OperatorDisconnected: true}
	if got := formatGatewayStatus(st); !strings.Contains(got, "抑制") {
		t.Fatalf("disconnected-by-operator status must mention suppression:\n%s", got)
	}
}

func TestFormatGatewayStatusTerminalError(t *testing.T) {
	st := gatewayagent.Status{
		State:     gatewayagent.StateError,
		ErrorCode: gatewayagent.ErrCodeAuthInvalid,
		LastError: "device token revoked",
		Retryable: false,
	}
	got := formatGatewayStatus(st)
	if !strings.Contains(got, gatewayagent.ErrCodeAuthInvalid) {
		t.Fatalf("error code missing:\n%s", got)
	}
	if !strings.Contains(got, "不可自动重试") {
		t.Fatalf("terminal error must carry the no-retry hint:\n%s", got)
	}
}

func TestGatewayHelpTextListsSubcommands(t *testing.T) {
	got := gatewayHelpText()
	for _, want := range []string{"status", "connect", "disconnect", "reconnect", "url", "help"} {
		if !strings.Contains(got, want) {
			t.Fatalf("help text missing %q:\n%s", want, got)
		}
	}
}

func TestGatewayNotConfiguredText(t *testing.T) {
	got := gatewayNotConfiguredText()
	if !strings.Contains(got, "未配置") || !strings.Contains(got, "--gateway") {
		t.Fatalf("not-configured text must explain how to enable:\n%s", got)
	}
}

func TestGatewayCommandHandledWithoutController(t *testing.T) {
	result, handled, err := tryExecuteStructuredChatCommand(&ChatSession{}, "/gateway")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !handled {
		t.Fatal("/gateway must be claimed even without a controller")
	}
	if len(result.Blocks) == 0 {
		t.Fatal("expected a text block for the not-configured message")
	}
}

func TestGatewayCommandHandledWithController(t *testing.T) {
	session := &ChatSession{Gateway: gatewayagent.New(gatewayagent.Config{Enabled: false}, nil)}
	for _, command := range []string{"/gateway", "/gateway status", "/gateway url", "/gateway help"} {
		result, handled, err := tryExecuteStructuredChatCommand(session, command)
		if err != nil || !handled {
			t.Fatalf("%s handled=%v err=%v", command, handled, err)
		}
		if len(result.Blocks) == 0 {
			t.Fatalf("%s produced no output block", command)
		}
	}
}

func TestGatewayCommandUnknownSubcommandStillHandled(t *testing.T) {
	session := &ChatSession{Gateway: gatewayagent.New(gatewayagent.Config{Enabled: false}, nil)}
	result, handled, err := tryExecuteStructuredChatCommand(session, "/gateway bogus")
	if err != nil || !handled {
		t.Fatalf("unknown subcommand handled=%v err=%v", handled, err)
	}
	if len(result.Blocks) == 0 {
		t.Fatal("expected help text for an unknown subcommand")
	}
}

func TestGatewayDisconnectDoesNotTouchConfig(t *testing.T) {
	// A manual disconnect must be a runtime-only action: the controller's config
	// stays enabled so a restart re-registers from configuration.
	controller := gatewayagent.New(gatewayagent.Config{Enabled: true, URL: "wss://gw", Token: "agw_x"}, nil)
	session := &ChatSession{Gateway: controller}
	if _, handled, err := tryExecuteStructuredChatCommand(session, "/gateway disconnect"); err != nil || !handled {
		t.Fatalf("disconnect handled=%v err=%v", handled, err)
	}
	if !controller.Config().Enabled {
		t.Fatal("disconnect must not disable the persisted config")
	}
	if !controller.Status().OperatorDisconnected {
		t.Fatal("disconnect must set the operator-disconnected flag")
	}
}
