package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNotifyDeliveryLedgerClaimReleasePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify-delivery.json")
	l := newNotifyDeliveryLedger(path, time.Hour, 8)
	if !l.Claim("a|b|e1") {
		t.Fatal("first claim should succeed")
	}
	if l.Claim("a|b|e1") {
		t.Fatal("second claim should be deduped")
	}
	l.Release("a|b|e1")
	if !l.Claim("a|b|e1") {
		t.Fatal("claim after release should succeed")
	}
	if err := l.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	reloaded := newNotifyDeliveryLedger(path, time.Hour, 8)
	if reloaded.Claim("a|b|e1") {
		t.Fatal("persisted claim should dedup after reload")
	}
	if !reloaded.Claim("a|b|e2") {
		t.Fatal("new key should claim")
	}
}

func TestNotifyDeliveryLedgerTTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notify-delivery.json")
	old := time.Now().Add(-2 * time.Hour).UnixNano()
	raw, err := json.Marshal(notifyDeliveryFile{Entries: map[string]int64{"old|k": old}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	l := newNotifyDeliveryLedger(path, time.Hour, 8)
	if !l.Claim("old|k") {
		t.Fatal("expired entry should be claimable")
	}
}

func TestNotifyDeliveryLedgerCapEvictsOldest(t *testing.T) {
	l := newNotifyDeliveryLedger(filepath.Join(t.TempDir(), "cap.json"), time.Hour, 2)
	if !l.Claim("k1") || !l.Claim("k2") {
		t.Fatal("initial claims should succeed")
	}
	time.Sleep(2 * time.Millisecond)
	if !l.Claim("k3") {
		t.Fatal("third claim should succeed")
	}
	time.Sleep(2 * time.Millisecond)
	if !l.Claim("k1") {
		t.Fatal("oldest entry should be evicted and claimable")
	}
}
