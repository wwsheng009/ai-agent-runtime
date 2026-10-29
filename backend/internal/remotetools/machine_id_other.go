//go:build !linux && !windows && !darwin

package remotetools

func machineIdentity() string {
	return ""
}
