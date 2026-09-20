//go:build windows && !win7compat

package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// stillActive 是 GetExitCodeProcess 对「仍在运行」的进程返回的退出码。
const stillActive = 259

// processAlive 判断 PID 是否仍然存活（Windows 无僵尸进程：进程退出即不可查询）。
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var code uint32
	if err := windows.GetExitCodeProcess(handle, &code); err != nil {
		return false
	}
	return code == stillActive
}

// childPIDs 用 Toolhelp 快照枚举 parent 的直接子进程，作为**独立于 Job Object
// 记账**的旁证：即便守卫本身没拿到 job，也能判定「进程树是否真的消失」。
func childPIDs(parent int) []int {
	if parent <= 0 {
		return nil
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snapshot)

	var entry windows.ProcessEntry32
	entry.Size = uint32(unsafe.Sizeof(entry))
	var out []int
	for err := windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if int(entry.ParentProcessID) == parent && int(entry.ProcessID) != parent {
			out = append(out, int(entry.ProcessID))
		}
	}
	return out
}

// stdioTreeReport 是父进程强杀测试里 helper 进程写出的进程树快照。
type stdioTreeReport struct {
	Root        int    `json:"root_pid"`
	Descendants []int  `json:"descendant_pids"`
	AttachError string `json:"attach_error,omitempty"`
	Error       string `json:"error,omitempty"`
}

func writeTreeReport(path string, report stdioTreeReport) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func readTreeReport(path string) (stdioTreeReport, error) {
	var report stdioTreeReport
	data, err := os.ReadFile(path)
	if err != nil {
		return report, err
	}
	err = json.Unmarshal(data, &report)
	return report, err
}

// TestMCPStdioTreeHostHelper 不是独立测试：它作为「agent 进程」被
// TestStdioTransportParentKillReapsTree 拉起，建立一个真实 stdio MCP 进程树
// （cmd.exe → ping.exe），把 PID 快照写进报告文件后阻塞，等待被强杀。
func TestMCPStdioTreeHostHelper(t *testing.T) {
	if os.Getenv("MCP_TREE_HOST_HELPER") != "1" {
		t.Skip("仅作为父进程强杀测试的 helper 进程运行")
	}
	reportPath := strings.TrimSpace(os.Getenv("MCP_TREE_REPORT"))
	if reportPath == "" {
		t.Fatal("helper 缺少 MCP_TREE_REPORT")
	}

	tr := NewStdioTransport(&Config{
		Type:    "stdio",
		Command: "cmd.exe",
		Args:    []string{"/c", "ping", "-n", "600", "127.0.0.1"},
	})
	ctx := context.Background()
	sdkTransport := tr.ToMCPSdkTransport(ctx)
	if _, err := sdkTransport.Connect(ctx); err != nil {
		_ = writeTreeReport(reportPath, stdioTreeReport{Error: err.Error()})
		t.Fatalf("helper Connect 失败: %v", err)
	}

	deadline := time.Now().Add(20 * time.Second)
	root := tr.stdioProcessPID()
	var descendants []int
	for time.Now().Before(deadline) {
		if pid := tr.stdioProcessPID(); pid > 0 {
			root = pid
		}
		descendants = childPIDs(root)
		if root > 0 && len(descendants) > 0 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	report := stdioTreeReport{
		Root:        root,
		Descendants: descendants,
		AttachError: tr.treeGuardReport().AttachErr,
	}
	if err := writeTreeReport(reportPath, report); err != nil {
		t.Fatalf("helper 写报告失败: %v", err)
	}

	// 阻塞：模拟一个正在运行的 agent 进程，直到父测试把它强杀。
	for {
		time.Sleep(time.Second)
	}
}

// TestStdioTransportParentKillReapsTree 覆盖计划 §6.2 验收标准 10 / §2.5 Z10：
// 父进程（agent）被强杀且**不发** session/close 时，stdio MCP 子进程树必须随
// Job Object（KILL_ON_JOB_CLOSE）一起消失，不留下孤儿进程。
func TestStdioTransportParentKillReapsTree(t *testing.T) {
	reportPath := filepath.Join(t.TempDir(), "tree-report.json")

	helper := exec.Command(os.Args[0], "-test.run=^TestMCPStdioTreeHostHelper$", "-test.v=false")
	helper.Env = append(os.Environ(),
		"MCP_TREE_HOST_HELPER=1",
		"MCP_TREE_REPORT="+reportPath,
	)
	var output bytes.Buffer
	helper.Stdout = &output
	helper.Stderr = &output
	if err := helper.Start(); err != nil {
		t.Fatalf("启动 helper 失败: %v", err)
	}

	helperDone := make(chan error, 1)
	go func() { helperDone <- helper.Wait() }()

	var report stdioTreeReport
	deadline := time.Now().Add(30 * time.Second)
	for {
		if parsed, err := readTreeReport(reportPath); err == nil {
			report = parsed
			break
		}
		if time.Now().After(deadline) {
			_ = helper.Process.Kill()
			t.Fatalf("helper 未在超时内写出进程树报告；helper 输出:\n%s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	cleanupPIDs := func() {
		// 任何失败路径都不得留下 helper 进程（否则它会连带留下一棵树）。
		if helper.Process != nil {
			_ = helper.Process.Kill()
		}
		for _, pid := range append([]int{report.Root}, report.Descendants...) {
			if processAlive(pid) {
				_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
			}
		}
	}
	t.Cleanup(cleanupPIDs)

	if report.Error != "" {
		t.Fatalf("helper 报告错误: %s", report.Error)
	}
	if report.AttachError != "" {
		t.Fatalf("Job Object 绑定失败（父死子亡不成立）: %s", report.AttachError)
	}
	if report.Root <= 0 {
		t.Fatalf("helper 未记录根进程 PID: %+v", report)
	}
	if len(report.Descendants) == 0 {
		t.Fatalf("helper 未观察到后代进程（无法验证树回收）: %+v", report)
	}
	if !processAlive(report.Root) {
		t.Fatalf("强杀前根进程 %d 已不存在", report.Root)
	}

	// 模拟客户端 Drop → child.kill()：强杀 agent 进程，不发任何协议关闭。
	if err := helper.Process.Kill(); err != nil {
		t.Fatalf("强杀 helper 失败: %v", err)
	}
	select {
	case <-helperDone:
	case <-time.After(10 * time.Second):
	}

	waitGone := func() bool {
		if processAlive(report.Root) {
			return false
		}
		for _, pid := range report.Descendants {
			if processAlive(pid) {
				return false
			}
		}
		return true
	}
	goneDeadline := time.Now().Add(20 * time.Second)
	for !waitGone() {
		if time.Now().After(goneDeadline) {
			alive := []int{}
			if processAlive(report.Root) {
				alive = append(alive, report.Root)
			}
			for _, pid := range report.Descendants {
				if processAlive(pid) {
					alive = append(alive, pid)
				}
			}
			t.Fatalf("父进程被强杀后仍有 stdio 子进程残留: %v（报告=%+v）", alive, report)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
