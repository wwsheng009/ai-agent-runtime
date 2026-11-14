package chat

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	runtimeevents "github.com/wwsheng009/ai-agent-runtime/internal/events"
)

// AllowedRootsContextKey is shared by actor admission memory and host /add-dir
// controls. An explicitly empty list is a revocation, not a missing value.
const AllowedRootsContextKey = "allowed_roots"

// prepareExternalDirectoryAdmission binds admission memory to the actual actor
// session, not the CLI's base ChatSession. Each run starts from its durable
// roots; the live snapshot lets later tools in the same run see new admissions
// without rebuilding the actor or mutating agent options during evaluation.
func (a *SessionActor) prepareExternalDirectoryAdmission(ctx context.Context, session *Session) {
	if a == nil || a.agent == nil || session == nil || session.ID != a.id {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	engine := a.agent.GetPermissionEngine()
	if engine == nil {
		return
	}
	// Embedded hosts may own admission persistence themselves. Rebind our own
	// callback on later runs, but do not wrap/call an earlier run's closure.
	if engine.ApproveExternalDir != nil && engine != a.externalDirEngine {
		return
	}
	a.externalDirEngine = engine
	var mu sync.RWMutex
	roots := sessionAllowedRoots(session)
	engine.ExternalAllowedRootsSource = func() []string {
		mu.RLock()
		defer mu.RUnlock()
		return append([]string(nil), roots...)
	}
	engine.ApproveExternalDir = func(dirs []string) {
		mu.Lock()
		defer mu.Unlock()
		if len(dirs) == 0 || ctx.Err() != nil {
			return
		}
		// This callback runs synchronously in the owning tool loop. Persist the
		// live session, not a separately loaded history snapshot. The metadata
		// merge applies only these new roots over the latest stored set.
		if err := a.persistSessionWithExternalDirs(ctx, session, dirs); err != nil {
			a.publish(runtimeevents.Event{
				Type:      "session.external_dir_admission_persist_error",
				SessionID: a.id,
				Payload:   map[string]interface{}{"error": err.Error()},
			})
			// Approval still permits this call, but failed persistence must not
			// silently grant future calls or claim durable admission.
			return
		}
		roots = sessionAllowedRoots(session)
	}
}

func sessionAllowedRoots(session *Session) []string {
	if session == nil {
		return nil
	}
	raw, _ := session.GetContext(AllowedRootsContextKey)
	return normalizeSessionAllowedRoots(raw)
}

// A recovered approval has no in-memory policy waiter, so the ordinary engine
// callback never runs. Only remember the durable engine-derived set for this
// exact session/tool. Patched arguments and legacy approvals without the set
// stay one-shot; do not guess a broader grant from their current paths.
func (a *SessionActor) rememberResumedExternalDirectoryAdmission(ctx context.Context, session *Session, approval *ApprovalRequest, pending *PendingToolInvocation, patched bool) {
	if a == nil || session == nil || session.ID != a.id || approval == nil || pending == nil ||
		approval.SessionID != a.id || approval.ToolCallID != pending.ToolCallID || approval.ToolName != pending.ToolName ||
		patched || len(approval.ExternalDirs) == 0 {
		return
	}
	if err := a.persistSessionWithExternalDirs(ctx, session, approval.ExternalDirs); err != nil {
		a.publish(runtimeevents.Event{
			Type:      "session.external_dir_admission_persist_error",
			SessionID: a.id,
			Payload:   map[string]interface{}{"error": err.Error()},
		})
	}
}

func normalizeSessionAllowedRoots(raw interface{}) []string {
	var values []string
	switch value := raw.(type) {
	case []string:
		values = value
	case []interface{}:
		for _, item := range value {
			if path, ok := item.(string); ok {
				values = append(values, path)
			}
		}
	case string:
		values = strings.FieldsFunc(value, func(r rune) bool {
			return r == ',' || r == ';' || r == '\n'
		})
	}
	return mergeSessionAllowedRoots(nil, values)
}

func mergeSessionAllowedRoots(roots, added []string) []string {
	out := make([]string, 0, len(roots)+len(added))
	seen := make(map[string]bool, cap(out))
	for _, paths := range [][]string{roots, added} {
		for _, path := range paths {
			path = strings.TrimSpace(path)
			if path == "" {
				continue
			}
			path = filepath.Clean(path)
			key := path
			if runtime.GOOS == "windows" {
				key = strings.ToLower(key)
			}
			if !seen[key] {
				seen[key] = true
				out = append(out, path)
			}
		}
	}
	return out
}

// mergeExternalDirectoryAdmission keeps host-side additions and revocations
// from being overwritten by an older run snapshot. Only freshly approved dirs
// may widen the durable set; ordinary checkpoints never resurrect old roots.
func mergeExternalDirectoryAdmission(session, latest *Session, admitted []string) {
	roots := sessionAllowedRoots(session)
	hasStoredRoots := false
	if latest != nil {
		if raw, ok := latest.GetContext(AllowedRootsContextKey); ok {
			roots = normalizeSessionAllowedRoots(raw)
			hasStoredRoots = true
		}
	}
	if hasStoredRoots || len(admitted) > 0 {
		session.SetContext(AllowedRootsContextKey, mergeSessionAllowedRoots(roots, admitted))
	}
}
