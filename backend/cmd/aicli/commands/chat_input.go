package commands

import (
	"bufio"
	"os"
)

var newChatInputReader = func() *bufio.Reader {
	return bufio.NewReader(os.Stdin)
}

func chatOptionInputReader(opts *chatCommandOptions) *bufio.Reader {
	if opts == nil {
		return newChatInputReader()
	}
	if opts.InputReader == nil {
		opts.InputReader = newChatInputReader()
	}
	return opts.InputReader
}

func chatSessionInputReader(session *ChatSession) *bufio.Reader {
	if session == nil {
		return newChatInputReader()
	}
	if session.InputReader == nil {
		session.InputReader = newChatInputReader()
	}
	return session.InputReader
}
