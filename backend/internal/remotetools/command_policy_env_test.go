package remotetools

import (
	"strings"
	"testing"
)

// TestCommandPolicyDefaultInheritsProcessEnvironment pins the new default: the
// remote shell sees the target process environment (the machine/user union on
// Windows), so tools such as Go can find LOCALAPPDATA and build caches.
func TestCommandPolicyDefaultInheritsProcessEnvironment(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{AllowedCommands: []string{"*"}}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}

	filtered := policy.FilterEnv([]string{
		"PATH=/bin",
		"LOCALAPPDATA=C:\\Users\\tester\\AppData\\Local",
		"APPDATA=C:\\Users\\tester\\AppData\\Roaming",
		"AGW_LOCAL_API_KEY=secret",
		"AICLI_REMOTE_TOKEN=secret",
	})
	if _, ok := envEntry(filtered, "LOCALAPPDATA"); !ok {
		t.Fatalf("LOCALAPPDATA was dropped: %#v", filtered)
	}
	if _, ok := envEntry(filtered, "APPDATA"); !ok {
		t.Fatalf("APPDATA was dropped: %#v", filtered)
	}
	for _, name := range []string{"AGW_LOCAL_API_KEY", "AICLI_REMOTE_TOKEN"} {
		if _, ok := envEntry(filtered, name); ok {
			t.Fatalf("credential-like variable %s leaked: %#v", name, filtered)
		}
	}
}

func TestCommandPolicyExplicitAllowlistOptsInToCredential(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{
		AllowedCommands: []string{"*"},
		EnvAllowlist:    []string{"LOCALAPPDATA", "MY_TOKEN"},
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}

	filtered := policy.FilterEnv([]string{
		"PATH=/bin",
		"LOCALAPPDATA=C:\\Users\\tester\\AppData\\Local",
		"APPDATA=C:\\Users\\tester\\AppData\\Roaming",
		"MY_TOKEN=explicitly-allowed",
		"OTHER_VAR=drop-me",
	})
	for _, name := range []string{"PATH", "LOCALAPPDATA", "MY_TOKEN"} {
		if _, ok := envEntry(filtered, name); !ok {
			t.Fatalf("explicit allowlist dropped %s: %#v", name, filtered)
		}
	}
	for _, name := range []string{"APPDATA", "OTHER_VAR"} {
		if _, ok := envEntry(filtered, name); ok {
			t.Fatalf("explicit allowlist kept %s: %#v", name, filtered)
		}
	}
}

func TestCommandPolicyExplicitEmptyEnvironment(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{
		AllowedCommands: []string{"*"},
		EnvAllowlist:    []string{},
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}
	if filtered := policy.FilterEnv([]string{"PATH=/bin", "HOME=/home/tester"}); len(filtered) != 0 {
		t.Fatalf("explicit empty allowlist kept %#v", filtered)
	}
}

func TestCommandPolicyDisableInheritanceUsesBaseline(t *testing.T) {
	policy, err := NewCommandPolicy(ShellConfig{
		AllowedCommands:       []string{"*"},
		DisableEnvInheritance: true,
	}, 0)
	if err != nil {
		t.Fatalf("NewCommandPolicy: %v", err)
	}

	filtered := policy.FilterEnv([]string{
		"PATH=/bin",
		"HOME=/home/tester",
		"LOCALAPPDATA=C:\\Users\\tester\\AppData\\Local",
		"APPDATA=C:\\Users\\tester\\AppData\\Roaming",
	})
	if _, ok := envEntry(filtered, "PATH"); !ok {
		t.Fatalf("baseline dropped PATH: %#v", filtered)
	}
	for _, name := range []string{"LOCALAPPDATA", "APPDATA"} {
		if _, ok := envEntry(filtered, name); ok {
			t.Fatalf("disabled inheritance kept %s: %#v", name, filtered)
		}
	}
}

func envEntry(env []string, name string) (string, bool) {
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if ok && strings.EqualFold(key, name) {
			return value, true
		}
	}
	return "", false
}
