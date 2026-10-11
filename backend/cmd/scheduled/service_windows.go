//go:build windows

// Windows Service 集成（D2）：以 SCM 服务方式运行调度守护进程，
// 并提供 install/uninstall/start/stop。
package main

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// ServiceName 是 Windows 服务名。
const ServiceName = "ai-agent-runtime-scheduled"

// serviceScheduleRunner 抽象可启动/停止的调度运行（供服务循环调用）。
type serviceScheduleRunner func(stop <-chan struct{})

// runAsWindowsService 在 SCM 控制下运行；isService 表示是否由 SCM 启动。
func runAsWindowsService(run serviceScheduleRunner) error {
	isService, err := svc.IsWindowsService()
	if err != nil {
		return fmt.Errorf("check windows service mode: %w", err)
	}
	if !isService {
		return errNotService
	}
	return svc.Run(ServiceName, &scheduleService{run: run})
}

// errNotService 表示当前不是服务上下文。
var errNotService = fmt.Errorf("not running as a windows service")

type scheduleService struct {
	run serviceScheduleRunner
}

func (s *scheduleService) Execute(_ []string, requests <-chan svc.ChangeRequest, status chan<- svc.Status) (bool, uint32) {
	status <- svc.Status{State: svc.StartPending}
	stop := make(chan struct{})
	go s.run(stop)
	status <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for request := range requests {
		switch request.Cmd {
		case svc.Interrogate:
			status <- request.CurrentStatus
		case svc.Stop, svc.Shutdown:
			status <- svc.Status{State: svc.StopPending}
			close(stop)
			time.Sleep(200 * time.Millisecond)
			return false, 0
		}
	}
	return false, 0
}

// installWindowsService 注册服务（需管理员），--start=auto。
func installWindowsService(executable string) error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (admin required): %w", err)
	}
	defer manager.Disconnect()
	if service, err := manager.OpenService(ServiceName); err == nil {
		_ = service.Close()
		return fmt.Errorf("service %s already exists", ServiceName)
	}
	service, err := manager.CreateService(ServiceName, executable, mgr.Config{
		DisplayName: "AI Agent Runtime Scheduled",
		Description: "Automation task (Schedule) scheduler daemon: delivers due reminders to their original sessions.",
		StartType:   mgr.StartAutomatic,
	})
	if err != nil {
		return fmt.Errorf("create service: %w", err)
	}
	defer service.Close()
	if err := service.Start(); err != nil {
		return fmt.Errorf("start service: %w", err)
	}
	return nil
}

// uninstallWindowsService 停止并删除服务（需管理员）。
func uninstallWindowsService() error {
	manager, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("connect to service manager (admin required): %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(ServiceName)
	if err != nil {
		return fmt.Errorf("open service %s: %w", ServiceName, err)
	}
	defer service.Close()
	if _, err := service.Control(svc.Stop); err != nil {
		// 未运行时停止失败是正常的（服务本来就是停止态）。
		_ = err
	}
	if err := service.Delete(); err != nil {
		return fmt.Errorf("delete service: %w", err)
	}
	return nil
}

// controlWindowsService 执行 start/stop/status。
func controlWindowsService(action string) (string, error) {
	manager, err := mgr.Connect()
	if err != nil {
		return "", fmt.Errorf("connect to service manager: %w", err)
	}
	defer manager.Disconnect()
	service, err := manager.OpenService(ServiceName)
	if err != nil {
		return "", fmt.Errorf("open service %s: %w", ServiceName, err)
	}
	defer service.Close()
	switch action {
	case "start":
		if err := service.Start(); err != nil {
			return "", fmt.Errorf("start service: %w", err)
		}
		return "started", nil
	case "stop":
		if _, err := service.Control(svc.Stop); err != nil {
			return "", fmt.Errorf("stop service: %w", err)
		}
		return "stop requested", nil
	case "status":
		state, err := service.Query()
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("state=%d pid=%d", state.State, state.ProcessId), nil
	default:
		return "", fmt.Errorf("unknown action %q", action)
	}
}

var _ = os.Exit
