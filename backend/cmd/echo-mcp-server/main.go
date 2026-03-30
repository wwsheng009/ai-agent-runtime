package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/wwsheng009/ai-agent-runtime/internal/mcp/server/echo"
)

var (
	addr = flag.String("addr", "localhost:8080", "WebSocket 服务地址")
)

func main() {
	flag.Parse()

	server := echo.NewEchoServer(*addr)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	serverErr := make(chan error, 1)
	go func() {
		if err := server.Start(); err != nil && err != http.ErrServerClosed {
			serverErr <- err
		}
	}()

	log.Println("Echo MCP Server is running on ws://" + *addr + "/mcp")
	log.Println("Press Ctrl+C to stop")

	select {
	case <-sigCh:
		log.Println("Shutting down server...")
	case err := <-serverErr:
		log.Printf("Server error: %v", err)
		os.Exit(1)
	}

	if err := server.Stop(); err != nil {
		log.Printf("Stop error: %v", err)
	}

	log.Println("Server stopped")
}
