package commands

import (
	"context"
	"net"
	"strconv"
	"strings"
	"sync"

	gatewayagent "github.com/wwsheng009/ai-agent-runtime/internal/gateway/agent"
)

var (
	chatGatewayEndpointMu   sync.RWMutex
	chatGatewayLocalBaseURL string
	chatGatewayLocalToken   string
)

// SetChatGatewayLoopbackEndpoint records this process's loopback web base URL
// and write token so the gateway agent can reverse-proxy to it. It is called
// from cmd/aicli once the loopback server is listening. A wildcard host is
// normalized to 127.0.0.1 (the local service only trusts loopback).
//
// If a chat session already exists, its controller is updated immediately and
// reconnected when it is already connected (so the new target takes effect
// without a manual reconnect).
func SetChatGatewayLoopbackEndpoint(host string, port int, token string) {
	if port <= 0 {
		return
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	base := "http://" + net.JoinHostPort(host, strconv.Itoa(port))

	chatGatewayEndpointMu.Lock()
	chatGatewayLocalBaseURL = base
	chatGatewayLocalToken = strings.TrimSpace(token)
	chatGatewayEndpointMu.Unlock()

	session := chatDebugDisplaySession()
	if session == nil || session.Gateway == nil {
		return
	}
	session.Gateway.SetForward(chatGatewayForwardConfig())
	if session.Gateway.Status().Connected() {
		go func() { _ = session.Gateway.Reconnect(context.Background()) }()
	}
}

// chatGatewayForwardConfig returns the recorded local forwarding target.
func chatGatewayForwardConfig() gatewayagent.ForwardConfig {
	chatGatewayEndpointMu.RLock()
	defer chatGatewayEndpointMu.RUnlock()
	return gatewayagent.ForwardConfig{
		LocalBaseURL: chatGatewayLocalBaseURL,
		LocalToken:   chatGatewayLocalToken,
	}
}
