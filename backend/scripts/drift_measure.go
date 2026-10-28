//go:build ignore

// drift_measure prints the current guard counts so the ratchet baselines in
// internal/driftguard can be set from a measurement instead of from memory.
package main

import (
	"fmt"
	"path/filepath"

	"github.com/wwsheng009/ai-agent-runtime/internal/driftguard"
)

func main() {
	cores := []string{"viewcore", "lscore", "searchcore", "mutatecore"}
	for _, core := range cores {
		root := filepath.Join("internal", "toolcore", core)
		total, sites, err := driftguard.FilesystemCallCounts(root)
		if err != nil {
			fmt.Printf("%-12s ERROR %v\n", core, err)
			continue
		}
		fmt.Printf("FS_CALLS %-12s %4d\n", core, total)
		for _, site := range sites {
			fmt.Printf("      %s\n", site)
		}
	}
	for _, core := range cores {
		root := filepath.Join("internal", "toolcore", core)
		literals, err := driftguard.BackendNameLiterals(root, "envd", "ssh", "http", "local")
		if err != nil {
			fmt.Printf("LITERALS %-12s ERROR %v\n", core, err)
			continue
		}
		fmt.Printf("LITERALS %-12s %4d %v\n", core, len(literals), literals)
		calls, callSites, err := driftguard.CapabilityCallCounts(root, "Capabilities")
		if err != nil {
			fmt.Printf("CAPCALLS %-12s ERROR %v\n", core, err)
			continue
		}
		fmt.Printf("CAPCALLS %-12s %4d %v\n", core, calls, callSites)
	}
}
