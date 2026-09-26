package policy

import "testing"

func TestClassifySensitivePath(t *testing.T) {
	cases := []struct {
		path string
		kind SensitivePathKind
	}{
		{".env", SensitivePathSecret},
		{".env.local", SensitivePathSecret},
		{"config/.env.production", SensitivePathSecret},
		{"/home/user/.ssh/id_rsa", SensitivePathSecret},
		{"id_rsa.pub", SensitivePathSecret},
		{"certs/server.pem", SensitivePathSecret},
		{"certs/server.key", SensitivePathSecret},
		{".netrc", SensitivePathSecret},
		{"credentials.json", SensitivePathSecret},
		{"C:\\Users\\vince\\.aws\\credentials", SensitivePathSecret},
		{".bashrc", SensitivePathPersistence},
		{".gitconfig", SensitivePathPersistence},
		{".vscode/settings.json", SensitivePathPersistence},
		{".devcontainer/devcontainer.json", SensitivePathPersistence},
		{"node_modules/.bin/tsc", SensitivePathPersistence},
		{".git/config", SensitivePathVCS},
		{"repo/.git/hooks/pre-commit", SensitivePathVCS},
		{".aicli/permissions.yaml", SensitivePathControlPlane},
		{"project/.aicli/grants.json", SensitivePathControlPlane},
		{"project/.aicli/permissions.local.yml", SensitivePathControlPlane},
		{"permissions.yaml", SensitivePathControlPlane},
		{".mcp.json", SensitivePathControlPlane},
	}
	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			match, ok := ClassifySensitivePath(tc.path)
			if !ok {
				t.Fatalf("ClassifySensitivePath(%q) = clean, want %s", tc.path, tc.kind)
			}
			if match.Kind != tc.kind {
				t.Fatalf("ClassifySensitivePath(%q) kind = %q, want %q", tc.path, match.Kind, tc.kind)
			}
		})
	}
}

func TestClassifySensitivePathClean(t *testing.T) {
	cases := []string{
		"",
		"README.md",
		"src/main.go",
		"docs/analysis/report.md",
		".gitignore",
		".github/workflows/ci.yml",
		"docs/environment.md",
		"config/app.yaml",
		"server.pub",
		"keys/public.pem.bak",
	}
	for _, path := range cases {
		t.Run(path, func(t *testing.T) {
			if match, ok := ClassifySensitivePath(path); ok {
				t.Fatalf("ClassifySensitivePath(%q) = %+v, want clean", path, match)
			}
		})
	}
}

func TestSensitiveReadArgument(t *testing.T) {
	for _, argument := range []string{".env", "sub/.env", "/home/user/.ssh/id_rsa", "~/.aws/credentials", "$HOME/.git-credentials"} {
		if !SensitiveReadArgument(argument) {
			t.Fatalf("SensitiveReadArgument(%q) = false, want true", argument)
		}
	}
	for _, argument := range []string{"README.md", "-n", "src/main.go", "go.mod"} {
		if SensitiveReadArgument(argument) {
			t.Fatalf("SensitiveReadArgument(%q) = true, want false", argument)
		}
	}
}
