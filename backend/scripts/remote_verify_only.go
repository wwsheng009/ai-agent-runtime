//go:build ignore

// Command remote_verify_only runs a verification script on the target machine
// without starting a chat session. It exists so the assertion can be re-run (and
// debugged) after the model's turn has already ended, without asking the model to
// do the work again.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	scriptPath := flag.String("verify", "", "verification script executed on the target")
	flag.Parse()

	raw, err := os.ReadFile(*scriptPath)
	if err != nil {
		fmt.Printf("FAIL read verify script: %v\n", err)
		os.Exit(2)
	}

	cmd := exec.Command("wsl", "-d", "Ubuntu", "--", "bash", "-s")
	cmd.Stdin = strings.NewReader(string(raw))
	cmd.Env = os.Environ()
	out, err := cmd.CombinedOutput()
	fmt.Println(string(out))
	if err != nil {
		fmt.Printf("VERIFY_EXIT: %v\n", err)
		os.Exit(1)
	}
	if !strings.Contains(string(out), "VERIFY_OK") {
		fmt.Println("VERIFY_EXIT: script ran but did not report VERIFY_OK")
		os.Exit(1)
	}
	fmt.Println("VERIFY_EXIT: OK")
}
