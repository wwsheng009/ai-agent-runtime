package agent

import (
	"strings"
)

// LiveUserInputBatch is a one-shot batch of user messages made available to an
// active ReAct run. Restore is called only when the loop cannot persist the
// batch before the next model request; after a successful checkpoint, ownership
// of the messages has transferred to the session history.
type LiveUserInputBatch struct {
	Inputs  []string
	Restore func()
}

// LiveUserInputSource is called at a ReAct step boundary immediately before
// constructing the next model request. An empty batch means that no live input
// is waiting.
type LiveUserInputSource func() LiveUserInputBatch

func normalizeLiveUserInputs(inputs []string) []string {
	if len(inputs) == 0 {
		return nil
	}
	out := make([]string, 0, len(inputs))
	for _, input := range inputs {
		if input = strings.TrimSpace(input); input != "" {
			out = append(out, input)
		}
	}
	return out
}
