//go:build windows
// +build windows

// goal-conpty-driver 在真实 ConPTY 中驱动 `aicli chat` 的交互式 TUI（行编辑器 +
// 参数补全 + resume kick watcher 等仅交互路径），按步骤写入真实控制字符（CR 等）
// 并等待输出标记。输出标记同时检查 PTY 实时流（256KB 滚动窗口）、会话
// debug.log 与 transcript 文件，避免 TUI 合并渲染导致的假阴性。
//
// 用途：`/goal` 特性的交互式端到端验证（安全阀有界续跑、resume kick、
// pause/clear 状态流转）。非交互 scripted 场景见 scripts/run-conpty-goal-e2e.ps1。
//
// 用法：
//
//	go run ./cmd/goal-conpty-driver -exe .\.tmp\aicli-m4.exe \
//	  -session-dir ..\output\goal-conpty-driver\sessions \
//	  -log-dir ..\output\goal-conpty-driver\logs \
//	  -transcript ..\output\goal-conpty-driver\transcript.txt \
//	  -render ..\output\goal-conpty-driver\render.log \
//	  -provider workbuddy -model cn:deepseek-v4.1-flash -reasoning-effort max
//
// 退出码：0 = 全部步骤通过；1 = 任一步骤超时/失败（打印现场尾部）。
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
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
	sinkWindowBytes                              = 1 << 20 // 1MB 滚动窗口（标题动画刷屏期间防目标行被挤出）
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

type ConPty struct {
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

func createConsoleProcessAttachedToPTY(hpc _HPCON, commandLine, workDir string, env []string) (*windows.ProcessInformation, error) {
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
	var envBlock *uint16
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT)
	if env != nil {
		flags |= uint32(windows.CREATE_UNICODE_ENVIRONMENT)
		envBlock = createEnvBlock(env)
	}
	siEx, err := getStartupInfoExForPTY(hpc)
	if err != nil {
		return nil, err
	}
	var pi windows.ProcessInformation
	err = windows.CreateProcess(
		nil, cmdLine, nil, nil, false, flags, envBlock, currentDirectory,
		&siEx.startupInfo, &pi)
	if err != nil {
		return nil, err
	}
	return &pi, nil
}

func createEnvBlock(envv []string) *uint16 {
	if len(envv) == 0 {
		return &utf16.Encode([]rune("\x00\x00"))[0]
	}
	length := 0
	for _, s := range envv {
		length += len(s) + 1
	}
	length += 1

	b := make([]byte, length)
	i := 0
	for _, s := range envv {
		l := len(s)
		copy(b[i:i+l], []byte(s))
		copy(b[i+l:i+l+1], []byte{0})
		i = i + l + 1
	}
	copy(b[i:i+1], []byte{0})
	return &utf16.Encode([]rune(string(b)))[0]
}

func closeHandles(handles ...windows.Handle) error {
	var err error
	for _, h := range handles {
		if h != windows.InvalidHandle {
			if err == nil {
				err = windows.CloseHandle(h)
			} else {
				windows.CloseHandle(h)
			}
		}
	}
	return err
}

func (cpty *ConPty) Close() error {
	if cpty.closed {
		return nil
	}
	cpty.closed = true
	win32ClosePseudoConsole(cpty.hpc)
	return closeHandles(
		cpty.pi.Process, cpty.pi.Thread,
		cpty.ptyIn.handle, cpty.ptyOut.handle,
		cpty.cmdIn.handle, cpty.cmdOut.handle)
}

func (cpty *ConPty) Read(p []byte) (int, error)  { return cpty.cmdOut.Read(p) }
func (cpty *ConPty) Write(p []byte) (int, error) { return cpty.cmdIn.Write(p) }

// Wait 等待进程退出；ctx 取消时返回 STILL_ACTIVE 与错误。
func (cpty *ConPty) Wait(ctx context.Context) (uint32, error) {
	var exitCode uint32 = _STILL_ACTIVE
	for {
		if err := ctx.Err(); err != nil {
			return _STILL_ACTIVE, fmt.Errorf("wait canceled: %v", err)
		}
		ret, _ := windows.WaitForSingleObject(cpty.pi.Process, 500)
		if ret != uint32(windows.WAIT_TIMEOUT) {
			err := windows.GetExitCodeProcess(cpty.pi.Process, &exitCode)
			return exitCode, err
		}
	}
}

func startConPty(commandLine, workDir string, env []string) (*ConPty, error) {
	coords := _COORD{140, 40}
	var cmdIn, cmdOut, ptyIn, ptyOut windows.Handle
	if err := windows.CreatePipe(&ptyIn, &cmdIn, nil, 0); err != nil {
		return nil, fmt.Errorf("CreatePipe: %v", err)
	}
	if err := windows.CreatePipe(&cmdOut, &ptyOut, nil, 0); err != nil {
		closeHandles(ptyIn, cmdIn)
		return nil, fmt.Errorf("CreatePipe: %v", err)
	}
	hPc, err := win32CreatePseudoConsole(&coords, ptyIn, ptyOut)
	if err != nil {
		closeHandles(ptyIn, ptyOut, cmdIn, cmdOut)
		return nil, err
	}
	pi, err := createConsoleProcessAttachedToPTY(hPc, commandLine, workDir, env)
	if err != nil {
		closeHandles(ptyIn, ptyOut, cmdIn, cmdOut)
		win32ClosePseudoConsole(hPc)
		return nil, fmt.Errorf("Failed to create console process: %v", err)
	}
	return &ConPty{
		hpc: hPc, pi: pi,
		ptyIn:  &handleIO{ptyIn},
		ptyOut: &handleIO{ptyOut},
		cmdIn:  &handleIO{cmdIn},
		cmdOut: &handleIO{cmdOut},
	}, nil
}

// sink 是 PTY 输出的滚动窗口（仅保留最近 sinkWindowBytes，避免长会话下
// 每次轮询快照的全量拷贝开销）。
type sink struct {
	mu    sync.Mutex
	ring  []byte
	total int
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.total += len(p)
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

func (s *sink) totalWritten() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total
}

func newestDebugLog(root string) string {
	best := ""
	var bestMod time.Time
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if !strings.EqualFold(d.Name(), "debug.log") {
			return nil
		}
		if info, statErr := d.Info(); statErr == nil && info.ModTime().After(bestMod) {
			best, bestMod = path, info.ModTime()
		}
		return nil
	})
	return best
}

func readFileText(path string) string {
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(data)
}

// step 是驱动脚本的一步：可选发送一行输入（自动追加 CR），然后等待任一标记
// （any）或某标记累计出现 countMin 次（countSub，优先统计 debug/transcript）。
type step struct {
	name     string
	send     string
	any      []string
	countSub string
	countMin int
	timeout  time.Duration
	settle   bool
	// confirmBusy：发送后若出现"忙时执行"确认门（busy slash command Phase A），
	// 自动应答 y 继续。
	confirmBusy bool
}

type stepResult struct {
	name   string
	ok     bool
	detail string
}

func containsAny(text string, candidates []string) bool {
	for _, c := range candidates {
		if c != "" && strings.Contains(text, c) {
			return true
		}
	}
	return false
}

func main() {
	exe := flag.String("exe", `.\aicli.exe`, "aicli 可执行文件路径")
	workDir := flag.String("workdir", "", "子进程工作目录（默认当前目录）")
	provider := flag.String("provider", "workbuddy", "provider（空串用配置默认）")
	model := flag.String("model", "cn:deepseek-v4.1-flash", "model（空串用配置默认）")
	reasoning := flag.String("reasoning-effort", "max", "reasoning effort（空串用配置默认）")
	sessionDir := flag.String("session-dir", "", "会话目录（必填）")
	logDir := flag.String("log-dir", "", "日志目录（必填）")
	transcript := flag.String("transcript", "", "transcript 文件路径（必填）")
	render := flag.String("render", "", "render 输出镜像路径（必填）")
	title := flag.String("title", "goal-conpty-driver", "会话标题")
	maxc := flag.Int("max-continuations", 2, "注入 AICLI_GOAL_MAX_CONTINUATIONS 的值（<=0 不注入）")
	flag.Parse()

	if *sessionDir == "" || *logDir == "" || *transcript == "" || *render == "" {
		fmt.Println("missing required flags: -session-dir/-log-dir/-transcript/-render")
		os.Exit(2)
	}
	for _, dir := range []string{*sessionDir, *logDir, filepath.Dir(*transcript), filepath.Dir(*render)} {
		_ = os.MkdirAll(dir, 0o755)
	}

	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(kv), "AICLI_GOAL_MAX_CONTINUATIONS=") {
			continue
		}
		env = append(env, kv)
	}
	if *maxc > 0 {
		env = append(env, fmt.Sprintf("AICLI_GOAL_MAX_CONTINUATIONS=%d", *maxc))
	}

	cmdline := fmt.Sprintf(`"%s" chat --session-dir "%s" --log-dir "%s" --transcript-file "%s" --render-output-file "%s" --title "%s"`,
		*exe, *sessionDir, *logDir, *transcript, *render, *title)
	if *provider != "" {
		cmdline += ` --provider "` + *provider + `"`
	}
	if *model != "" {
		cmdline += ` --model "` + *model + `"`
	}
	if *reasoning != "" {
		cmdline += ` --reasoning-effort "` + *reasoning + `"`
	}
	fmt.Printf("[driver] cmdline=%s\n", cmdline)

	cpty, err := startConPty(cmdline, *workDir, env)
	if err != nil {
		fmt.Printf("[driver] start error: %v\n", err)
		os.Exit(1)
	}
	defer cpty.Close()
	globalPty = cpty

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
		code, _ := cpty.Wait(context.Background())
		exitCh <- code
	}()

	procExited := func() (bool, uint32) {
		select {
		case code := <-exitCh:
			exitCh <- code // put back for later reads
			return true, code
		default:
			return false, 0
		}
	}

	debugPath := ""
	sourcesText := func() string {
		if debugPath == "" {
			debugPath = newestDebugLog(*logDir)
		}
		return out.snapshot() + "\n" + readFileText(*transcript) + "\n" + readFileText(debugPath)
	}
	countText := func(sub string) int {
		if debugPath == "" {
			debugPath = newestDebugLog(*logDir)
		}
		// 只按 debug 计数：transcript 与 debug 会同时记录同一事件，双来源会
		// 把单次事件计成 2 次（首版驱动因此让"resume 后达安全阀"假快通过）。
		if txt := readFileText(debugPath); txt != "" {
			return strings.Count(txt, sub)
		}
		return strings.Count(readFileText(*transcript), sub)
	}

	goalObjective := "/goal 等待型目标：只回复一句“等待中”；不要调用 update_goal，也不要调用任何工具。目标保持 active 等待用户指令。"

	steps := []step{
		{name: "启动横幅", any: []string{"Plan OFF"}, timeout: 60 * time.Second},
		{name: "工具/技能加载静止", settle: true, timeout: 120 * time.Second},
		{name: "设置等待型目标", send: goalObjective, any: []string{"Goal 已设置"}, timeout: 120 * time.Second},
		{name: "首轮续跑达安全阀（bounded）", any: []string{"已达配置上限", "goal marked blocked"}, timeout: 240 * time.Second},
		{name: "pause 生效", send: "/goal pause", any: []string{"Goal 已暂停"}, timeout: 120 * time.Second, confirmBusy: true},
		{name: "resume 生效", send: "/goal resume", any: []string{"Goal 已恢复"}, timeout: 120 * time.Second, confirmBusy: true},
		{name: "resume 后新一轮续跑达安全阀", countSub: "已达配置上限", countMin: 2, timeout: 240 * time.Second},
		{name: "clear 前等待空闲", settle: true, timeout: 30 * time.Second},
		{name: "clear 生效", send: "/goal clear", any: []string{"Goal 已清除"}, timeout: 120 * time.Second, confirmBusy: true},
	}

	results := make([]stepResult, 0, len(steps))
	allOK := true
	for _, st := range steps {
		start := time.Now()
		ok := runStep(st, out, sourcesText, countText, procExited)
		detail := fmt.Sprintf("waited=%s", time.Since(start).Round(time.Second))
		if !ok {
			allOK = false
			tail := out.snapshot()
			if r := []rune(tail); len(r) > 700 {
				tail = string(r[len(r)-700:])
			}
			detail += "\n---- PTY tail ----\n" + tail
		}
		results = append(results, stepResult{name: st.name, ok: ok, detail: detail})
		mark := "PASS"
		if !ok {
			mark = "FAIL"
		}
		fmt.Printf("[driver] %s %s (%s)\n", mark, st.name, detail)
		if !ok {
			break
		}
	}

	if allOK {
		// 正常收尾：/exit 触发终端还原与会话落盘。
		_, _ = cpty.Write([]byte("/exit\r"))
		select {
		case code := <-exitCh:
			fmt.Printf("[driver] exit code=%d\n", code)
			if code != 0 {
				allOK = false
				results = append(results, stepResult{name: "进程退出码", ok: false, detail: fmt.Sprintf("exit=%d", code)})
			}
		case <-time.After(30 * time.Second):
			fmt.Println("[driver] FAIL 退出等待超时（30s），强制关闭")
			allOK = false
			results = append(results, stepResult{name: "退出等待", ok: false, detail: "timeout 30s"})
		}
	}

	_ = cpty.Close()
	<-readerDone

	fmt.Println("[driver] ==== 步骤结果 ====")
	for _, r := range results {
		mark := "PASS"
		if !r.ok {
			mark = "FAIL"
		}
		fmt.Printf("[driver] [%s] %s\n", mark, r.name)
	}
	if !allOK {
		os.Exit(1)
	}
	fmt.Println("[driver] ALL STEPS PASSED")
	os.Exit(0)
}

func runStep(st step, out *sink, sourcesText func() string, countText func(string) int, procExited func() (bool, uint32)) bool {
	if st.send != "" {
		// 行编辑器提交需要真实的 CR（\r）；目标命令为整行文本 + CR。
		if _, err := writeAll(out, st.send); err != nil {
			return false
		}
	}
	deadline := time.Now().Add(st.timeout)
	lastLen, lastChange := out.totalWritten(), time.Now()
	busyConfirmed := false
	for {
		if st.confirmBusy && !busyConfirmed {
			if strings.Contains(sourcesText(), "忙时执行") {
				if _, err := writeAll(out, "y"); err == nil {
					busyConfirmed = true
					fmt.Println("[driver] 检测到忙时命令确认门，已应答 y")
				}
			}
		}
		if st.settle {
			if cur := out.totalWritten(); cur != lastLen {
				lastLen, lastChange = cur, time.Now()
			} else if time.Since(lastChange) >= 4*time.Second && time.Since(lastChange) < st.timeout {
				return true
			}
		}
		if len(st.any) > 0 && containsAny(sourcesText(), st.any) {
			return true
		}
		if st.countSub != "" && countText(st.countSub) >= st.countMin {
			return true
		}
		if exited, _ := procExited(); exited {
			// 再给一轮最终检查机会（输出可能刚到就退出）。
			time.Sleep(300 * time.Millisecond)
			if len(st.any) > 0 && containsAny(sourcesText(), st.any) {
				return true
			}
			if st.countSub != "" && countText(st.countSub) >= st.countMin {
				return true
			}
			return false
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// writeAll 通过全局 PTY 写入；为保持 runStep 签名简单，用包级变量承接。
var globalPty *ConPty

func writeAll(_ *sink, line string) (int, error) {
	if globalPty == nil {
		return 0, fmt.Errorf("pty not initialized")
	}
	payload := []byte(line + "\r")
	n, err := globalPty.Write(payload)
	if err == nil {
		fmt.Printf("[driver] send: %s\n", truncateRunes(line, 80))
	}
	return n, err
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "…"
}
