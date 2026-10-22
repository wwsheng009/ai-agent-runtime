//go:build windows

// remote-task-conpty 驱动一个真实的 ConPTY 会话：在其中启动 `aicli chat`，
// 让模型通过挂载的 remote MCP 目标（WSL2 上的 aicli-remote-tools）自主完成
// 一个代码修复任务。
//
// 与 goal-conpty-driver 的区别：那个驱动验证 /goal 状态机；这个驱动验证
// “模型在 PTY 里真正调用远程工具改动远端机器”，最终裁决取自**远端文件系统
// 与远端测试进程**，而不是模型的自述。
//
// ConPTY 管道代码与 cmd/goal-conpty-driver 保持一致（该实现已在本仓库的
// 交互式 E2E 中验证过），避免出现第二套未验证的 PTY 绑定。
//
// 用法：
//
//	go run ./scripts/remote_task_conpty.go \
//	  -exe ..\.tmp\aicli.exe -workdir ..\.tmp\scenario\ws \
//	  -session-dir ..\.tmp\scenario\sessions -log-dir ..\.tmp\scenario\logs \
//	  -transcript ..\.tmp\scenario\transcript.txt -render ..\.tmp\scenario\render.log \
//	  -task ..\.tmp\scenario\TASK.md -provider magpie -model qoder/dfmodel
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modKernel32                        = windows.NewLazySystemDLL("kernel32.dll")
	fCreatePseudoConsole               = modKernel32.NewProc("CreatePseudoConsole")
	fClosePseudoConsole                = modKernel32.NewProc("ClosePseudoConsole")
	fInitializeProcThreadAttributeList = modKernel32.NewProc("InitializeProcThreadAttributeList")
	fUpdateProcThreadAttribute         = modKernel32.NewProc("UpdateProcThreadAttribute")
)

const (
	_STILL_ACTIVE                        uint32  = 259
	_S_OK                                uintptr = 0
	_PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE uintptr = 0x20016
	sinkWindowBytes                              = 1 << 20
)

type _COORD struct{ X, Y int16 }

func (c *_COORD) Pack() uintptr { return uintptr((int32(c.Y) << 16) | int32(c.X)) }

type _HPCON windows.Handle

type handleIO struct{ handle windows.Handle }

func (h *handleIO) Read(p []byte) (int, error) {
	var numRead uint32
	err := windows.ReadFile(h.handle, p, &numRead, nil)
	return int(numRead), err
}

func (h *handleIO) Write(p []byte) (int, error) {
	var numWritten uint32
	err := windows.WriteFile(h.handle, p, &numWritten, nil)
	return int(numWritten), err
}

func (h *handleIO) Close() error { return windows.CloseHandle(h.handle) }

type conPty struct {
	hpc                          _HPCON
	pi                           *windows.ProcessInformation
	ptyIn, ptyOut, cmdIn, cmdOut *handleIO
	closed                       bool
}

func win32ClosePseudoConsole(hPc _HPCON) {
	if fClosePseudoConsole.Find() != nil {
		return
	}
	fClosePseudoConsole.Call(uintptr(hPc))
}

func win32CreatePseudoConsole(c *_COORD, hIn, hOut windows.Handle) (_HPCON, error) {
	if fCreatePseudoConsole.Find() != nil {
		return 0, fmt.Errorf("CreatePseudoConsole not found")
	}
	var hPc _HPCON
	ret, _, _ := fCreatePseudoConsole.Call(
		c.Pack(), uintptr(hIn), uintptr(hOut), 0, uintptr(unsafe.Pointer(&hPc)))
	if ret != _S_OK {
		return 0, fmt.Errorf("CreatePseudoConsole() failed with status 0x%x", ret)
	}
	return hPc, nil
}

type _StartupInfoEx struct {
	startupInfo   windows.StartupInfo
	attributeList []byte
}

func getStartupInfoExForPTY(hpc _HPCON) (*_StartupInfoEx, error) {
	if fInitializeProcThreadAttributeList.Find() != nil {
		return nil, fmt.Errorf("InitializeProcThreadAttributeList not found")
	}
	if fUpdateProcThreadAttribute.Find() != nil {
		return nil, fmt.Errorf("UpdateProcThreadAttribute not found")
	}
	var siEx _StartupInfoEx
	siEx.startupInfo.Cb = uint32(unsafe.Sizeof(windows.StartupInfo{}) + unsafe.Sizeof(&siEx.attributeList[0]))
	siEx.startupInfo.Flags |= windows.STARTF_USESTDHANDLES
	var size uintptr
	_, _, _ = fInitializeProcThreadAttributeList.Call(0, 1, 0, uintptr(unsafe.Pointer(&size)))
	siEx.attributeList = make([]byte, size, size)
	ret, _, err := fInitializeProcThreadAttributeList.Call(
		uintptr(unsafe.Pointer(&siEx.attributeList[0])), 1, 0, uintptr(unsafe.Pointer(&size)))
	if ret != 1 {
		return nil, fmt.Errorf("InitializeProcThreadAttributeList: %v", err)
	}
	ret, _, err = fUpdateProcThreadAttribute.Call(
		uintptr(unsafe.Pointer(&siEx.attributeList[0])), 0,
		_PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE, uintptr(hpc),
		unsafe.Sizeof(hpc), 0, 0)
	if ret != 1 {
		return nil, fmt.Errorf("UpdateProcThreadAttribute: %v", err)
	}
	return &siEx, nil
}

func createConsoleProcessAttachedToPTY(hpc _HPCON, commandLine, workDir string) (*windows.ProcessInformation, error) {
	cmdLine, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return nil, err
	}
	var currentDirectory *uint16
	if workDir != "" {
		currentDirectory, err = windows.UTF16PtrFromString(workDir)
		if err != nil {
			return nil, err
		}
	}
	siEx, err := getStartupInfoExForPTY(hpc)
	if err != nil {
		return nil, err
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		nil, cmdLine, nil, nil, false,
		uint32(windows.EXTENDED_STARTUPINFO_PRESENT), nil, currentDirectory,
		&siEx.startupInfo, &pi)
	if err != nil {
		return nil, err
	}
	return &pi, nil
}

func startConPty(commandLine, workDir string) (*conPty, error) {
	coords := _COORD{150, 45}
	var cmdIn, cmdOut, ptyIn, ptyOut windows.Handle
	if err := windows.CreatePipe(&ptyIn, &cmdIn, nil, 0); err != nil {
		return nil, fmt.Errorf("CreatePipe: %v", err)
	}
	if err := windows.CreatePipe(&cmdOut, &ptyOut, nil, 0); err != nil {
		return nil, fmt.Errorf("CreatePipe: %v", err)
	}
	hPc, err := win32CreatePseudoConsole(&coords, ptyIn, ptyOut)
	if err != nil {
		return nil, err
	}
	pi, err := createConsoleProcessAttachedToPTY(hPc, commandLine, workDir)
	if err != nil {
		win32ClosePseudoConsole(hPc)
		return nil, fmt.Errorf("Failed to create console process: %v", err)
	}
	return &conPty{
		hpc: hPc, pi: pi,
		ptyIn:  &handleIO{ptyIn},
		ptyOut: &handleIO{ptyOut},
		cmdIn:  &handleIO{cmdIn},
		cmdOut: &handleIO{cmdOut},
	}, nil
}

func (cpty *conPty) Read(p []byte) (int, error)  { return cpty.cmdOut.Read(p) }
func (cpty *conPty) Write(p []byte) (int, error) { return cpty.cmdIn.Write(p) }

func (cpty *conPty) Close() error {
	if cpty.closed {
		return nil
	}
	cpty.closed = true
	win32ClosePseudoConsole(cpty.hpc)
	var firstErr error
	for _, h := range []windows.Handle{
		cpty.pi.Process, cpty.pi.Thread,
		cpty.ptyIn.handle, cpty.ptyOut.handle, cpty.cmdIn.handle, cpty.cmdOut.handle,
	} {
		if h != windows.InvalidHandle {
			if err := windows.CloseHandle(h); err == nil {
				continue
			} else if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// sink 是 PTY 输出的滚动窗口。
type sink struct {
	mu   sync.Mutex
	ring []byte
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ring = append(s.ring, p...)
	if len(s.ring) > sinkWindowBytes {
		s.ring = append([]byte(nil), s.ring[len(s.ring)-sinkWindowBytes:]...)
	}
	return len(p), nil
}

func (s *sink) snapshot() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return string(s.ring)
}

func main() {
	exe := flag.String("exe", `..\.tmp\aicli.exe`, "aicli executable")
	workDir := flag.String("workdir", "", "child working directory holding .aicli/mcp.yaml")
	provider := flag.String("provider", "magpie", "provider")
	model := flag.String("model", "qoder/dfmodel", "model")
	reasoning := flag.String("reasoning-effort", "", "reasoning effort (empty = default)")
	sessionDir := flag.String("session-dir", "", "session dir")
	logDir := flag.String("log-dir", "", "log dir")
	transcript := flag.String("transcript", "", "transcript file")
	render := flag.String("render", "", "render mirror")
	taskFile := flag.String("task", "", "task markdown handed to the model")
	verifyScript := flag.String("verify", "", "remote bash script (run on the target) that exits 0 when the task succeeded")
	idle := flag.Duration("idle", 45*time.Second, "idle window that ends the turn")
	budget := flag.Duration("timeout", 15*time.Minute, "overall budget")
	envPairs := flag.String("env", "", "comma-separated KEY=VALUE exported into the child (e.g. the remote bearer token)")
	permissionMode := flag.String("permission-mode", "accept_edits", "chat permission mode (default|accept_edits|plan|bypass_permissions|dont_ask)")
	autoApprove := flag.Bool("auto-approve", true, "answer the interactive approval prompt with yes")
	allowTools := flag.String("allow-tool", "", "comma-separated pre-approved tool patterns (passed as repeated --allow-tool)")
	approvalReuse := flag.String("approval-reuse", "", "local actor approval reuse policy (off|session_readonly_shell|team_readonly_shell)")
	flag.Parse()

	if *workDir == "" || *sessionDir == "" || *logDir == "" || *transcript == "" || *render == "" || *taskFile == "" {
		fmt.Println("missing required flags: -workdir/-session-dir/-log-dir/-transcript/-render/-task")
		os.Exit(2)
	}
	// The MCP entry resolves the bearer token through ${AICLI_REMOTE_TOKEN}; a
	// child that does not inherit it fails env expansion and the server is
	// skipped silently, which looks like "the model ignored the remote tools".
	for _, pair := range strings.Split(*envPairs, ",") {
		pair = strings.TrimSpace(pair)
		if pair == "" {
			continue
		}
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name == "" {
			fmt.Printf("[driver] bad -env entry %q (want KEY=VALUE)\n", pair)
			os.Exit(2)
		}
		if err := os.Setenv(strings.TrimSpace(name), value); err != nil {
			fmt.Printf("[driver] setenv %s: %v\n", name, err)
			os.Exit(2)
		}
	}
	for _, d := range []string{*sessionDir, *logDir, filepath.Dir(*transcript), filepath.Dir(*render)} {
		_ = os.MkdirAll(d, 0o755)
	}
	taskBytes, err := os.ReadFile(*taskFile)
	if err != nil {
		fmt.Printf("[driver] read task: %v\n", err)
		os.Exit(2)
	}
	task := strings.Join(strings.Fields(string(taskBytes)), " ")

	cmdline := fmt.Sprintf(`"%s" chat --session-dir "%s" --log-dir "%s" --transcript-file "%s" --render-output-file "%s"`,
		*exe, *sessionDir, *logDir, *transcript, *render)
	if *provider != "" {
		cmdline += ` --provider "` + *provider + `"`
	}
	if *model != "" {
		cmdline += ` --model "` + *model + `"`
	}
	if *reasoning != "" {
		cmdline += ` --reasoning-effort "` + *reasoning + `"`
	}
	if *permissionMode != "" {
		cmdline += ` --permission-mode "` + *permissionMode + `"`
	}
	for _, pattern := range strings.Split(*allowTools, ",") {
		if pattern = strings.TrimSpace(pattern); pattern != "" {
			cmdline += ` --allow-tool "` + pattern + `"`
		}
	}
	// Remote shell approvals ask the user for every distinct command family. In a
	// deployment scenario that means a dozen prompts, and each one costs a full
	// approval round-trip. `session_readonly_shell` lets the second and later
	// read-only command of the same family reuse the first approval, which is what
	// an operator watching this run would do.
	if *approvalReuse != "" {
		cmdline += ` --approval-reuse "` + *approvalReuse + `"`
	}
	fmt.Printf("[driver] cmdline=%s\n", cmdline)

	cpty, err := startConPty(cmdline, *workDir)
	if err != nil {
		fmt.Printf("[driver] start error: %v\n", err)
		os.Exit(1)
	}
	defer cpty.Close()

	out := &sink{}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		buf := make([]byte, 8192)
		for {
			n, err := cpty.Read(buf)
			if n > 0 {
				_, _ = out.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	exitCh := make(chan uint32, 1)
	go func() {
		for {
			ret, _ := windows.WaitForSingleObject(cpty.pi.Process, 500)
			if ret != uint32(windows.WAIT_TIMEOUT) {
				var code uint32
				_ = windows.GetExitCodeProcess(cpty.pi.Process, &code)
				exitCh <- code
				return
			}
		}
	}()

	exited := func() (bool, uint32) {
		select {
		case code := <-exitCh:
			exitCh <- code
			return true, code
		default:
			return false, 0
		}
	}

	deadline := time.Now().Add(*budget)

	// 1) TUI 就绪：等横幅，或等输出静止（不同版本横幅文案不同）。
	if !waitStable(out, 90*time.Second, exited) {
		fmt.Println("[driver] FAIL TUI 未就绪")
		dumpTail(out, 4000)
		os.Exit(1)
	}
	fmt.Println("[driver] PASS TUI ready")

	// 2) 提交任务（整行 + 真实 CR，走行编辑器）
	if _, err := cpty.Write([]byte(task + "\r")); err != nil {
		fmt.Printf("[driver] FAIL write task: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("[driver] task submitted (%d chars)\n", len([]rune(task)))

	// 3) 等本轮结束：连续 idle 无新输出，或进程退出，或预算耗尽。
	lastLen := len(out.snapshot())
	lastChange := time.Now()
	started := time.Now()
	approved := 0
	approvalScan := 0
	lastApprovalAt := time.Time{}
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)
		snapshot := out.snapshot()
		if cur := len(snapshot); cur != lastLen {
			lastLen = cur
			lastChange = time.Now()
		}
		// 权限门是真交互的一部分：远程写/执行默认要人确认。驱动扮演用户，
		// 在检测到提示时按键同意——这是"人批过一次"的真实路径，而不是把
		// 权限模式整个关掉。
		// 只看**新增**字节：滚动窗口会保留旧提示，按快照判断会对同一次提示
		// 反复按键（实测刷出 80+ 次误批）。
		// The sink is a rolling window: once it overflows, offsets shift and a
		// stale approvalScan would slice the wrong region. Reset in that case.
		if len(snapshot) < approvalScan {
			approvalScan = 0
		}
		if len(snapshot) > approvalScan {
			fresh := snapshot[approvalScan:]
			if *autoApprove && awaitingApproval(fresh) {
				// A prompt that keeps re-rendering (spinner) would otherwise be
				// answered on every frame. Rate-limit to one answer per 3s so a
				// genuine new prompt is still handled but a redraw is not.
				if time.Since(lastApprovalAt) >= 3*time.Second {
					if _, err := cpty.Write([]byte("y\r")); err == nil {
						approved++
						lastApprovalAt = time.Now()
						approvalScan = len(snapshot)
						fmt.Printf("[driver] approved interactive prompt #%d\n", approved)
						lastChange = time.Now()
					}
				} else {
					// Keep the idle timer alive while a prompt is on screen: the
					// turn is not finished just because the UI stopped printing.
					lastChange = time.Now()
					// Do NOT advance approvalScan here. Advancing it would drop the
					// pending prompt: it would never be re-examined after the rate
					// limit expires, the TUI's own 3-minute approval timer would
					// fire, and the run would look like a product failure.
				}
			} else {
				approvalScan = len(snapshot)
			}
		}
		if e, _ := exited(); e {
			break
		}
		if time.Since(lastChange) > *idle && time.Since(started) > 30*time.Second {
			break
		}
	}
	fmt.Printf("[driver] turn ended after %s (pty bytes=%d)\n",
		time.Since(started).Round(time.Second), len(out.snapshot()))
	fmt.Printf("[driver] interactive approvals answered: %d\n", approved)

	snapshotPath := filepath.Join(filepath.Dir(*transcript), "pty-raw.log")
	_ = os.WriteFile(snapshotPath, []byte(out.snapshot()), 0o644)
	fmt.Printf("[driver] raw PTY written to %s\n", snapshotPath)

	// 4) 退出 TUI
	_, _ = cpty.Write([]byte("/exit\r"))
	select {
	case code := <-exitCh:
		fmt.Printf("[driver] exit code=%d\n", code)
	case <-time.After(40 * time.Second):
		fmt.Println("[driver] exit wait timeout; force close")
	}
	_ = cpty.Close()
	<-readerDone

	// 5) 用真实远端状态裁决（不依赖模型自述）
	pass := verifyRemote(*verifyScript)
	fmt.Println("[driver] ==== PTY tail ====")
	dumpTail(out, 3000)
	if !pass {
		fmt.Println("[driver] REMOTE TASK FAILED")
		os.Exit(1)
	}
	fmt.Println("[driver] REMOTE TASK PASSED")
	os.Exit(0)
}

func dumpTail(out *sink, limit int) {
	tail := out.snapshot()
	if r := []rune(tail); len(r) > limit {
		tail = string(r[len(r)-limit:])
	}
	fmt.Println(tail)
}

// waitStable waits for the TUI banner or for output to stop changing.
func waitStable(out *sink, budget time.Duration, exited func() (bool, uint32)) bool {
	deadline := time.Now().Add(budget)
	lastLen := len(out.snapshot())
	lastChange := time.Now()
	for time.Now().Before(deadline) {
		text := out.snapshot()
		if strings.Contains(text, "Plan OFF") || strings.Contains(text, "aicli") && strings.Contains(text, "❯") {
			return true
		}
		if cur := len(text); cur != lastLen {
			lastLen = cur
			lastChange = time.Now()
		} else if time.Since(lastChange) > 8*time.Second {
			return true
		}
		if e, _ := exited(); e {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
	return false
}

// verifyRemote runs a verification script **on the target machine** and treats
// its exit code as the verdict.
//
// The assertion deliberately lives on the remote side: a model that merely says
// "done" in its final message cannot satisfy it, and a check that ran locally
// could pass against files the model never actually produced remotely.
func verifyRemote(scriptPath string) bool {
	if strings.TrimSpace(scriptPath) == "" {
		fmt.Println("[verify] FAIL no -verify script supplied; refusing to accept the model's word")
		return false
	}
	raw, err := os.ReadFile(scriptPath)
	if err != nil {
		fmt.Printf("[verify] FAIL read verify script: %v\n", err)
		return false
	}
	fmt.Println("[verify] ==== remote state ====")
	output := runWSL(string(raw))
	fmt.Println(output)
	if !strings.Contains(output, "VERIFY_OK") {
		fmt.Println("[verify] FAIL remote verification did not report VERIFY_OK")
		return false
	}
	fmt.Println("[verify] PASS remote verification script succeeded")
	return true
}

// awaitingApproval reports whether the TUI is blocked on a permission prompt.
//
// The prompt is a real part of the product: a remote write or command asks the
// user first. The driver answers it the way a user would instead of turning the
// permission system off, so the scenario still exercises the gate.
func awaitingApproval(snapshot string) bool {
	if strings.Contains(snapshot, "Waiting for approval") {
		return true
	}
	if strings.Contains(snapshot, "Action Required") {
		return true
	}
	// The prompt block itself. This is what stays on screen while the TUI waits;
	// "Action Required" is only the window title and can be missed when the redraw
	// is partial.
	return strings.Contains(snapshot, "请选择") || strings.Contains(snapshot, "esc to interrupt")
}

func runWSL(script string) string {
	// The verification script is written for bash: it uses `[ -x ]`, `${x:-y}`
	// and backslash-free POSIX quoting. Running it through an interactive login
	// shell re-parses it (this WSL defaults to zsh) and rewrites `$*`-style
	// expansions, which silently corrupts the assertions.
	//
	// `bash -s` reads the program from stdin, so the script text never becomes a
	// shell word that a second parser can touch.
	cmd := exec.Command("wsl", "-d", "Ubuntu", "--", "bash", "-s")
	cmd.Stdin = strings.NewReader(script)
	cmd.Env = os.Environ()
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return string(raw) + "\n[verify] wsl error: " + err.Error()
	}
	return string(raw)
}

var _ = context.Background
