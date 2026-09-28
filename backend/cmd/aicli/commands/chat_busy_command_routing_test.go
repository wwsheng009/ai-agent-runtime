package commands

import (
	"bufio"
	"strings"
	"testing"
)

func newBusyPolicyTestQueue() *chatInputQueue {
	return newChatInputQueue(bufio.NewReader(strings.NewReader("")))
}

func drainBusyPolicyTestQueue(t *testing.T, queue *chatInputQueue) []string {
	t.Helper()
	var texts []string
	for {
		select {
		case item := <-queue.lines:
			texts = append(texts, strings.TrimSpace(item.Text))
		default:
			return texts
		}
	}
}

// P1-3：策略解析器 + 消费方注册时的四档路由。
func TestChatInputQueuePolicyRoutingImmediateAndScreen(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	queue := newBusyPolicyTestQueue()
	queue.setCommandPolicyResolver(chatSlashCommandBusyPolicyFor)
	queue.setBusyCommandExecutor(func(chatQueuedInput) bool { return true })

	if got := queue.routeInputTextFromSource("/status", chatInputSourceStdin); !got.immediate() {
		t.Fatalf("/status 应为 immediate，实际 %+v", got)
	}
	if got := queue.routeInputTextFromSource("/theme", chatInputSourceStdin); !got.rejected() {
		t.Fatalf("/theme 应保持 rejected，实际 %+v", got)
	}
	if texts := drainBusyPolicyTestQueue(t, queue); len(texts) != 0 {
		t.Fatalf("I/R 档不应入队，实际 %v", texts)
	}
}

// P1-3/INV-8：非本地终端来源（web/远程/未知）不得走 I/S，必须保持排队（回执语义）。
func TestChatInputQueuePolicyRoutingRejectsRemoteImmediate(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	queue := newBusyPolicyTestQueue()
	queue.setCommandPolicyResolver(chatSlashCommandBusyPolicyFor)
	queue.setBusyCommandExecutor(func(chatQueuedInput) bool { return true })

	if got := queue.routeInputTextFromSource("/status", chatInputSourceWeb); !got.queued() {
		t.Fatalf("web 来源 /status 必须回退排队，实际 %+v", got)
	}
	if got := queue.routeInputTextFromSource("/status", ""); !got.queued() {
		t.Fatalf("未知来源 /status 必须回退排队（fail-closed），实际 %+v", got)
	}
	if texts := drainBusyPolicyTestQueue(t, queue); len(texts) != 2 {
		t.Fatalf("两个回退输入都应入队，实际 %v", texts)
	}
}

// P1-3：消费方未注册（P1-5/P2-4 未落地）时，I/S 必须回退排队，绝不丢输入。
func TestChatInputQueuePolicyRoutingFallsBackWithoutExecutor(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	queue := newBusyPolicyTestQueue()
	queue.setCommandPolicyResolver(chatSlashCommandBusyPolicyFor)

	if got := queue.routeInputTextFromSource("/status", chatInputSourceStdin); !got.queued() {
		t.Fatalf("无消费方时 /status 必须回退排队，实际 %+v", got)
	}
	if texts := drainBusyPolicyTestQueue(t, queue); len(texts) != 1 || texts[0] != "/status" {
		t.Fatalf("回退输入必须入队且内容不变，实际 %v", texts)
	}
}

// P1-7/T18：灰度关闭时路由与旧白名单逐条等价（I/S 不可达）。
func TestChatInputQueuePolicyRoutingGateOffMatchesLegacyGate(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "off")

	queue := newBusyPolicyTestQueue()
	queue.setCommandPolicyResolver(chatSlashCommandBusyPolicyFor)

	if got := queue.routeInputTextFromSource("/status", chatInputSourceStdin); !got.queued() {
		t.Fatalf("关闭灰度后 /status 应排队（旧白名单 queue-safe），实际 %+v", got)
	}
	if got := queue.routeInputTextFromSource("/theme", chatInputSourceStdin); !got.rejected() {
		t.Fatalf("关闭灰度后 /theme 应拒绝（旧白名单未登记），实际 %+v", got)
	}
	if texts := drainBusyPolicyTestQueue(t, queue); len(texts) != 1 || texts[0] != "/status" {
		t.Fatalf("仅 queue-safe 命令应入队，实际 %v", texts)
	}
}

// setCommandGate 兼容包装：true→deferred（旧测试语义不变），且不受新策略影响。
func TestChatInputQueueLegacyCommandGateWrapper(t *testing.T) {
	t.Setenv(chatBusyCommandEnv, "on")

	queue := newBusyPolicyTestQueue()
	queue.setCommandGate(func(string) bool { return true })
	if got := queue.routeInputText("/theme"); !got.queued() {
		t.Fatalf("旧门 true 应映射为排队，实际 %+v", got)
	}
	rejecting := newBusyPolicyTestQueue()
	rejecting.setCommandGate(func(string) bool { return false })
	if got := rejecting.routeInputText("/status"); !got.rejected() {
		t.Fatalf("旧门 false 应映射为拒绝，实际 %+v", got)
	}
}

// P1-5：I/S 档消费方经 consumeBusyCommand 交接；未注册时不得占有。
func TestChatInputQueueConsumeBusyCommand(t *testing.T) {
	queue := newBusyPolicyTestQueue()
	if queue.consumeBusyCommand(chatQueuedInput{Text: "/status", Source: chatInputSourceStdin}) {
		t.Fatal("未注册执行器时不得占有输入")
	}

	var consumed string
	queue.setBusyCommandExecutor(func(item chatQueuedInput) bool {
		consumed = item.Text
		return true
	})
	if !queue.consumeBusyCommand(chatQueuedInput{Text: "/status", Source: chatInputSourceStdin}) {
		t.Fatal("注册执行器后应占有输入")
	}
	if consumed != "/status" {
		t.Fatalf("执行器收到 %q，期望 /status", consumed)
	}
}
