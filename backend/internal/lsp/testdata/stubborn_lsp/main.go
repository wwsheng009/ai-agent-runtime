// testdata/stubborn_lsp 是一个**故意不合规**的语言服务器，用于验收 LSP 子进程
// 的生命周期保证（live_hard_exit_orphan_test.go）。
//
// 它只做一件事：完成 initialize 握手，然后**再也不读 stdin**，直接睡死。
//
// 为什么需要它：LSP over stdio 的现实退出机制是"客户端关闭 stdin → 服务端读到
// EOF → 自行退出"。gopls 遵守这个约定，所以"宿主被强杀后没有孤儿"这件事用
// gopls 验是**验不出真伪的**——它可能在根本没绑定 Job Object 的情况下就通过，
// 因为它自己够乖。
//
// 这个 stub 故意不读 stdin：宿主一旦死亡，管道写端关闭对它没有任何影响，它会
// 一直活着。于是"强杀宿主后它消失了"就只可能来自 OS 级生命周期绑定
// （Windows Job Object KILL_ON_JOB_CLOSE / Unix Setpgid + 父进程组语义），
// 而不可能来自服务端的礼貌。
//
// 这正是 ADR-0005 D2 要求但一直缺的断言：那条验收此前只覆盖 MCP
// （scripts/acp_e2e_mcp_parent_kill.go），没有覆盖 LSP。
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

func main() {
	in := bufio.NewReader(os.Stdin)
	for {
		length, err := readContentLength(in)
		if err != nil {
			// 读不到 initialize（宿主已死或管道关闭）：静默退出。
			// 真实验收里宿主是被强杀的，这条分支不该影响判定——
			// 判定看的是"不读 stdin 之后是否仍存活"。
			return
		}
		body := make([]byte, length)
		if _, err := readFull(in, body); err != nil {
			return
		}
		id := extractID(string(body))
		if !strings.Contains(string(body), "\"initialize\"") {
			continue
		}
		resp := fmt.Sprintf(
			`{"jsonrpc":"2.0","id":%d,"result":{"capabilities":{"textDocumentSync":1},`+
				`"serverInfo":{"name":"stubborn-lsp","version":"0.0.1"}}}`,
			id,
		)
		fmt.Fprintf(os.Stdout, "Content-Length: %d\r\n\r\n%s", len(resp), resp)
		break
	}
	// 握手完成后**不再触碰 stdin**，并忽略任何退出信号：故意不合规。
	// 只有 OS 级强制终止（Job Object / 父进程组）才能结束这个进程。
	//
	// 这里必须用 sleep 而不是 `select {}`：没有其它 goroutine 时，Go 运行时会
	// 判定"all goroutines are asleep"并直接 panic 杀掉本进程——那等于把 stub
	// 自己变成一个主动退出的进程，整个实验就白做了。sleep 中的 goroutine
	// 不参与死锁判定。
	for {
		time.Sleep(time.Hour)
	}
}

func readContentLength(r *bufio.Reader) (int, error) {
	length := -1
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			// 空行是 header 与 body 的分隔符，必须在这里**消费掉**再返回：
			// 若继续读下一个 header，body 就会带着残留的 "\r\n" 前缀，
			// 请求 id 解析不出来 → 客户端永远等不到响应 → 握手超时。
			if length >= 0 {
				return length, nil
			}
			continue
		}
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			n, err := strconv.Atoi(strings.TrimSpace(line[len("content-length:"):]))
			if err != nil {
				return 0, err
			}
			length = n
		}
	}
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	read := 0
	for read < len(buf) {
		n, err := r.Read(buf[read:])
		read += n
		if err != nil {
			return read, err
		}
	}
	return read, nil
}

// extractID 取请求 id。用真解析而不是字符串匹配：手写标记对空格敏感
// （`"id": 1` 匹配不到），而 id 对不上客户端会一直等响应。
func extractID(body string) int {
	var req struct {
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &req); err != nil || len(req.ID) == 0 {
		return 0
	}
	var id int
	if err := json.Unmarshal(req.ID, &id); err != nil {
		return 0
	}
	return id
}
