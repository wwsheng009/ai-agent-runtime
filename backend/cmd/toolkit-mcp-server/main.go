package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctl"
)

func main() {
	toolsFlag := flag.String("tools", "", "要暴露的工具（逗号分隔，为空则暴露全部）")
	excludeFlag := flag.String("exclude", "", "要排除的工具（逗号分隔）")
	flag.Parse()

	// Compatibility wrapper: the tool surface, profile filtering and MCP
	// transport now live in toolctl. Keep the old flags working, but do not
	// maintain a second registry or a second stdio server loop.
	args := []string{"serve", "--mcp", "stdio", "--profile", "ops", "--allow-write", "--allow-command"}
	if list := normalizeList(*toolsFlag); list != "" {
		args = append(args, "--tools", list)
	}
	if list := normalizeList(*excludeFlag); list != "" {
		args = append(args, "--exclude-tools", list)
	}
	cli := &toolctl.CLI{Stdout: os.Stdout, Stderr: os.Stderr}
	if code := cli.Run(args); code != toolctl.ExitOK {
		fmt.Fprintf(os.Stderr, "toolkit-mcp-server: exited with %d\n", code)
		os.Exit(code)
	}
}

func normalizeList(raw string) string {
	parts := strings.Split(raw, ",")
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			kept = append(kept, trimmed)
		}
	}
	return strings.Join(kept, ",")
}
