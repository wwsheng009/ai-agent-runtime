package remotetools

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestAcquireIdentityLockRejectsASecondHolder(t *testing.T) {
	identity := filepath.Join(t.TempDir(), "mount-id")
	first, err := acquireIdentityLock(identity)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	second, err := acquireIdentityLock(identity)
	if err == nil {
		_ = second.Close()
		t.Fatalf("second lock succeeded while the first was held")
	}
	if !strings.Contains(err.Error(), "--instance") {
		t.Fatalf("lock error = %v, want an --instance hint", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first lock: %v", err)
	}
	third, err := acquireIdentityLock(identity)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	_ = third.Close()
}
