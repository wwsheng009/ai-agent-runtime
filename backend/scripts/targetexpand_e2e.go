//go:build ignore

// Command targetexpand_e2e loads a real remoteTargets file and prints the expanded
// MCP servers and per-target policies.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetargets"
)

func main() {
	path := flag.String("file", "", "remoteTargets YAML")
	flag.Parse()

	expanded, err := remotetargets.LoadFile(*path)
	if err != nil {
		fmt.Printf("FAIL load: %v\n", err)
		os.Exit(1)
	}
	report := map[string]interface{}{
		"servers":        expanded.Servers,
		"policies":       expanded.Policies,
		"subagentAccess": expanded.SubagentAccess,
		"delegatedRead":  expanded.SubagentPoliciesForRole("researcher"),
		"delegatedImpl":  expanded.SubagentPoliciesForRole("implementer"),
		"warnings":       expanded.Warnings,
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	fmt.Println(string(encoded))
}
