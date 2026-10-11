package main

import (
	"flag"
	"fmt"
)

// flagSet wraps flag.CommandLine.Set so tests and callers share one error text.
func flagSet(name, value string) error {
	if f := flag.CommandLine.Lookup(name); f == nil {
		return fmt.Errorf("unknown flag %q in config file", name)
	}
	return flag.CommandLine.Set(name, value)
}
