// Command aicli-mesh is the aicli node mesh CLI: discovery, inspection and
// operations for the local multi-process mesh (architecture §7).
//
// It is deliberately independent of any running aicli process — every command
// reads the mesh directory directly, which is why `ls` / `show` / `gc` /
// `doctor` keep working when nothing is alive.
//
// It is *not* a node: it never writes a record, a binding or a lease. The only
// command that mutates anything is `gc --apply`, and it only deletes leftovers
// whose process is provably gone (rule R3).
//
// The binary is a thin wrapper: argument parsing, rendering and exit codes live
// in internal/mesh/cli.go so they are covered by unit tests (see cli_test.go).
package main

import (
	"os"

	"github.com/wwsheng009/ai-agent-runtime/internal/mesh"
)

// version is injected by scripts/build.ps1 (-X main.version=<v>; the tool is
// registered with LdflagsKind "main-version").
var version = mesh.CLIVersion

func main() {
	cli := &mesh.CLI{Stdout: os.Stdout, Stderr: os.Stderr, Version: version}
	os.Exit(cli.Run(os.Args[1:]))
}
