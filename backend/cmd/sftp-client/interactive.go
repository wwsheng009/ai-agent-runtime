package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
)

// newAppContext 创建交互/批处理上下文。
func newAppContext(s *sshclient.SFTP, flags *cliFlags) *appContext {
	cwd, _ := os.Getwd()
	return &appContext{
		sftp:      s,
		flags:     flags,
		remoteCWD: ".",
		localCWD:  cwd,
	}
}

// runInteractive 进入交互式 SFTP 命令循环。
func runInteractive(s *sshclient.SFTP, flags *cliFlags) int {
	if !isTerminal() {
		fmt.Fprintln(os.Stderr, "sftp-client: interactive mode requires a terminal")
		return 1
	}

	ctx := newAppContext(s, flags)
	scanner := bufio.NewScanner(os.Stdin)
	fmt.Fprintf(os.Stderr, "sftp> ")
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			fmt.Fprintf(os.Stderr, "sftp> ")
			continue
		}

		// 嵌入路径变量替换
		line = strings.ReplaceAll(line, "$remote", ctx.remoteCWD)
		line = strings.ReplaceAll(line, "$local", ctx.localCWD)

		err := execCommand(ctx, line)
		if err == errQuit {
			break
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "sftp: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "sftp> ")
	}
	return 0
}

// ========== 命令实现（绑定 appContext） ==========

func (c *appContext) cmdPwd() error {
	wd, err := c.sftp.Client().RealPath(c.remoteCWD)
	if err != nil {
		return fmt.Errorf("pwd: %v", err)
	}
	fmt.Println(wd)
	return nil
}

func (c *appContext) cmdLpwd() error {
	wd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("lpwd: %v", err)
	}
	fmt.Println(wd)
	return nil
}

func (c *appContext) cmdLs(args []string) error {
	pathArg := c.remoteCWD
	long := false
	for _, a := range args {
		if a == "-la" || a == "-l" || a == "-al" {
			long = true
		} else {
			pathArg = c.resolveRemote(a)
		}
	}
	infos, err := c.sftp.List(pathArg)
	if err != nil {
		return fmt.Errorf("ls %q: %v", pathArg, err)
	}
	if long {
		for _, fi := range infos {
			fmt.Printf("%s %10d %s\n", fi.Mode().String(), fi.Size(), fi.Name())
		}
	} else {
		names := make([]string, 0, len(infos))
		for _, fi := range infos {
			names = append(names, fi.Name())
		}
		fmt.Println(strings.Join(names, "  "))
	}
	return nil
}

func (c *appContext) cmdLls(args []string) error {
	pathArg := "."
	if len(args) > 0 && args[0] != "-la" && args[0] != "-l" {
		pathArg = args[0]
	}
	cmd := exec.Command("ls", "-la", pathArg)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (c *appContext) cmdCd(args []string) error {
	target := c.remoteCWD
	if len(args) > 0 {
		target = c.resolveRemote(args[0])
	}
	// 验证目录存在
	if _, err := c.sftp.Stat(target); err != nil {
		return fmt.Errorf("cd %q: %v", target, err)
	}
	c.remoteCWD = target
	return nil
}

func (c *appContext) cmdLcd(args []string) error {
	target := "."
	if len(args) > 0 {
		target = args[0]
	}
	if err := os.Chdir(target); err != nil {
		return fmt.Errorf("lcd %q: %v", target, err)
	}
	c.localCWD, _ = os.Getwd()
	return nil
}

func (c *appContext) cmdGet(args []string) error {
	recursive := c.flags.recursive
	remotePath := ""
	localPath := ""
	for _, a := range args {
		if a == "-r" || a == "-R" {
			recursive = true
		} else if remotePath == "" {
			remotePath = a
		} else {
			localPath = a
		}
	}
	if remotePath == "" {
		return fmt.Errorf("get: missing remote path")
	}
	remotePath = c.resolveRemote(remotePath)
	if localPath == "" {
		localPath = filepath.Base(remotePath)
	}

	fi, err := c.sftp.Stat(remotePath)
	if err != nil {
		return fmt.Errorf("get: stat %q: %v", remotePath, err)
	}
	if fi.IsDir() {
		if !recursive {
			return fmt.Errorf("get: %q is a directory (use -r or -R)", remotePath)
		}
		return c.sftp.DownloadDir(remotePath, localPath, c.flags.force)
	}
	return c.sftp.DownloadFile(remotePath, localPath, c.flags.force)
}

func (c *appContext) cmdPut(args []string) error {
	recursive := c.flags.recursive
	localPath := ""
	remotePath := ""
	for _, a := range args {
		if a == "-r" || a == "-R" {
			recursive = true
		} else if localPath == "" {
			localPath = a
		} else {
			remotePath = a
		}
	}
	if localPath == "" {
		return fmt.Errorf("put: missing local path")
	}
	if remotePath == "" {
		remotePath = path.Base(localPath)
	}
	remotePath = c.resolveRemote(remotePath)

	fi, err := os.Stat(localPath)
	if err != nil {
		return fmt.Errorf("put: stat %q: %v", localPath, err)
	}
	if fi.IsDir() {
		if !recursive {
			return fmt.Errorf("put: %q is a directory (use -r or -R)", localPath)
		}
		return c.sftp.UploadDir(localPath, remotePath, c.flags.force)
	}
	return c.sftp.UploadFile(localPath, remotePath, c.flags.force)
}

func (c *appContext) cmdRm(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rm: missing file path")
	}
	for _, p := range args {
		rp := c.resolveRemote(p)
		if err := c.sftp.Remove(rp); err != nil {
			return fmt.Errorf("rm %q: %v", p, err)
		}
	}
	return nil
}

func (c *appContext) cmdRmdir(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("rmdir: missing directory path")
	}
	for _, p := range args {
		rp := c.resolveRemote(p)
		if err := c.sftp.RemoveDir(rp); err != nil {
			return fmt.Errorf("rmdir %q: %v", p, err)
		}
	}
	return nil
}

func (c *appContext) cmdMkdir(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("mkdir: missing directory path")
	}
	for _, p := range args {
		rp := c.resolveRemote(p)
		if err := c.sftp.MkdirAll(rp); err != nil {
			return fmt.Errorf("mkdir %q: %v", p, err)
		}
	}
	return nil
}

func (c *appContext) cmdChmod(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("chmod: usage: chmod <mode> <path>")
	}
	mode, err := strconv.ParseUint(args[0], 8, 32)
	if err != nil {
		return fmt.Errorf("chmod: invalid mode %q", args[0])
	}
	for _, p := range args[1:] {
		rp := c.resolveRemote(p)
		if err := c.sftp.Chmod(rp, os.FileMode(mode)); err != nil {
			return fmt.Errorf("chmod %q: %v", p, err)
		}
	}
	return nil
}

func (c *appContext) cmdChown(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("chown: usage: chown <uid>:<gid> <path>")
	}
	parts := strings.SplitN(args[0], ":", 2)
	if len(parts) < 2 {
		return fmt.Errorf("chown: invalid uid:gid %q", args[0])
	}
	uid, _ := strconv.Atoi(parts[0])
	gid, _ := strconv.Atoi(parts[1])
	for _, p := range args[1:] {
		rp := c.resolveRemote(p)
		if err := c.sftp.Chown(rp, uid, gid); err != nil {
			return fmt.Errorf("chown %q: %v", p, err)
		}
	}
	return nil
}

func (c *appContext) cmdRename(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("rename: usage: rename <old> <new>")
	}
	oldPath := c.resolveRemote(args[0])
	newPath := c.resolveRemote(args[1])
	return c.sftp.Rename(oldPath, newPath)
}

func (c *appContext) cmdSymlink(args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("symlink: usage: symlink <target> <link>")
	}
	// symlink 的目标不需要解析（按原样传递），链接路径需要解析
	linkPath := c.resolveRemote(args[1])
	return c.sftp.Symlink(args[0], linkPath)
}

func (c *appContext) cmdStat(args []string) error {
	target := c.remoteCWD
	if len(args) > 0 {
		target = c.resolveRemote(args[0])
	}
	fi, err := c.sftp.Stat(target)
	if err != nil {
		return fmt.Errorf("stat %q: %v", target, err)
	}
	fmt.Printf("  Size: %d\n", fi.Size())
	fmt.Printf("  Mode: %s\n", fi.Mode().String())
	fmt.Printf("  ModTime: %s\n", fi.ModTime().Format("2006-01-02 15:04:05"))
	if fi.IsDir() {
		fmt.Println("  Type: directory")
	} else {
		fmt.Println("  Type: file")
	}
	return nil
}

func (c *appContext) cmdLocal(command string) error {
	cmdStr := strings.TrimSpace(command)
	if cmdStr == "" {
		return nil
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", cmdStr)
	} else {
		cmd = exec.Command("sh", "-c", cmdStr)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func printHelp() {
	fmt.Println(`Available commands:
  ls [-la] [path]       List remote directory
  lls [path]            List local directory
  cd <path>             Change remote directory
  lcd <path>            Change local directory
  pwd                   Show remote working directory
  lpwd                  Show local working directory
  get [-r] <remote> [local]    Download file(s)/directory
  put [-r] <local> [remote]    Upload file(s)/directory
  rm <file>             Delete remote file
  rmdir <dir>           Delete remote empty directory
  mkdir <dir>           Create remote directory
  chmod <mode> <path>   Change remote file permissions
  chown <uid>:<gid> <path>     Change remote file owner
  rename <old> <new>    Rename remote file or directory
  symlink <target> <link>      Create remote symlink
  stat <path>           Show remote file info
  echo <text>           Print text locally
  !<command>            Execute local shell command
  help / ?              Show this help
  quit / exit / bye     Quit sftp-client`)
}