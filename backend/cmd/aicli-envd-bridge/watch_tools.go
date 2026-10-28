package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/envdclient"
)

const (
	watchDefaultWait = 5 * time.Second
	watchMaxWait     = 30 * time.Second
	watchDefaultMax  = 50
	watchMaxEvents   = 500
)

// toolWatchDir captures a bounded window of directory-change events.
//
// envd's WatchDir is a server stream with no natural end. Exposing it directly
// would leave a stream (and a goroutine on the target) alive after the MCP call,
// so this tool owns the stream for one call: it waits up to wait_ms, returns at
// most max_events, then closes the stream. Continuous monitoring needs repeated
// calls or a future session-style tool.
func (b *bridge) toolWatchDir(ctx context.Context, args json.RawMessage) callResult {
	var in struct {
		Path      string `json:"path"`
		WaitMS    int64  `json:"wait_ms"`
		MaxEvents int    `json:"max_events"`
	}
	if err := decodeArgs(args, &in); err != nil {
		return errorResult("invalid arguments: %v", err)
	}
	dir, err := b.resolvePath(ctx, in.Path)
	if err != nil {
		return errorResult("%v", err)
	}

	wait := watchDefaultWait
	if in.WaitMS > 0 {
		wait = time.Duration(in.WaitMS) * time.Millisecond
	}
	if wait > watchMaxWait {
		wait = watchMaxWait
	}
	maxEvents := in.MaxEvents
	if maxEvents <= 0 {
		maxEvents = watchDefaultMax
	}
	if maxEvents > watchMaxEvents {
		maxEvents = watchMaxEvents
	}

	watchCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	stream, err := b.client.WatchDir(watchCtx, dir)
	if err != nil {
		return errorResult("watch %s: %v", dir, err)
	}
	defer func() { _ = stream.Close() }()

	started := time.Now()
	events := make([]envdclient.WatchEvent, 0, maxEvents)
	streamEnded := false
	var streamErr error
	for len(events) < maxEvents {
		var event envdclient.WatchEvent
		err := stream.Recv(&event)
		if err != nil {
			switch {
			case errors.Is(err, io.EOF):
				streamEnded = true
			case watchCtx.Err() != nil:
				// The bounded window expired; this is a normal stop condition.
			default:
				streamErr = err
			}
			break
		}
		if event.Start {
			continue
		}
		events = append(events, event)
	}

	var sb strings.Builder
	if len(events) == 0 {
		switch {
		case streamErr != nil:
			return errorResult("watch %s: %v", dir, streamErr)
		case streamEnded:
			fmt.Fprintf(&sb, "watch %s: stream closed before any change event", dir)
		default:
			fmt.Fprintf(&sb, "watch %s: no change events within %s", dir, wait.Round(time.Millisecond))
		}
		return textResult("%s", sb.String())
	}

	fmt.Fprintf(&sb, "watch %s: %d event(s) in %s\n", dir, len(events), time.Since(started).Round(time.Millisecond))
	for _, event := range events {
		kind := event.Type
		if kind == "" {
			kind = "(unknown)"
		}
		fmt.Fprintf(&sb, "%s %q\n", kind, event.Name)
	}
	if streamErr != nil {
		fmt.Fprintf(&sb, "[stream error: %v]", streamErr)
	}
	return textResult("%s", strings.TrimRight(sb.String(), "\n"))
}
