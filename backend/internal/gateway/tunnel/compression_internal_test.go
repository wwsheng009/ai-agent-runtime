package tunnel

import "testing"

// TestNewServerUpgraderEnablesCompression pins the transport settings the
// gateway advertises to agents: permessage-deflate and 32 KiB buffers. An
// older agent that does not offer the extension still negotiates cleanly and
// stays uncompressed.
func TestNewServerUpgraderEnablesCompression(t *testing.T) {
	server := NewServer(Config{ServerVersion: "test"})
	if !server.upgrader.EnableCompression {
		t.Fatal("agent tunnel upgrader must accept permessage-deflate")
	}
	if server.upgrader.ReadBufferSize != 32<<10 || server.upgrader.WriteBufferSize != 32<<10 {
		t.Fatalf("upgrader buffers = %d/%d, want 32KiB",
			server.upgrader.ReadBufferSize, server.upgrader.WriteBufferSize)
	}
}