//go:build !windows

package commands

import "context"

func readSystemConsoleLine(context.Context) (string, bool, error) {
	return "", false, nil
}
