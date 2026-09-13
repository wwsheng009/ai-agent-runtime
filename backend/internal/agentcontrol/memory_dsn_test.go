package agentcontrol

import (
	"strings"
	"testing"
)

// TestDefaultMemoryStoreDSNsStayUnique reproduces the coarse-clock hazard for
// the default in-memory stores: two registries created inside one clock tick
// used to resolve to the same cache=shared database, so one store could observe
// (and mutate) the rows of the other. The bug is invisible on hosts with a fine
// clock and deterministic on Windows guests, where time.Now().UnixNano() does
// not advance between consecutive calls.
func TestDefaultMemoryStoreDSNsStayUnique(t *testing.T) {
	const configs = 16

	agentDSNs := make(map[string]struct{}, configs)
	mailboxDSNs := make(map[string]struct{}, configs)
	for i := 0; i < configs; i++ {
		dsn, path, err := resolveLazyGlobalAgentDSN(&GlobalAgentStoreConfig{})
		if err != nil {
			t.Fatalf("resolveLazyGlobalAgentDSN: %v", err)
		}
		if path != "" {
			t.Fatalf("expected an in-memory DSN, got path %q", path)
		}
		if !strings.Contains(dsn, "mode=memory") {
			t.Fatalf("expected an in-memory DSN, got %q", dsn)
		}
		if _, duplicate := agentDSNs[dsn]; duplicate {
			t.Fatalf("agent registry DSN %q was handed out twice", dsn)
		}
		agentDSNs[dsn] = struct{}{}

		mailboxDSN, mailboxPath, err := resolveLazyGlobalMailboxDSN(&GlobalMailboxStoreConfig{})
		if err != nil {
			t.Fatalf("resolveLazyGlobalMailboxDSN: %v", err)
		}
		if mailboxPath != "" {
			t.Fatalf("expected an in-memory DSN, got path %q", mailboxPath)
		}
		if !strings.Contains(mailboxDSN, "mode=memory") {
			t.Fatalf("expected an in-memory DSN, got %q", mailboxDSN)
		}
		if _, duplicate := mailboxDSNs[mailboxDSN]; duplicate {
			t.Fatalf("mailbox registry DSN %q was handed out twice", mailboxDSN)
		}
		mailboxDSNs[mailboxDSN] = struct{}{}
	}
}

// TestExplicitMemoryDSNIsStillRespected makes sure the uuid default did not
// change the documented override path: an explicit DSN (including a shared
// in-memory one) must be returned untouched.
func TestExplicitMemoryDSNIsStillRespected(t *testing.T) {
	const shared = "file:agent-control-explicit?mode=memory&cache=shared"

	dsn, path, err := resolveLazyGlobalAgentDSN(&GlobalAgentStoreConfig{DSN: shared})
	if err != nil {
		t.Fatalf("resolveLazyGlobalAgentDSN: %v", err)
	}
	if path != "" {
		t.Fatalf("explicit DSN must not resolve to a path, got %q", path)
	}
	if !strings.Contains(dsn, "file:agent-control-explicit") {
		t.Fatalf("explicit DSN was rewritten to %q", dsn)
	}

	mailboxDSN, _, err := resolveLazyGlobalMailboxDSN(&GlobalMailboxStoreConfig{DSN: shared})
	if err != nil {
		t.Fatalf("resolveLazyGlobalMailboxDSN: %v", err)
	}
	if !strings.Contains(mailboxDSN, "file:agent-control-explicit") {
		t.Fatalf("explicit mailbox DSN was rewritten to %q", mailboxDSN)
	}
}
