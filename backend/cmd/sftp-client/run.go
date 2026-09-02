package main

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/wwsheng009/ai-agent-runtime/internal/sshclient"
)

// appContext 保存交互/批处理共享的 SFTP 会话状态。
type appContext struct {
	sftp      *sshclient.SFTP
	flags     *cliFlags
	remoteCWD string
	localCWD  string
}

// resolveRemote 将相对远程路径基于当前远程目录解析为绝对路径。
func (c *appContext) resolveRemote(p string) string {
	if p == "" || p == "." {
		return c.remoteCWD
	}
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "\\") {
		return path.Clean(strings.ReplaceAll(p, "\\", "/"))
	}
	return path.Clean(path.Join(c.remoteCWD, strings.ReplaceAll(p, "\\", "/")))
}

// runBatch 批处理模式：从文件逐行读取命令执行。
// OpenSSH sftp -b 语义：任一条命令失败默认中止并返回非零退出码。
func runBatch(s *sshclient.SFTP, flags *cliFlags) int {
	ctx := newAppContext(s, flags)
	f, err := os.Open(flags.batchFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: open batch file %q: %v\n", flags.batchFile, err)
		return 1
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		// 空行与注释跳过
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// 支持 echo（本地打印）
		if strings.HasPrefix(line, "echo ") {
			fmt.Println(strings.TrimPrefix(line, "echo "))
			continue
		}
		if err := execCommand(ctx, line); err != nil {
			fmt.Fprintf(os.Stderr, "sftp-client: batch line %d: %q: %v\n", lineNo, line, err)
			return 1
		}
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: read batch file: %v\n", err)
		return 1
	}
	return 0
}

// runDirect 直接传输模式：
//   - sftp-client host:remote-path local-path   下载
//   - sftp-client host local-path remote-path   上传（第二个参数为远程）
//   - sftp-client host:remote-dir/              列出远程目录
func runDirect(s *sshclient.SFTP, flags *cliFlags, remotePath string) int {
	ctx := newAppContext(s, flags)
	// 只有 host:path，无本地参数 → 列出远程目录
	if remotePath != "" && len(flags.paths) == 0 {
		return ctx.doLs(remotePath)
	}

	// host 且两个参数：上传模式
	if remotePath == "" && len(flags.paths) >= 2 {
		localPath := flags.paths[0]
		remoteTarget := flags.paths[1]
		return ctx.doPut(localPath, remoteTarget)
	}

	// host:remote-path 且一个本地参数：下载模式
	if remotePath != "" && len(flags.paths) >= 1 {
		localPath := flags.paths[0]
		return ctx.doGet(remotePath, localPath)
	}

	fmt.Fprintln(os.Stderr, "sftp-client: invalid arguments for direct transfer")
	fmt.Fprintln(os.Stderr, "  download: sftp-client user@host:remote-path local-path")
	fmt.Fprintln(os.Stderr, "  upload:   sftp-client user@host local-path remote-path")
	fmt.Fprintln(os.Stderr, "  list:     sftp-client user@host:remote-dir/")
	return 254
}

// doGet 下载单个文件或目录。
func (c *appContext) doGet(remotePath, localPath string) int {
	fi, err := c.sftp.Stat(remotePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: stat %q: %v\n", remotePath, err)
		return 1
	}

	if fi.IsDir() {
		if !c.flags.recursive {
			fmt.Fprintf(os.Stderr, "sftp-client: %q is a directory (use -R/--recursive to download)\n", remotePath)
			return 1
		}
		if err := c.sftp.DownloadDir(remotePath, localPath, c.flags.force); err != nil {
			fmt.Fprintf(os.Stderr, "sftp-client: download dir %q: %v\n", remotePath, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "sftp-client: downloaded %q -> %q\n", remotePath, localPath)
		return 0
	}

	if err := c.sftp.DownloadFile(remotePath, localPath, c.flags.force); err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: download %q: %v\n", remotePath, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "sftp-client: downloaded %q -> %q\n", remotePath, localPath)
	return 0
}

// doPut 上传单个文件或目录。
func (c *appContext) doPut(localPath, remotePath string) int {
	fi, err := os.Stat(localPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: stat %q: %v\n", localPath, err)
		return 1
	}

	if fi.IsDir() {
		if !c.flags.recursive {
			fmt.Fprintf(os.Stderr, "sftp-client: %q is a directory (use -R/--recursive to upload)\n", localPath)
			return 1
		}
		if err := c.sftp.UploadDir(localPath, remotePath, c.flags.force); err != nil {
			fmt.Fprintf(os.Stderr, "sftp-client: upload dir %q: %v\n", localPath, err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "sftp-client: uploaded %q -> %q\n", localPath, remotePath)
		return 0
	}

	if err := c.sftp.UploadFile(localPath, remotePath, c.flags.force); err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: upload %q: %v\n", localPath, err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "sftp-client: uploaded %q -> %q\n", localPath, remotePath)
	return 0
}

// doLs 列出远程目录。
func (c *appContext) doLs(remotePath string) int {
	infos, err := c.sftp.List(remotePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "sftp-client: list %q: %v\n", remotePath, err)
		return 1
	}
	printListing(infos)
	return 0
}

// execCommand 执行单条 SFTP 命令（交互/批处理共用）。
func execCommand(ctx *appContext, line string) error {
	fields := splitCommand(line)
	if len(fields) == 0 {
		return nil
	}
	cmd := strings.ToLower(fields[0])
	args := fields[1:]

	switch cmd {
	case "quit", "exit", "bye", "q":
		return errQuit
	case "help", "?":
		printHelp()
		return nil
	case "pwd":
		return ctx.cmdPwd()
	case "lpwd":
		return ctx.cmdLpwd()
	case "ls", "dir":
		return ctx.cmdLs(args)
	case "lls":
		return ctx.cmdLls(args)
	case "cd":
		return ctx.cmdCd(args)
	case "lcd":
		return ctx.cmdLcd(args)
	case "get":
		return ctx.cmdGet(args)
	case "put":
		return ctx.cmdPut(args)
	case "rm", "remove", "del":
		return ctx.cmdRm(args)
	case "rmdir":
		return ctx.cmdRmdir(args)
	case "mkdir":
		return ctx.cmdMkdir(args)
	case "chmod":
		return ctx.cmdChmod(args)
	case "chown":
		return ctx.cmdChown(args)
	case "rename", "mv":
		return ctx.cmdRename(args)
	case "symlink", "ln":
		return ctx.cmdSymlink(args)
	case "stat", "info":
		return ctx.cmdStat(args)
	case "echo":
		fmt.Println(strings.Join(args, " "))
		return nil
	default:
		// 本地命令：!<command>
		if strings.HasPrefix(cmd, "!") {
			return ctx.cmdLocal(strings.TrimPrefix(line, cmd))
		}
		return fmt.Errorf("unknown command %q (type help for a list)", cmd)
	}
}

// errQuit 是退出交互循环的信号错误。
var errQuit = fmt.Errorf("quit")

// splitCommand 拆分命令行（支持双引号包裹的路径）。
func splitCommand(line string) []string {
	var fields []string
	var current strings.Builder
	inQuote := false
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if current.Len() > 0 {
				fields = append(fields, current.String())
				current.Reset()
			}
		default:
			current.WriteRune(r)
		}
	}
	if current.Len() > 0 {
		fields = append(fields, current.String())
	}
	return fields
}

func printListing(infos []os.FileInfo) {
	for _, fi := range infos {
		name := fi.Name()
		if fi.IsDir() {
			name += "/"
		}
		fmt.Printf("%s %10d %s\n", fi.Mode().String(), fi.Size(), name)
	}
}