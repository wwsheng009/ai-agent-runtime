//go:build ignore

// Command pflag_probe replicates ssh-client's flag parsing to show exactly what
// reaches flags.command for a given argv. It exists because the ssh-stdio
// failure ("/bin/sh: Illegal option --") is either a product quoting bug or a
// test-harness mistake, and the only way to tell is to look at the parsed args.
package main

import (
	"fmt"
	"os"

	"github.com/spf13/pflag"
)

func main() {
	fs := pflag.NewFlagSet("probe", pflag.ContinueOnError)
	// ssh-client calls SetInterspersed(false) so that a remote command's own
	// flags are not parsed locally. Reproduce that here to see what it does to a
	// `--` separator.
	interspersed := false
	for _, arg := range os.Args[1:] {
		if arg == "-interspersed" {
			interspersed = true
		}
	}
	if !interspersed {
		fs.SetInterspersed(false)
	}
	var stdio bool
	var password string
	var port int
	fs.BoolVar(&stdio, "stdio", false, "")
	fs.StringVar(&password, "password", "", "")
	fs.IntVarP(&port, "port", "p", 0, "")
	fs.BoolP("no-tty", "T", false, "")
	fs.StringArrayP("option", "o", nil, "")

	if err := fs.Parse(os.Args[1:]); err != nil {
		fmt.Println("parse error:", err)
		return
	}
	args := fs.Args()
	fmt.Printf("args (%d):\n", len(args))
	for i, a := range args {
		fmt.Printf("  [%d] %q\n", i, a)
	}
	if len(args) > 1 {
		fmt.Printf("command = args[1:] = %q\n", args[1:])
	}
}
