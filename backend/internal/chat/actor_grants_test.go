package chat

import (
	"path/filepath"
	"testing"

	runtimepolicy "github.com/wwsheng009/ai-agent-runtime/internal/policy"
)

func TestNewPermissionGrantStores(t *testing.T) {
	session, project := newPermissionGrantStores("")
	if session == nil {
		t.Fatal("expected a session grant store")
	}
	if project != nil {
		t.Fatalf("empty root must not create a project store, got %#v", project)
	}

	root := t.TempDir()
	session, project = newPermissionGrantStores(root)
	if session == nil || project == nil {
		t.Fatalf("expected both stores, got session=%v project=%v", session, project)
	}
	fileStore, ok := project.(*runtimepolicy.FileGrantStore)
	if !ok {
		t.Fatalf("project store = %T, want *FileGrantStore", project)
	}
	want := filepath.Join(root, ".aicli", runtimepolicy.DefaultGrantsFileName)
	if fileStore.Path() != want {
		t.Fatalf("project store path = %q, want %q", fileStore.Path(), want)
	}
}

func TestAttachPermissionGrantStores(t *testing.T) {
	session, project := newPermissionGrantStores(t.TempDir())
	engine := &runtimepolicy.Engine{}
	attachPermissionGrantStores(engine, session, project)
	if engine.Grants != runtimepolicy.GrantStore(session) {
		t.Fatal("session store was not attached")
	}
	if engine.ProjectGrants != project {
		t.Fatal("project store was not attached")
	}

	// Host-provided stores win: attach must not clobber them.
	hostSession := &runtimepolicy.MemoryGrantStore{}
	hostProject, err := runtimepolicy.OpenProjectGrantStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	engine = &runtimepolicy.Engine{Grants: hostSession, ProjectGrants: hostProject}
	attachPermissionGrantStores(engine, session, project)
	if engine.Grants != runtimepolicy.GrantStore(hostSession) {
		t.Fatal("attach overwrote a host-provided session store")
	}
	if engine.ProjectGrants != runtimepolicy.GrantStore(hostProject) {
		t.Fatal("attach overwrote a host-provided project store")
	}

	// Nil engine and nil stores are no-ops.
	attachPermissionGrantStores(nil, session, project)
	attachPermissionGrantStores(&runtimepolicy.Engine{}, nil, nil)
}

func TestSessionActorPermissionGrantStoresAreLazyAndStable(t *testing.T) {
	actor := &SessionActor{}
	session, project := actor.permissionGrantStores()
	if session == nil {
		t.Fatal("expected a session store")
	}
	// The actor has no agent, so the workspace root falls back to the process
	// working directory and a project store is still available.
	if project == nil {
		t.Fatal("expected a project store from the working directory fallback")
	}
	session2, project2 := actor.permissionGrantStores()
	if session2 != session || project2 != project {
		t.Fatal("grant stores must be created once per actor")
	}
}
