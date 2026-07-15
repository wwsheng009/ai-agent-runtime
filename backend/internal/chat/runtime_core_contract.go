package chat

// RuntimeCoreDescriptor identifies the durable execution contract shared by
// in-process aicli and runtime-server transports.
type RuntimeCoreDescriptor struct {
	Name              string `json:"name"`
	ContractVersion   int    `json:"contract_version"`
	Lifecycle         string `json:"lifecycle"`
	StateAuthority    string `json:"state_authority"`
	EventProtocol     string `json:"event_protocol"`
	ApprovalProtocol  string `json:"approval_protocol"`
	BackgroundDurable bool   `json:"background_durable"`
}

const (
	RuntimeCoreSessionActor     = "session_actor"
	RuntimeCoreContractVersion  = 1
	RuntimeCoreLifecycle        = "durable_session_actor"
	RuntimeCoreStateAuthority   = "runtime_state_store"
	RuntimeCoreEventProtocol    = "session_runtime_events"
	RuntimeCoreApprovalProtocol = "runtime_command_relay"
)

// SessionActorRuntimeCore describes the only supported interactive execution
// core for aicli Chat, Exec, Resume, and runtime-server commands.
func SessionActorRuntimeCore() RuntimeCoreDescriptor {
	return RuntimeCoreDescriptor{
		Name:              RuntimeCoreSessionActor,
		ContractVersion:   RuntimeCoreContractVersion,
		Lifecycle:         RuntimeCoreLifecycle,
		StateAuthority:    RuntimeCoreStateAuthority,
		EventProtocol:     RuntimeCoreEventProtocol,
		ApprovalProtocol:  RuntimeCoreApprovalProtocol,
		BackgroundDurable: true,
	}
}

// IsSessionActorRuntimeCore reports whether descriptor satisfies the current
// aicli/runtime-server execution contract.
func IsSessionActorRuntimeCore(descriptor RuntimeCoreDescriptor) bool {
	return descriptor.Name == RuntimeCoreSessionActor &&
		descriptor.ContractVersion == RuntimeCoreContractVersion &&
		descriptor.Lifecycle == RuntimeCoreLifecycle &&
		descriptor.StateAuthority == RuntimeCoreStateAuthority &&
		descriptor.EventProtocol == RuntimeCoreEventProtocol &&
		descriptor.ApprovalProtocol == RuntimeCoreApprovalProtocol &&
		descriptor.BackgroundDurable
}
