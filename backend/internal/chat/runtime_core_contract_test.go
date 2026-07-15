package chat

import "testing"

func TestSessionActorRuntimeCoreSatisfiesUnifiedContract(t *testing.T) {
	descriptor := SessionActorRuntimeCore()
	if !IsSessionActorRuntimeCore(descriptor) {
		t.Fatalf("expected unified runtime descriptor, got %#v", descriptor)
	}
	if descriptor.ContractVersion <= 0 {
		t.Fatalf("expected positive contract version, got %d", descriptor.ContractVersion)
	}

	descriptor.EventProtocol = "legacy_events"
	if IsSessionActorRuntimeCore(descriptor) {
		t.Fatal("expected mismatched event protocol to fail the unified contract")
	}
}
