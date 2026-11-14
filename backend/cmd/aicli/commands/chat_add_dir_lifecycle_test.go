package commands

import (
	"context"
	"reflect"
	"testing"

	config "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimechat "github.com/wwsheng009/ai-agent-runtime/internal/chat"
)

func TestChatAllowedRootsMetadataOverridesStaleField(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  interface{}
		want []string
	}{
		{"automatic_admission", []interface{}{"/ext/new"}, []string{"/ext/new"}},
		{"revoked_empty", []string{}, nil},
		{"revoked_null", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := newAddDirTestSession("")
			session.AllowedRoots = []string{"/ext/stale"}
			session.RuntimeSession.SetContext(chatAllowedRootsContextKey, tc.raw)
			if got := chatAllowedRoots(session); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("roots = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestRestoreChatPersistenceStateMergesExplicitAddDir(t *testing.T) {
	for _, tc := range []struct {
		name    string
		hasKey  bool
		stored  []string
		startup []string
		want    []string
	}{
		{"stored_and_explicit", true, []string{"/ext/a"}, []string{"/ext/b"}, []string{"/ext/a", "/ext/b"}},
		{"explicit_readmission", true, nil, []string{"/ext/b"}, []string{"/ext/b"}},
		{"absent_metadata", false, nil, []string{"/ext/b"}, []string{"/ext/b"}},
		{"revocation_without_flag", true, nil, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := runtimechat.NewInMemoryStorage()
			manager := runtimechat.NewSessionManager(storage, nil)
			defer manager.Stop()
			loaded, err := manager.Create(context.Background(), "add-dir-user")
			if err != nil {
				t.Fatal(err)
			}
			if tc.hasKey {
				loaded.SetContext(chatAllowedRootsContextKey, tc.stored)
				if err := storage.Update(context.Background(), loaded); err != nil {
					t.Fatal(err)
				}
			}
			session := &ChatSession{
				Provider:       config.Provider{Protocol: "openai"},
				SessionManager: manager,
				SessionUserID:  "add-dir-user",
				Ephemeral:      true,
				AllowedRoots:   []string{"/ext/stale"},
			}
			if err := restoreChatPersistenceState(session, &chatPersistenceState{
				loadedRuntimeSession: loaded,
			}, &chatCommandOptions{AllowedDirs: tc.startup}); err != nil {
				t.Fatal(err)
			}
			if got := chatAllowedRoots(session); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("live roots = %#v, want %#v", got, tc.want)
			}
			stored, err := storage.Load(context.Background(), loaded.ID)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := stored.GetContext(chatAllowedRootsContextKey)
			if got := normalizePersistedAllowedRoots(raw); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("stored roots = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestSyncRuntimeSessionFromChatPreservesDurableAllowedRoots(t *testing.T) {
	for _, planMutated := range []bool{false, true} {
		for _, tc := range []struct {
			name string
			want []string
		}{
			{"automatic_admission", []string{"/ext/old", "/ext/new"}},
			{"revocation", nil},
		} {
			t.Run(tc.name+map[bool]string{false: "/ordinary", true: "/plan_mutation"}[planMutated], func(t *testing.T) {
				ctx := context.Background()
				storage := runtimechat.NewInMemoryStorage()
				manager := runtimechat.NewSessionManager(storage, nil)
				defer manager.Stop()
				live, err := manager.Create(ctx, "add-dir-user")
				if err != nil {
					t.Fatal(err)
				}
				live.SetContext(chatAllowedRootsContextKey, []string{"/ext/old"})
				session := &ChatSession{
					Provider:       config.Provider{Protocol: "openai"},
					SessionManager: manager,
					SessionUserID:  "add-dir-user",
					RuntimeSession: live,
					AllowedRoots:   []string{"/ext/old"},
				}
				stored := live.Clone()
				stored.SetContext(chatAllowedRootsContextKey, tc.want)
				if err := storage.Update(ctx, stored); err != nil {
					t.Fatal(err)
				}
				if err := syncRuntimeSessionFromChatMode(session, false, planMutated); err != nil {
					t.Fatal(err)
				}
				reloaded, err := storage.Load(ctx, live.ID)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := reloaded.GetContext(chatAllowedRootsContextKey)
				if got := normalizePersistedAllowedRoots(raw); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("checkpoint changed durable roots: got %#v, want %#v", got, tc.want)
				}
				if got := chatAllowedRoots(session); !reflect.DeepEqual(got, tc.want) {
					t.Fatalf("live roots = %#v, want %#v", got, tc.want)
				}
			})
		}
	}
}

func TestChatAllowedRootsDoNotLeakAcrossSessionSwitch(t *testing.T) {
	manager := runtimechat.NewSessionManager(runtimechat.NewInMemoryStorage(), nil)
	defer manager.Stop()
	session := &ChatSession{
		SessionManager: manager,
		SessionUserID:  "add-dir-user",
		Ephemeral:      true,
		AllowedRoots:   []string{"/ext/previous"},
	}
	if err := restoreChatStateFromRuntimeSession(session, runtimechat.NewSession("add-dir-user")); err != nil {
		t.Fatal(err)
	}
	if got := chatAllowedRoots(session); len(got) != 0 {
		t.Fatalf("restore inherited the previous conversation's roots: %#v", got)
	}
	setChatAllowedRoots(session, []string{"/ext/current"})
	if err := createNewRuntimeConversation(session, "new conversation"); err != nil {
		t.Fatal(err)
	}
	if got := chatAllowedRoots(session); len(got) != 0 {
		t.Fatalf("new conversation inherited old roots: %#v", got)
	}
}
