package audit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLogEntriesNewestFirstAndLimit(t *testing.T) {
	log := New(100, "")
	base := time.Unix(1_700_000_000, 0)
	log.now = func() time.Time { return base }

	log.Record(Event{Type: TypeLoginSuccess, IP: "203.0.113.7"})
	log.Record(Event{Type: TypeDeviceCreated, Detail: "laptop"})
	log.Record(Event{Type: TypeManualBan, IP: "203.0.113.9"})

	entries := log.Entries(2)
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	if entries[0].Type != TypeManualBan {
		t.Fatalf("newest first expected manual_ban, got %s", entries[0].Type)
	}
	if entries[1].Type != TypeDeviceCreated {
		t.Fatalf("second entry = %s", entries[1].Type)
	}
}

func TestLogRingBufferTrims(t *testing.T) {
	log := New(2, "")
	log.Record(Event{Type: "a"})
	log.Record(Event{Type: "b"})
	log.Record(Event{Type: "c"})
	entries := log.Entries(0)
	if len(entries) != 2 {
		t.Fatalf("buffer should keep 2, got %d", len(entries))
	}
	if entries[0].Type != "c" || entries[1].Type != "b" {
		t.Fatalf("unexpected entries: %+v", entries)
	}
}

func TestLogPersistsToFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.log")
	log := New(10, path)
	log.Record(Event{Type: TypeLoginFailure, IP: "198.51.100.9"})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read audit file: %v", err)
	}
	if !strings.Contains(string(data), TypeLoginFailure) {
		t.Fatalf("audit file missing event: %s", data)
	}
}
