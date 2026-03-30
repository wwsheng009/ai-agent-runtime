package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit"
	"github.com/wwsheng009/ai-agent-runtime/internal/toolkit/tools"
)

func main() {
	toolsFlag := flag.String("tools", "", "要暴露的工具（逗号分隔，为空则暴露全部）")
	excludeFlag := flag.String("exclude", "", "要排除的工具（逗号分隔）")
	flag.Parse()

	registry := toolkit.NewRegistry()
	allTools := []toolkit.Tool{
		tools.NewBashTool(),
		tools.NewViewTool(),
		tools.NewEditTool(),
		tools.NewWriteTool(),
		tools.NewGlobTool(),
		tools.NewGrepTool(),
		tools.NewLsTool(),
		tools.NewDownloadTool(),
		tools.NewFetchTool(),
		tools.NewMultieditTool(),
		tools.NewTodosTool(),
		tools.NewSourcegraphTool(),
		tools.NewWebSearchTool(),
	}

	includeTools := parseList(*toolsFlag)
	excludeTools := parseList(*excludeFlag)

	registeredCount := 0
	for _, t := range allTools {
		name := t.Name()
		if contains(excludeTools, name) {
			continue
		}
		if len(includeTools) > 0 && !contains(includeTools, name) {
			continue
		}

		_ = registry.Register(t)
		registeredCount++
	}

	mcpServer := toolkit.NewToolkitMCPServer(registry)

	fmt.Fprintln(os.Stderr, "========================================")
	fmt.Fprintln(os.Stderr, "  Toolkit MCP Server")
	fmt.Fprintln(os.Stderr, "========================================")
	fmt.Fprintf(os.Stderr, "已注册工具: %d 个\n", registeredCount)
	fmt.Fprintln(os.Stderr, "工具列表:")
	for _, name := range mcpServer.ListExposedTools() {
		fmt.Fprintf(os.Stderr, "  - %s\n", name)
	}
	fmt.Fprintln(os.Stderr, "========================================")
	fmt.Fprintln(os.Stderr, "等待客户端连接...")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		if err := mcpServer.Start(ctx); err != nil {
			log.Fatalf("服务器启动失败: %v", err)
		}
	}()

	<-sigChan
	fmt.Fprintln(os.Stderr, "\n收到退出信号，正在关闭服务器...")

	if err := mcpServer.Stop(); err != nil {
		log.Printf("服务器停止失败: %v", err)
	}

	fmt.Fprintln(os.Stderr, "服务器已关闭")
}

func parseList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
