//go:build darwin

package remotetools

import (
	"os/exec"
	"strings"
)

func machineIdentity() string {
	out, err := exec.Command("sysctl", "-n", "kern.uuid").Output()
	if err == nil {
		if value := strings.TrimSpace(string(out)); value != "" {
			return value
		}
	}
	return ""
}
