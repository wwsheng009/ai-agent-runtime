package main

import (
	"os"

	"github.com/wwsheng009/ai-agent-runtime/internal/toolctl"
)

func main() {
	cli := &toolctl.CLI{Stdout: os.Stdout, Stderr: os.Stderr}
	os.Exit(cli.Run(os.Args[1:]))
}
