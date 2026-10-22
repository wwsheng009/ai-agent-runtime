package sshclient

import (
	"bytes"
	"context"
	"fmt"
	"io"

	"golang.org/x/crypto/ssh"
)

// ExitError 表示远程命令以非零状态退出。
type ExitError struct {
	ExitCode int
	Stderr   []byte
}

func (e *ExitError) Error() string {
	if len(e.Stderr) > 0 {
		return fmt.Sprintf("remote command exited with status %d: %s", e.ExitCode, bytes.TrimSpace(e.Stderr))
	}
	return fmt.Sprintf("remote command exited with status %d", e.ExitCode)
}

// RunCommand 在远程执行单条命令，将 stdout/stderr 分别写入 wOut/wErr。
// 返回远程退出码；连接级错误返回 error。
func RunCommand(client *ssh.Client, command string, wOut, wErr io.Writer) (int, error) {
	return RunCommandIO(context.Background(), client, command, nil, wOut, wErr)
}

// RunCommandIO 在远程执行单条命令，语义与 RunCommand 相同，但支持把本地
// stdin 持续写入远端进程。stdio MCP 这类双向协议必须用它：RunCommand 只接
// stdout/stderr，远端进程拿不到任何输入，JSON-RPC 的 initialize 就会一直等待。
//
// stdin 为 nil 时不设置 session.Stdin（保持原有单向行为）。
func RunCommandIO(ctx context.Context, client *ssh.Client, command string, stdin io.Reader, wOut, wErr io.Writer) (int, error) {
	session, err := client.NewSession()
	if err != nil {
		return 255, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	if stdin != nil {
		session.Stdin = stdin
	}
	session.Stdout = wOut
	session.Stderr = wErr

	if ctx == nil {
		ctx = context.Background()
	}
	// 看门狗：ctx 完成时强制断开底层连接（x/crypto 的 session.Run 不可中断）。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-done:
		}
	}()

	if err := session.Run(command); err != nil {
		var exitErr *ssh.ExitError
		if IsExitError(err, &exitErr) {
			return exitErr.ExitStatus(), nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 255, fmt.Errorf("run command interrupted: %w", ctxErr)
		}
		return 255, fmt.Errorf("run command: %w", err)
	}
	return 0, nil
}

// RunCommandContext 在远程执行单条命令，语义与 RunCommand 相同，但受 ctx 约束。
// 当 ctx 被取消/超时时，强制断开 SSH 连接使 session.Run 立即返回，
// 避免服务器死链不回复时无限阻塞。返回值同 RunCommand。
func RunCommandContext(ctx context.Context, client *ssh.Client, command string, wOut, wErr io.Writer) (int, error) {
	session, err := client.NewSession()
	if err != nil {
		return 255, fmt.Errorf("open session: %w", err)
	}
	defer session.Close()

	session.Stdout = wOut
	session.Stderr = wErr

	// 看门狗：ctx 完成时强制断开底层连接（x/crypto 的 session.Run 不可中断，
	// 唯一可靠地让其返回的方式是关闭连接），从而让 Run 报 connection closed 退出。
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			client.Close()
		case <-done:
		}
	}()

	if err := session.Run(command); err != nil {
		var exitErr *ssh.ExitError
		if IsExitError(err, &exitErr) {
			return exitErr.ExitStatus(), nil
		}
		// ctx 超时/取消导致的断开，报错信息带上 ctx 原因，便于调用方识别超时。
		if ctxErr := ctx.Err(); ctxErr != nil {
			return 255, fmt.Errorf("run command interrupted: %w", ctxErr)
		}
		return 255, fmt.Errorf("run command: %w", err)
	}
	return 0, nil
}

// RunCommandCapture 执行远程命令并返回合并输出（命令模式用）。
func RunCommandCapture(client *ssh.Client, command string) (string, int, error) {
	var out, errBuf bytes.Buffer
	code, err := RunCommand(client, command, &out, &errBuf)
	return out.String() + errBuf.String(), code, err
}

// OpenSession 打开一个新的远程会话（供交互式 shell 等使用）。
func OpenSession(client *ssh.Client) (*ssh.Session, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open session: %w", err)
	}
	return session, nil
}

// IsExitError 判断 err 是否为 *ssh.ExitError（兼容 wrap）。
func IsExitError(err error, target **ssh.ExitError) bool {
	if err == nil {
		return false
	}
	switch e := err.(type) {
	case *ssh.ExitError:
		*target = e
		return true
	case *ssh.ExitMissingError:
		return false
	}
	return false
}
