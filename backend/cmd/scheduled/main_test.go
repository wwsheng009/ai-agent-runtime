package main

import (
	"flag"
	"testing"
)

// TestParseCommandFlagsAcceptsBothOrders 钉住 2026-10-14 的修复：子命令与
// flags 的两种顺序必须等价，且 flag 值不得被静默丢弃（事故：`run --store X`
// 落回默认 store）。
func TestParseCommandFlagsAcceptsBothOrders(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		command string
		store   string
	}{
		{name: "command-first", args: []string{"run", "--store", `C:\tmp\s.sqlite`}, command: "run", store: `C:\tmp\s.sqlite`},
		{name: "flags-first", args: []string{"--store", `C:\tmp\s.sqlite`, "run"}, command: "run", store: `C:\tmp\s.sqlite`},
		{name: "doctor-command-first", args: []string{"doctor", "--store", "x"}, command: "doctor", store: "x"},
		{name: "no-command", args: []string{"--store", "x"}, command: "", store: "x"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("scheduled", flag.ContinueOnError)
			store := fs.String("store", "", "")
			command, err := parseCommandFlags(fs, tc.args)
			if err != nil {
				t.Fatalf("parseCommandFlags: %v", err)
			}
			if command != tc.command {
				t.Fatalf("command = %q, want %q", command, tc.command)
			}
			if *store != tc.store {
				t.Fatalf("store = %q, want %q", *store, tc.store)
			}
		})
	}
}

func TestParseCommandFlagsRejectsUnexpectedArgs(t *testing.T) {
	fs := flag.NewFlagSet("scheduled", flag.ContinueOnError)
	if _, err := parseCommandFlags(fs, []string{"run", "extra"}); err == nil {
		t.Fatal("extra positional argument must fail instead of being ignored")
	}
}
