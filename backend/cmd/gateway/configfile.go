package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// loadFlagConfigFile reads the optional gateway flagfile: a YAML document whose
// keys mirror the flag names (with or without the leading dashes) and whose
// values are rendered into the string form FlagSet.Set expects. Unknown keys,
// the meta keys (config/help) and values that cannot render as scalars are hard
// errors — a typo must never silently start the gateway with default options.
func loadFlagConfigFile(path string) ([][2]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc map[string]interface{}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	out := make([][2]string, 0, len(doc))
	for key, raw := range doc {
		name := strings.TrimLeft(strings.TrimSpace(key), "-")
		switch name {
		case "", "config", "help":
			return nil, fmt.Errorf("%s: key %q is not a settable flag", path, key)
		}
		value, err := configValueString(name, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, [2]string{name, value})
	}
	return out, nil
}

// configValueString renders raw as the string form FlagSet.Set accepts. YAML
// gives us bool/int/float/string natively; durations arrive as strings ("90s")
// or as bare numbers (rejected with a hint — Go duration parsing treats "90" as
// nanoseconds, so guessing units would be worse than an explicit error).
func configValueString(name string, raw interface{}) (string, error) {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", fmt.Errorf("key %q: empty value", name)
		}
		return s, nil
	case bool:
		return strconv.FormatBool(v), nil
	case int:
		return strconv.Itoa(v), nil
	case float64:
		if v != float64(int(v)) {
			return "", fmt.Errorf("key %q: want an integer, got %v", name, v)
		}
		return strconv.Itoa(int(v)), nil
	case []interface{}:
		return "", fmt.Errorf("key %q: lists are not supported (flags are scalars)", name)
	default:
		return "", fmt.Errorf("key %q: unsupported value type %T", name, raw)
	}
}

// applyFlagConfig applies every file entry via fs.Set unless the user passed
// that flag explicitly on the command line. Unknown names surface as Set errors.
func applyFlagConfig(fs *flag.FlagSet, entries [][2]string, explicit map[string]bool) error {
	for _, kv := range entries {
		if explicit[kv[0]] {
			continue // command line wins
		}
		if fs.Lookup(kv[0]) == nil {
			return fmt.Errorf("unknown flag %q in config file", kv[0])
		}
		if err := fs.Set(kv[0], kv[1]); err != nil {
			return fmt.Errorf("flag %s=%s: %w", kv[0], kv[1], err)
		}
	}
	return nil
}
