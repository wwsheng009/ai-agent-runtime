package commands

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport"
	"github.com/wwsheng009/ai-agent-runtime/internal/sessionport/registry"
)

type transferInteractiveSelection struct {
	Agent      string
	Root       string
	InstanceID string
	SessionID  string
}

type transferPrompt struct {
	in  *bufio.Reader
	out io.Writer
}

func newTransferPrompt(cmd *cobra.Command) *transferPrompt {
	return &transferPrompt{
		in:  bufio.NewReader(cmd.InOrStdin()),
		out: cmd.OutOrStdout(),
	}
}

func (p *transferPrompt) readLine(prompt string) (string, error) {
	fmt.Fprint(p.out, prompt)
	line, err := p.in.ReadString('\n')
	if err != nil && len(line) == 0 {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func (p *transferPrompt) choose(prompt string, count int) (int, error) {
	if count <= 0 {
		return 0, fmt.Errorf("没有可选择的项目")
	}
	if count == 1 {
		return 0, nil
	}
	for {
		line, err := p.readLine(fmt.Sprintf("%s [1-%d，默认 1]: ", prompt, count))
		if err != nil {
			return 0, fmt.Errorf("读取选择失败: %w", err)
		}
		if line == "" {
			return 0, nil
		}
		value, err := strconv.Atoi(line)
		if err == nil && value >= 1 && value <= count {
			return value - 1, nil
		}
		fmt.Fprintf(p.out, "无效选择 %q，请输入 1-%d。\n", line, count)
	}
}

func (p *transferPrompt) confirm(prompt string, defaultYes bool) (bool, error) {
	suffix := "[y/N]"
	if defaultYes {
		suffix = "[Y/n]"
	}
	for {
		line, err := p.readLine(fmt.Sprintf("%s %s: ", prompt, suffix))
		if err != nil {
			return false, fmt.Errorf("读取确认失败: %w", err)
		}
		switch strings.ToLower(line) {
		case "":
			return defaultYes, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		default:
			fmt.Fprintln(p.out, "请输入 y 或 n。")
		}
	}
}

func selectTransferInteractive(prompt *transferPrompt, agent string, roots []string, instanceFlag, dbPath, projectsDir, zipPath, sessionID string, latest bool) (transferInteractiveSelection, error) {
	if strings.TrimSpace(zipPath) != "" {
		inspected, err := registry.InspectZip(zipPath)
		if err != nil {
			return transferInteractiveSelection{}, err
		}
		refs := inspected.Sessions
		for _, warning := range inspected.Warnings {
			fmt.Fprintf(prompt.out, "ZIP 警告: %s\n", warning)
		}
		if sessionID != "" {
			ref, ok := findTransferRef(refs, sessionID)
			if !ok {
				return transferInteractiveSelection{}, fmt.Errorf("ZIP 中找不到会话: %s", sessionID)
			}
			printTransferInteractiveSessionHeader(prompt.out, "DSH ZIP", zipPath)
			printTransferInspectRefs([]sessionport.SessionRef{ref})
			return transferInteractiveSelection{Agent: string(sessionport.AgentDeepSeekHarness), SessionID: ref.SessionID}, nil
		}
		selected, err := chooseTransferInteractiveSession(prompt, "DSH ZIP", zipPath, refs, latest)
		if err != nil {
			return transferInteractiveSelection{}, err
		}
		return transferInteractiveSelection{Agent: string(sessionport.AgentDeepSeekHarness), SessionID: selected.SessionID}, nil
	}

	instances, err := transferInspectInstances(agent, roots, instanceFlag, dbPath, projectsDir)
	if err != nil {
		return transferInteractiveSelection{}, err
	}
	if len(instances) == 0 {
		return transferInteractiveSelection{}, fmt.Errorf("未发现可用的 Agent 实例；请设置 DSH_HOME/WORKBUDDY_HOME 或传 --root")
	}
	instance, err := chooseTransferInteractiveInstance(prompt, instances)
	if err != nil {
		return transferInteractiveSelection{}, err
	}
	refs, err := registry.List(instance)
	if err != nil {
		return transferInteractiveSelection{}, err
	}
	if len(refs) == 0 {
		return transferInteractiveSelection{}, fmt.Errorf("%s 实例没有可用会话", instance.InstanceID)
	}
	selected, err := chooseTransferInteractiveSession(prompt, instance.InstanceID, instance.Root, refs, latest)
	if err != nil {
		return transferInteractiveSelection{}, err
	}
	return transferInteractiveSelection{
		Agent:      instance.AgentID,
		Root:       instance.Root,
		InstanceID: instance.InstanceID,
		SessionID:  selected.SessionID,
	}, nil
}

func chooseTransferInteractiveInstance(prompt *transferPrompt, instances []sessionport.Instance) (sessionport.Instance, error) {
	if len(instances) == 1 {
		instance := instances[0]
		fmt.Fprintf(prompt.out, "使用实例: %s [%s]\n", instance.InstanceID, instance.AgentID)
		return instance, nil
	}
	fmt.Fprintln(prompt.out, "发现多个 Agent 实例:")
	for index, instance := range instances {
		fmt.Fprintf(prompt.out, "  [%d] %s [%s]\n", index+1, instance.InstanceID, instance.AgentID)
		fmt.Fprintf(prompt.out, "      root: %s\n", instance.Root)
		fmt.Fprintf(prompt.out, "      format: v%d %s, sessions=%d\n",
			instance.FormatVersion, blankTransferValue(instance.FormatStatus), instance.SessionCount)
		for _, warning := range instance.Warnings {
			fmt.Fprintf(prompt.out, "      warning: %s\n", warning)
		}
	}
	index, err := prompt.choose("选择实例", len(instances))
	if err != nil {
		return sessionport.Instance{}, err
	}
	return instances[index], nil
}

func chooseTransferInteractiveSession(prompt *transferPrompt, source, root string, refs []sessionport.SessionRef, latest bool) (sessionport.SessionRef, error) {
	if len(refs) == 0 {
		return sessionport.SessionRef{}, fmt.Errorf("没有可用会话")
	}
	if latest {
		selected := refs[0]
		for _, ref := range refs[1:] {
			if ref.UpdatedAt.After(selected.UpdatedAt) {
				selected = ref
			}
		}
		fmt.Fprintf(prompt.out, "使用最近会话: %s\n", selected.SessionID)
		return selected, nil
	}
	if len(refs) == 1 {
		printTransferInteractiveSessionHeader(prompt.out, source, root)
		printTransferInspectRefs(refs)
		return refs[0], nil
	}
	printTransferInteractiveSessionHeader(prompt.out, source, root)
	for index, ref := range refs {
		title := strings.TrimSpace(ref.Title)
		if title == "" {
			title = "(untitled)"
		}
		fmt.Fprintf(prompt.out, "  [%d] %s  %s  v%d/%s  %s\n",
			index+1, ref.SessionID, blankTransferValue(ref.FormatStatus), ref.FormatVersion,
			blankTransferValue(ref.Compression), title)
	}
	index, err := prompt.choose("选择会话", len(refs))
	if err != nil {
		return sessionport.SessionRef{}, err
	}
	return refs[index], nil
}

func printTransferInteractiveSessionHeader(out io.Writer, source, root string) {
	fmt.Fprintf(out, "源: %s\n", source)
	if root != "" {
		fmt.Fprintf(out, "根: %s\n", root)
	}
}

func findTransferRef(refs []sessionport.SessionRef, sessionID string) (sessionport.SessionRef, bool) {
	for _, ref := range refs {
		if ref.SessionID == sessionID {
			return ref, true
		}
	}
	return sessionport.SessionRef{}, false
}
