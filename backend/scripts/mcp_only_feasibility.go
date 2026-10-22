//go:build ignore

// Command mcp_only_feasibility tests whether an MCP-only tool surface can be
// produced from the existing pieces, WITHOUT touching the chat wiring.
//
// The question it answers: can `--mcp-only <name>` be implemented by (a) loading
// the normal MCP config, (b) filtering it to one server, (c) building a manager
// over that snapshot, and (d) constructing a tools.Manager with no local toolkit?
//
// Run from backend/:
//
//	go run ./scripts/mcp_only_feasibility.go -config <mcp.yaml> -server <name>
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	mcpconfig "github.com/wwsheng009/ai-agent-runtime/internal/mcp/config"
	mcpmanager "github.com/wwsheng009/ai-agent-runtime/internal/mcp/manager"
	runtimetools "github.com/wwsheng009/ai-agent-runtime/internal/tools"
)

func main() {
	configPath := flag.String("config", "", "mcp.yaml holding the server")
	serverName := flag.String("server", "", "the single server to keep")
	flag.Parse()

	if *configPath == "" || *serverName == "" {
		fmt.Println("usage: go run ./scripts/mcp_only_feasibility.go -config <mcp.yaml> -server <name>")
		os.Exit(2)
	}

	resolved, err := mcpconfig.LoadEffectiveFrom("", *configPath)
	if err != nil {
		fmt.Printf("FAIL load config: %v\n", err)
		os.Exit(1)
	}
	loaded := resolved.Config

	// Step 1: can the config be filtered to one server without breaking it?
	kept, ok := loaded.MCPServers[*serverName]
	if !ok {
		names := make([]string, 0, len(loaded.MCPServers))
		for name := range loaded.MCPServers {
			names = append(names, name)
		}
		sort.Strings(names)
		fmt.Printf("FAIL server %q not found; available: %v\n", *serverName, names)
		os.Exit(1)
	}
	fmt.Printf("PASS config loaded, server %q present (type=%s)\n", *serverName, kept.Type)

	filtered := &mcpconfig.Config{MCPServers: map[string]mcpconfig.MCPConfig{*serverName: kept}}
	fmt.Printf("PASS filtered to %d server(s)\n", len(filtered.MCPServers))

	// Step 2: can a manager be built over the in-memory snapshot and started?
	mgr := mcpmanager.NewManager()
	scoped, ok := mgr.(mcpmanager.ScopedManager)
	if !ok {
		fmt.Println("FAIL manager does not implement ScopedManager; cannot load an in-memory snapshot")
		os.Exit(1)
	}
	if err := scoped.LoadConfigFromConfig(filtered); err != nil {
		fmt.Printf("FAIL LoadConfigFromConfig: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS in-memory snapshot accepted")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	go func() { _ = mgr.Start(ctx) }()

	deadline := time.Now().Add(20 * time.Second)
	var tools []*struct{ Name string }
	_ = tools
	for time.Now().Before(deadline) {
		if len(mgr.ListTools()) > 0 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	infos := mgr.ListTools()
	if len(infos) == 0 {
		fmt.Println("FAIL no tools appeared within 20s")
		mgr.Stop()
		os.Exit(1)
	}
	fmt.Printf("PASS %d tool(s) connected\n", len(infos))
	for _, info := range infos {
		fmt.Printf("   %s/%s\n", info.MCPName, info.Tool.Name)
	}

	// Step 3: does a toolkit-less tools.Manager expose ONLY those tools?
	tm := runtimetools.NewRemoteOnlyManager(mgr)
	listed := tm.ListTools()
	fmt.Printf("PASS remote-only tools.Manager lists %d tool(s):\n", len(listed))
	for _, d := range listed {
		identity, _ := d.Metadata["mcp_name"].(string)
		fmt.Printf("   %-32s mcp=%q\n", d.Name, identity)
	}

	// Step 4: confirm nothing local leaked in.
	local := 0
	for _, d := range listed {
		if _, hasMCP := d.Metadata["mcp_name"]; !hasMCP {
			local++
			fmt.Printf("   LOCAL LEAK: %s\n", d.Name)
		}
	}
	_ = mgr.Stop()
	if local > 0 {
		fmt.Printf("FAIL %d non-MCP tool(s) leaked into the exclusive surface\n", local)
		os.Exit(1)
	}
	fmt.Println("PASS no local tools in the exclusive surface")
	fmt.Println("FEASIBLE")
}
