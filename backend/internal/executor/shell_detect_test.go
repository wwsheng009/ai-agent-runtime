package executor

import (
	"encoding/base64"
	"strings"
	"testing"
	"unicode/utf16"
)

func decodePowerShellEncoded(t *testing.T, payload string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("UTF-16LE payload must have an even byte count, got %d", len(raw))
	}
	units := make([]uint16, 0, len(raw)/2)
	for index := 0; index < len(raw); index += 2 {
		units = append(units, uint16(raw[index])|uint16(raw[index+1])<<8)
	}
	return string(utf16.Decode(units))
}

func assertShellArgs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("unexpected argv length: got %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("unexpected argv: got %#v, want %#v", got, want)
		}
	}
}

func TestDeriveExecArgsHardensPowerShellAgentShell(t *testing.T) {
	shell := Shell{Path: "pwsh", Type: ShellTypePwsh}

	// Agent shell (login=false): profile and interactive prompts must not be
	// able to block or pollute a tool call.
	assertShellArgs(t, shell.DeriveExecArgs("Get-Location", false),
		[]string{"pwsh", "-NoProfile", "-NonInteractive", "-Command", "Get-Location"})

	// Login shells keep profile semantics (no -NoProfile), so only the
	// long-script transport is hardened there.
	assertShellArgs(t, shell.DeriveExecArgs("Get-Location", true),
		[]string{"pwsh", "-Command", "Get-Location"})
}

func TestDeriveExecArgsEncodesLongPowerShellScripts(t *testing.T) {
	shell := Shell{Path: "powershell.exe", Type: ShellTypePowerShell}
	script := strings.Repeat("Write-Output 'x'; ", 500)
	if len(script) <= PowerShellEncodedCommandThreshold {
		t.Fatalf("fixture must exceed the encoded-command threshold, got %d chars", len(script))
	}

	args := shell.DeriveExecArgs(script, false)
	if len(args) != 5 {
		t.Fatalf("unexpected argv: %#v", args)
	}
	if args[0] != "powershell.exe" || args[1] != "-NoProfile" || args[2] != "-NonInteractive" || args[3] != "-EncodedCommand" {
		t.Fatalf("long scripts must use the hardened encoded transport, got %#v", args)
	}
	if !ShellArgsUseEncodedCommand(args) {
		t.Fatalf("ShellArgsUseEncodedCommand must detect the transport: %#v", args)
	}
	if decoded := decodePowerShellEncoded(t, args[4]); decoded != PowerShellCommandPrefix+script {
		t.Fatalf("encoded payload must round-trip the script with the UTF-8 prefix, got %q", decoded)
	}

	// The same script through a login shell also avoids the raw command line.
	loginArgs := shell.DeriveExecArgs(script, true)
	if !ShellArgsUseEncodedCommand(loginArgs) {
		t.Fatalf("long login-shell scripts must be encoded too, got %#v", loginArgs)
	}

	// Short scripts stay on -Command so they remain readable in logs.
	shortArgs := shell.DeriveExecArgs("Get-Location", false)
	if ShellArgsUseEncodedCommand(shortArgs) {
		t.Fatalf("short scripts must keep -Command, got %#v", shortArgs)
	}
	if shortArgs[len(shortArgs)-2] != "-Command" {
		t.Fatalf("short scripts must keep -Command, got %#v", shortArgs)
	}
}

func TestDeriveExecArgsKeepsUnixShellsAndCmd(t *testing.T) {
	bash := Shell{Path: "/bin/bash", Type: ShellTypeBash}
	assertShellArgs(t, bash.DeriveExecArgs("ls", false), []string{"/bin/bash", "-c", "ls"})
	assertShellArgs(t, bash.DeriveExecArgs("ls", true), []string{"/bin/bash", "-lc", "ls"})

	cmdShell := Shell{Path: "cmd", Type: ShellTypeCmd}
	assertShellArgs(t, cmdShell.DeriveExecArgs("dir", false), []string{"cmd", "/c", "dir"})
	if ShellArgsUseEncodedCommand(cmdShell.DeriveExecArgs("dir", false)) {
		t.Fatal("cmd argv must never look like an encoded PowerShell command")
	}
}
