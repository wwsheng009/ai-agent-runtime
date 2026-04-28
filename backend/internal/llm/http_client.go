package llm

import (
	"net/http"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	runtimehttpclient "github.com/wwsheng009/ai-agent-runtime/internal/pkg/httpclient"
)

func newProviderHTTPClient(
	timeout time.Duration,
	proxyCfg *agentconfig.ProxyConfig,
	stream bool,
) *http.Client {
	return runtimehttpclient.NewProviderHTTPClient(timeout, proxyCfg, stream)
}
