package commands

import (
	"bufio"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"time"
)

type chatQueuedInput struct {
	Text       string
	Source     string
	EnqueuedAt time.Time
}

type chatInputQueue struct {
	reader        *bufio.Reader
	lines         chan chatQueuedInput
	priorityLines chan chatQueuedInput
	errs          chan error
	start         sync.Once
	mu            sync.RWMutex
	priorityMode  bool
}

func newChatInputQueue(reader *bufio.Reader) *chatInputQueue {
	if reader == nil {
		reader = newChatInputReader()
	}
	return &chatInputQueue{
		reader:        reader,
		lines:         make(chan chatQueuedInput, 32),
		priorityLines: make(chan chatQueuedInput, 4),
		errs:          make(chan error, 1),
	}
}

func ensureChatInputQueue(session *ChatSession) *chatInputQueue {
	if session == nil {
		return nil
	}
	if session.InputQueue == nil {
		session.InputQueue = newChatInputQueue(chatSessionInputReader(session))
	}
	session.InputQueue.startPump()
	return session.InputQueue
}

func (q *chatInputQueue) startPump() {
	if q == nil {
		return
	}
	q.start.Do(func() {
		q.ensureChannels()
		if q.reader == nil {
			q.reader = newChatInputReader()
		}
		go func() {
			for {
				line, err := q.reader.ReadString('\n')
				if line != "" {
					q.routeLine(chatQueuedInput{
						Text:       line,
						Source:     "stdin",
						EnqueuedAt: time.Now().UTC(),
					})
				}
				if err != nil {
					// EOF（Ctrl+D/Ctrl+Z）：静默忽略，不放入 errs 通道，
					// 避免抢占 ctx.Done() 导致 Ctrl+C 中断失效
					if errors.Is(err, io.EOF) {
						continue
					}
					// 将错误通知给等待方，但不退出泵 goroutine。
					// 在 Ctrl+C（SIGINT）场景下，ReadString 可能因终端中断返回临时错误，
					// 泵应继续读取后续输入而非永久退出。
					select {
					case q.errs <- err:
					default:
					}
					// 其他错误短暂睡眠避免空转，然后继续读取
					time.Sleep(50 * time.Millisecond)
					continue
				}
			}
		}()
	})
}

func (q *chatInputQueue) readLine(ctx context.Context) (string, error) {
	if q == nil {
		return "", io.EOF
	}
	q.startPump()
	for {
		select {
		case item := <-q.lines:
			return item.Text, nil
		default:
		}
		select {
		case item := <-q.lines:
			return item.Text, nil
		case err := <-q.errs:
			select {
			case item := <-q.lines:
				return item.Text, nil
			default:
			}
			return "", err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (q *chatInputQueue) readPriorityLine(ctx context.Context) (string, error) {
	if q == nil {
		return "", io.EOF
	}
	q.setPriorityMode(true)
	defer q.setPriorityMode(false)
	q.startPump()
	for {
		select {
		case item := <-q.priorityLines:
			return item.Text, nil
		default:
		}
		select {
		case item := <-q.priorityLines:
			return item.Text, nil
		case err := <-q.errs:
			select {
			case item := <-q.priorityLines:
				return item.Text, nil
			default:
			}
			return "", err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}

func (q *chatInputQueue) pendingCount() int {
	if q == nil {
		return 0
	}
	q.ensureChannels()
	return len(q.lines)
}

func (q *chatInputQueue) discardPending() int {
	if q == nil {
		return 0
	}
	q.ensureChannels()
	discarded := 0
	for {
		select {
		case <-q.lines:
			discarded++
		default:
			return discarded
		}
	}
}

func chatInteractiveReadLine(session *ChatSession, ctx context.Context) (string, error) {
	if session != nil && session.InputQueue != nil {
		return session.InputQueue.readLine(ctx)
	}
	reader := chatSessionInputReader(session)
	line, err := reader.ReadString('\n')
	if line != "" {
		return line, nil
	}
	return "", err
}

func chatInteractiveReadPriorityLine(session *ChatSession, ctx context.Context) (string, error) {
	if session != nil && session.InputQueue != nil {
		return session.InputQueue.readPriorityLine(ctx)
	}
	return chatInteractiveReadLine(session, ctx)
}

func chatInputQueueHasQueuedLines(session *ChatSession) bool {
	if session == nil || session.InputQueue == nil {
		return false
	}
	return session.InputQueue.pendingCount() > 0
}

func lenQueuedInteractiveInput(session *ChatSession) int {
	if session == nil || session.InputQueue == nil {
		return 0
	}
	return session.InputQueue.pendingCount()
}

func queuedInteractiveInputState(session *ChatSession) (int, bool) {
	if session == nil {
		return 0, false
	}
	return lenQueuedInteractiveInput(session), session.queuedInputDrain
}

func discardQueuedInteractiveLines(session *ChatSession) int {
	if session == nil || session.InputQueue == nil {
		return 0
	}
	return session.InputQueue.discardPending()
}

func normalizeQueuedInputLine(line string) string {
	return strings.TrimRight(line, "\r\n")
}

func (q *chatInputQueue) ensureChannels() {
	if q.lines == nil {
		q.lines = make(chan chatQueuedInput, 32)
	}
	if q.priorityLines == nil {
		q.priorityLines = make(chan chatQueuedInput, 4)
	}
	if q.errs == nil {
		q.errs = make(chan error, 1)
	}
}

func (q *chatInputQueue) setPriorityMode(active bool) {
	if q == nil {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.priorityMode = active
}

func (q *chatInputQueue) routeLine(item chatQueuedInput) {
	if q == nil {
		return
	}
	q.ensureChannels()
	q.mu.RLock()
	priorityMode := q.priorityMode
	q.mu.RUnlock()
	if priorityMode {
		q.priorityLines <- item
		return
	}
	q.lines <- item
}
