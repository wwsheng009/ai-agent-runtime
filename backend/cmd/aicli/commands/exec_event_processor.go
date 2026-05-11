package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
)

type ExecEventProcessor interface {
	PrintConfigSummary(opts *ExecOptions, model string, provider string)
	OnThreadStarted(event ThreadStartedEvent)
	OnTurnStarted(event TurnStartedEvent)
	OnTurnCompleted(event TurnCompletedEvent)
	OnTurnFailed(event TurnFailedEvent)
	OnItemStarted(event ItemStartedEvent)
	OnItemUpdated(event ItemUpdatedEvent)
	OnItemCompleted(event ItemCompletedEvent)
	OnError(event ErrorEvent)
	OnWarning(message string)
	OnStreamDelta(delta string)
	SetFinalMessage(message string)
	SetFinalResult(result ExecFinalResult)
	GetFinalMessage() string
	PrintFinalOutput(opts *ExecOptions) error
}

func NewExecEventProcessor(jsonMode bool, writer io.Writer, lastMessageFile string) ExecEventProcessor {
	if writer == nil {
		if jsonMode {
			writer = os.Stdout
		} else {
			writer = os.Stderr
		}
	}
	if jsonMode {
		return newJSONLEventProcessor(writer, lastMessageFile)
	}
	return newHumanEventProcessor(writer, lastMessageFile)
}

type JSONLEventProcessor struct {
	writer          io.Writer
	lastMessageFile string
	finalMessage    string
	finalResult     ExecFinalResult
	threadID        string
	sequence        int64
	mu              sync.Mutex
}

func newJSONLEventProcessor(writer io.Writer, lastMessageFile string) *JSONLEventProcessor {
	return &JSONLEventProcessor{writer: writer, lastMessageFile: lastMessageFile}
}

func (p *JSONLEventProcessor) emit(eventType string, data interface{}) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sequence++
	event := newThreadEvent(p.sequence, p.threadID, eventType, data)
	encoded, err := json.Marshal(event)
	if err != nil {
		return
	}
	fmt.Fprintln(p.writer, string(encoded))
}

func (p *JSONLEventProcessor) PrintConfigSummary(opts *ExecOptions, model, provider string) {}

func (p *JSONLEventProcessor) OnThreadStarted(event ThreadStartedEvent) {
	p.mu.Lock()
	p.threadID = event.ThreadID
	p.mu.Unlock()
	p.emit(EventTypeThreadStarted, event)
}

func (p *JSONLEventProcessor) OnTurnStarted(event TurnStartedEvent) {
	p.emit(EventTypeTurnStarted, event)
}

func (p *JSONLEventProcessor) OnTurnCompleted(event TurnCompletedEvent) {
	p.emit(EventTypeTurnCompleted, event)
}

func (p *JSONLEventProcessor) OnTurnFailed(event TurnFailedEvent) {
	p.emit(EventTypeTurnFailed, event)
}

func (p *JSONLEventProcessor) OnItemStarted(event ItemStartedEvent) {
	p.emit(EventTypeItemStarted, event)
}

func (p *JSONLEventProcessor) OnItemUpdated(event ItemUpdatedEvent) {
	p.emit(EventTypeItemUpdated, event)
}

func (p *JSONLEventProcessor) OnItemCompleted(event ItemCompletedEvent) {
	p.emit(EventTypeItemCompleted, event)
}

func (p *JSONLEventProcessor) OnError(event ErrorEvent) {
	p.emit(EventTypeError, event)
}

func (p *JSONLEventProcessor) OnWarning(message string) {
	p.emit(EventTypeWarning, ErrorEvent{Message: message})
}

func (p *JSONLEventProcessor) OnStreamDelta(delta string) {}

func (p *JSONLEventProcessor) SetFinalMessage(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finalMessage = message
	p.finalResult.Message = message
}

func (p *JSONLEventProcessor) SetFinalResult(result ExecFinalResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finalResult = result
	p.finalMessage = result.Message
}

func (p *JSONLEventProcessor) GetFinalMessage() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finalMessage
}

func (p *JSONLEventProcessor) PrintFinalOutput(opts *ExecOptions) error {
	return writeExecLastMessageFile(p.lastMessageFile, p.GetFinalMessage(), p.OnWarning)
}

type HumanEventProcessor struct {
	writer          io.Writer
	lastMessageFile string
	finalMessage    string
	finalResult     ExecFinalResult
	theme           *ui.Theme
	isTTY           bool
	mu              sync.Mutex
}

func newHumanEventProcessor(writer io.Writer, lastMessageFile string) *HumanEventProcessor {
	isTTY := false
	if f, ok := writer.(*os.File); ok {
		if stat, err := f.Stat(); err == nil {
			isTTY = stat.Mode()&os.ModeCharDevice != 0
		}
	}
	return &HumanEventProcessor{
		writer:          writer,
		lastMessageFile: lastMessageFile,
		theme:           ui.GetTheme(ui.ThemeAuto),
		isTTY:           isTTY,
	}
}

func (p *HumanEventProcessor) PrintConfigSummary(opts *ExecOptions, model, provider string) {
	if !p.isTTY {
		return
	}
	fmt.Fprintf(p.writer, "%s %s/%s\n", p.theme.MetaLabelColor.Sprint("Model:"), provider, model)
	if opts != nil && opts.PermissionMode != "" {
		fmt.Fprintf(p.writer, "%s %s\n", p.theme.MetaLabelColor.Sprint("Permission:"), string(opts.PermissionMode))
	}
}

func (p *HumanEventProcessor) OnThreadStarted(event ThreadStartedEvent) {}

func (p *HumanEventProcessor) OnTurnStarted(event TurnStartedEvent) {
	if p.isTTY {
		fmt.Fprintf(p.writer, "%s\n", p.theme.MutedColor.Sprint("Thinking..."))
	}
}

func (p *HumanEventProcessor) OnTurnCompleted(event TurnCompletedEvent) {
	if p.isTTY {
		fmt.Fprint(p.writer, "\r\033[K")
	}
}

func (p *HumanEventProcessor) OnTurnFailed(event TurnFailedEvent) {
	fmt.Fprintf(p.writer, "%s %s\n", p.theme.ErrorColor.Sprint("Error:"), event.Error)
}

func (p *HumanEventProcessor) OnItemStarted(event ItemStartedEvent) {
	if event.ItemType != "tool_call" {
		return
	}
	if details, ok := event.Details.(ToolCallDetails); ok {
		fmt.Fprintf(p.writer, "%s %s\n", p.theme.ToolColor.Sprint(p.theme.CommandIcon), details.ToolName)
	}
}

func (p *HumanEventProcessor) OnItemUpdated(event ItemUpdatedEvent) {}

func (p *HumanEventProcessor) OnItemCompleted(event ItemCompletedEvent) {}

func (p *HumanEventProcessor) OnError(event ErrorEvent) {
	fmt.Fprintf(p.writer, "%s %s\n", p.theme.ErrorColor.Sprint("Error:"), event.Message)
}

func (p *HumanEventProcessor) OnWarning(message string) {
	fmt.Fprintf(p.writer, "%s %s\n", p.theme.WarningColor.Sprint("Warning:"), message)
}

func (p *HumanEventProcessor) OnStreamDelta(delta string) {
	fmt.Fprint(p.writer, delta)
}

func (p *HumanEventProcessor) SetFinalMessage(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finalMessage = message
	p.finalResult.Message = message
}

func (p *HumanEventProcessor) SetFinalResult(result ExecFinalResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.finalResult = result
	p.finalMessage = result.Message
}

func (p *HumanEventProcessor) GetFinalMessage() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.finalMessage
}

func (p *HumanEventProcessor) PrintFinalOutput(opts *ExecOptions) error {
	p.mu.Lock()
	message := p.finalMessage
	result := p.finalResult
	p.mu.Unlock()

	if opts != nil && opts.OutputFormat == "json" {
		printCommandJSONOutput("exec", opts.JSONEnvelope, result)
	} else if message != "" {
		fmt.Fprintln(os.Stdout, message)
	}
	return writeExecLastMessageFile(p.lastMessageFile, message, p.OnWarning)
}

func writeExecLastMessageFile(path string, message string, warn func(string)) error {
	if path == "" || message == "" {
		return nil
	}
	if err := os.WriteFile(path, []byte(message), 0644); err != nil {
		if warn != nil {
			warn(fmt.Sprintf("写入最后消息文件失败: %v", err))
		}
		return err
	}
	return nil
}
