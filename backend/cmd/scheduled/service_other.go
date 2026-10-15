//go:build !windows

// 非 Windows（systemd/launchd）平台：服务模式返回不支持，安装走 systemd（D2 后续）。
package main

import "fmt"

const ServiceName = "ai-agent-runtime-scheduled"

type serviceScheduleRunner func(stop <-chan struct{})

var errNotService = fmt.Errorf("service mode is only supported on windows in this build")

func runAsWindowsService(run serviceScheduleRunner) error {
	_ = run
	return errNotService
}

func installWindowsService(executable, configPath string) error {
	_ = executable
	_ = configPath
	return fmt.Errorf("windows service install is not supported on this platform; use systemd (see docs/automation-tasks/scheduler-daemon-design.md)")
}

func uninstallWindowsService() error {
	return fmt.Errorf("windows service uninstall is not supported on this platform")
}

func controlWindowsService(action string) (string, error) {
	return "", fmt.Errorf("windows service control is not supported on this platform")
}
