// Command testrelay 把子进程的 stdin/stdout 桥接到一个 TCP 连接，
// 作为测试中 connect.exe 等代理工具的极简替身。
//
// 用法: testrelay <host> <port>
package main

import (
	"io"
	"log"
	"net"
	"os"
)

func main() {
	if len(os.Args) != 3 {
		log.Fatalf("usage: relay <host> <port>")
	}
	conn, err := net.Dial("tcp", net.JoinHostPort(os.Args[1], os.Args[2]))
	if err != nil {
		log.Fatalf("relay dial %s:%s: %v", os.Args[1], os.Args[2], err)
	}
	defer conn.Close()

	// 远端 → stdout
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(os.Stdout, conn)
	}()
	// stdin → 远端
	_, _ = io.Copy(conn, os.Stdin)
	if tc, ok := conn.(*net.TCPConn); ok {
		_ = tc.CloseWrite()
	}
	<-done
}
