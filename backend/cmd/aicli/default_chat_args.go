package main

import (
	"strconv"
	"strings"

	"github.com/spf13/pflag"
)

// prependDefaultChatCommand injects the chat subcommand when the user only
// supplies flags. This keeps `aicli` equivalent to `aicli chat` without
// changing explicit subcommands or help invocations.
func prependDefaultChatCommand(args []string, flags ...*pflag.FlagSet) []string {
	if len(args) == 0 {
		return []string{"chat"}
	}

	if containsHelpFlag(args) || hasPositionalArg(args, flags) {
		return args
	}

	return append([]string{"chat"}, args...)
}

func containsHelpFlag(args []string) bool {
	for _, arg := range args {
		switch {
		case arg == "-h":
			return true
		case arg == "--help":
			return true
		case strings.HasPrefix(arg, "--help="):
			return true
		}
	}
	return false
}

func hasPositionalArg(args []string, flags []*pflag.FlagSet) bool {
	if len(args) == 0 {
		return false
	}

	inFlagValue := false
	for i, arg := range args {
		if inFlagValue {
			inFlagValue = false
			continue
		}

		if arg == "--" {
			return i+1 < len(args)
		}

		if strings.HasPrefix(arg, "--") {
			if strings.Contains(arg, "=") {
				continue
			}
			if flag := lookupFlag(flags, strings.TrimPrefix(arg, "--"), false); flag != nil {
				if flag.NoOptDefVal == "" {
					inFlagValue = true
					continue
				}
				if isBoolFlagWithExplicitValue(flag, args, i) {
					inFlagValue = true
				}
			}
			continue
		}

		if strings.HasPrefix(arg, "-") {
			if len(arg) == 2 {
				if flag := lookupFlag(flags, string(arg[1]), true); flag != nil {
					if flag.NoOptDefVal == "" {
						inFlagValue = true
						continue
					}
					if isBoolFlagWithExplicitValue(flag, args, i) {
						inFlagValue = true
					}
				}
			}
			continue
		}

		return true
	}

	return false
}

func lookupFlag(flags []*pflag.FlagSet, name string, shorthand bool) *pflag.Flag {
	if strings.TrimSpace(name) == "" {
		return nil
	}

	for _, set := range flags {
		if set == nil {
			continue
		}

		var flag *pflag.Flag
		if shorthand {
			flag = set.ShorthandLookup(name)
		} else {
			flag = set.Lookup(name)
		}
		if flag != nil {
			return flag
		}
	}

	return nil
}

func isBoolFlagWithExplicitValue(flag *pflag.Flag, args []string, index int) bool {
	if flag == nil || flag.Value == nil || flag.Value.Type() != "bool" {
		return false
	}
	nextIndex := index + 1
	if nextIndex >= len(args) {
		return false
	}
	_, err := strconv.ParseBool(strings.TrimSpace(args[nextIndex]))
	return err == nil
}
