package client

import (
	"testing"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
)

func TestClientOptionsKeepAliveOnlyForRemote(t *testing.T) {
	remote := &config.MCPConfig{ExecutionScope: config.ExecutionScopeRemote}
	if got := clientOptionsFor(remote).KeepAlive; got != remoteSessionKeepAlive {
		t.Fatalf("remote keepalive = %v, want %v", got, remoteSessionKeepAlive)
	}
	if remoteSessionKeepAlive <= 0 || remoteSessionKeepAlive > time.Minute {
		t.Fatalf("remote keepalive must be positive and <= 1m, got %v", remoteSessionKeepAlive)
	}

	local := &config.MCPConfig{}
	if got := clientOptionsFor(local).KeepAlive; got != 0 {
		t.Fatalf("local keepalive = %v, want 0", got)
	}
	if got := clientOptionsFor(nil).KeepAlive; got != 0 {
		t.Fatalf("nil keepalive = %v, want 0", got)
	}
}
