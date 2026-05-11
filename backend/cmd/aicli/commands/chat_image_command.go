package commands

import (
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/spf13/cobra"
)

func handleImageGenerationCommand(session *ChatSession, command string) bool {
	if session == nil {
		fmt.Println("错误: 当前没有活动会话")
		return false
	}

	req, outputOptions, err := parseChatImageGenerationCommand(session, command)
	if err != nil {
		fmt.Println(formatCommandError("错误: "+err.Error(), chatImageGenerationWantsJSON(session, command)))
		return false
	}

	result, details, err := runImageGenerateCommand(req)
	if err != nil {
		if isJSONOutputFormat(outputOptions.Format) {
			fmt.Println(marshalCompactJSONLine(commandErrorPayload{
				OK:      false,
				Command: "image",
				Error:   err.Error(),
				Details: details,
			}))
		} else {
			fmt.Printf("错误: %v\n", err)
		}
		return false
	}
	renderImageGenerateCommandResult(result, outputOptions)
	return false
}

func parseChatImageGenerationCommand(session *ChatSession, command string) (imageGenerateCommandRequest, structuredOutputOptions, error) {
	rawArgs := extractCommandArgument(command)
	args, err := splitChatImageGenerationArgs(rawArgs)
	if err != nil {
		return imageGenerateCommandRequest{}, structuredOutputOptions{}, err
	}

	cmd := NewImageCommand(nil)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	if err := cmd.Flags().Parse(args); err != nil {
		return imageGenerateCommandRequest{}, structuredOutputOptions{}, err
	}

	outputOptions, err := resolveStructuredOutputOptions(cmd, "text", "text", "json")
	if err != nil {
		return imageGenerateCommandRequest{}, structuredOutputOptions{}, err
	}
	if shouldUseSessionJSONCommandOutput(session) && !flagChanged(cmd, "output") && !flagChanged(cmd, "json") {
		outputOptions.Format = "json"
		outputOptions.Envelope = session.JSONEnvelope
	}

	req := chatImageGenerationRequestFromParsedCommand(session, cmd, cmd.Flags().Args())
	return req, outputOptions, nil
}

func chatImageGenerationRequestFromParsedCommand(session *ChatSession, cmd *cobra.Command, args []string) imageGenerateCommandRequest {
	req := imageGenerateCommandRequest{
		Config:       nil,
		Session:      session,
		Prompt:       imagePromptFromFlags(cmd, args),
		Provider:     stringFlag(cmd, "provider"),
		Model:        stringFlag(cmd, "model"),
		Path:         stringFlag(cmd, "path"),
		N:            intFlag(cmd, "n"),
		Size:         stringFlag(cmd, "size"),
		Quality:      stringFlag(cmd, "quality"),
		Background:   stringFlag(cmd, "background"),
		OutputFormat: stringFlag(cmd, "output-format"),
		OutputDir:    stringFlag(cmd, "output-dir"),
		Debug:        boolFlag(cmd, "debug"),
		DebugWriter:  nil,
	}
	if session != nil {
		req.Config = session.Config
		req.Timeout = session.RequestTimeout
	}
	if flagChanged(cmd, "timeout") {
		req.Timeout = time.Duration(intFlag(cmd, "timeout")) * time.Second
	}
	if req.Debug {
		req.DebugWriter = nil
	}
	if flagChanged(cmd, "output-compression") {
		value := intFlag(cmd, "output-compression")
		req.OutputCompression = &value
	}
	return req
}

func splitChatImageGenerationArgs(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, nil
	}

	args := make([]string, 0, 8)
	var current strings.Builder
	quote := rune(0)
	tokenStarted := false

	flush := func() {
		if !tokenStarted {
			return
		}
		args = append(args, current.String())
		current.Reset()
		tokenStarted = false
	}

	for _, r := range input {
		if quote != 0 {
			if r == quote {
				quote = 0
				tokenStarted = true
				continue
			}
			current.WriteRune(r)
			tokenStarted = true
			continue
		}

		switch {
		case r == '\'' || r == '"':
			quote = r
			tokenStarted = true
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
			tokenStarted = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("参数引号未闭合")
	}
	flush()
	return args, nil
}

func chatImageGenerationWantsJSON(session *ChatSession, command string) bool {
	if shouldUseSessionJSONCommandOutput(session) {
		return true
	}
	normalized := strings.ToLower(command)
	return strings.Contains(normalized, "--json") ||
		strings.Contains(normalized, "--output json") ||
		strings.Contains(normalized, "--output=json")
}
