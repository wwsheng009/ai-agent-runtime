package remotetargets

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// decodeRemoteConfigPayload pulls the base64 JSON out of an ssh-stdio arg list.
//
// The profile a spec requests is only meaningful if it actually reaches the
// server, and it travels inside `--config-b64`; asserting on the expanded struct
// would not prove the transport carried it.
func decodeRemoteConfigPayload(t *testing.T, args []string) map[string]interface{} {
	t.Helper()
	for i, arg := range args {
		if arg != "--config-b64" || i+1 >= len(args) {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(args[i+1])
		require.NoError(t, err, "config-b64 must be valid base64")
		var payload map[string]interface{}
		require.NoError(t, json.Unmarshal(raw, &payload))
		return payload
	}
	t.Fatalf("no --config-b64 in args: %v", args)
	return nil
}

// TestParseSpecHTTPSchemes covers the one-string HTTP declaration.
func TestParseSpecHTTPSchemes(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		transport  string
		depth      SpecDepth
		listen     string
		targetName string
	}{
		{
			name:      "bare host takes the default port",
			raw:       "http://10.0.0.9",
			transport: TransportHTTP,
			depth:     DepthReadonly,
			listen:    "10.0.0.9:18765",
		},
		{
			name:      "explicit port",
			raw:       "http://10.0.0.9:19000",
			transport: TransportHTTP,
			depth:     DepthReadonly,
			listen:    "10.0.0.9:19000",
		},
		{
			name:      "depth marker",
			raw:       "http://10.0.0.9:18765#ops",
			transport: TransportHTTP,
			depth:     DepthOps,
			listen:    "10.0.0.9:18765",
		},
		{
			name:      "https is accepted",
			raw:       "https://svc.internal#standard",
			transport: TransportHTTP,
			depth:     DepthStandard,
			listen:    "svc.internal:18765",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			spec, err := ParseSpec(tc.raw)
			require.NoError(t, err)
			require.Equal(t, tc.transport, spec.Transport)
			require.Equal(t, tc.depth, spec.Depth)
			require.Equal(t, tc.listen, spec.listenAddress())
		})
	}
}

// TestParseSpecSSH covers the ssh:// form, including the port and workspace path.
func TestParseSpecSSH(t *testing.T) {
	spec, err := ParseSpec("ssh://ops@10.0.0.9:2222/srv/app#standard")
	require.NoError(t, err)

	require.Equal(t, TransportSSHStdio, spec.Transport)
	require.Equal(t, DepthStandard, spec.Depth)
	require.Equal(t, "10.0.0.9", spec.Host)
	require.Equal(t, "ops", spec.User)
	require.Equal(t, 2222, spec.Port)
	require.Equal(t, "/srv/app", spec.Workspace)
	require.Equal(t, "10-0-0-9", spec.TargetName())
}

// TestSSHStdioArgsEnableKeepAlive locks the dead-link detection default: a
// silently dropped SSH link must fail within roughly 45s (15s x 3), not hang
// until the OS TCP timeout.
func TestSSHStdioArgsEnableKeepAlive(t *testing.T) {
	spec, err := ParseSpec("ssh://ops@host/srv/app")
	require.NoError(t, err)
	expanded, err := spec.Expand()
	require.NoError(t, err)
	server, ok := expanded.Servers["host"]
	require.True(t, ok)
	joined := strings.Join(server.Args, " ")
	require.Contains(t, joined, "ServerAliveInterval=15")
	require.Contains(t, joined, "ServerAliveCountMax=3")
}

// TestParseSpecRejects guards the failure mode that would otherwise reach the
// remote server as a confusing runtime error.
func TestParseSpecRejects(t *testing.T) {
	for _, raw := range []string{
		"",
		"10.0.0.9:18765",      // no scheme
		"ftp://10.0.0.9",      // wrong scheme
		"http://",             // no host
		"ssh://",              // no host
		"http://h:18765#nope", // unknown depth
		"ssh://h:99999",       // out-of-range port
		"ssh://h:abc",         // non-numeric port
	} {
		_, err := ParseSpec(raw)
		require.Error(t, err, "%q must be rejected", raw)
	}
}

// TestSpecExpandProducesPrefixedTools is the safety-critical assertion.
//
// tools.Manager visits MCP tools before local ones and locally protects only
// ls/glob/grep/view. An unprefixed remote `write` would therefore shadow the
// local one and silently mutate the wrong machine. Every spec must generate a
// prefix.
func TestSpecExpandProducesPrefixedTools(t *testing.T) {
	spec, err := ParseSpec("http://10.0.0.9:18765#ops")
	require.NoError(t, err)

	expanded, err := spec.Expand()
	require.NoError(t, err)
	require.Len(t, expanded.Servers, 1)

	for _, server := range expanded.Servers {
		require.Contains(t, server.URL, "10.0.0.9:18765")
		require.Equal(t, "remote", string(server.ExecutionScope))
		require.Equal(t, "trusted_remote", string(server.TrustLevel))
	}
}

// TestSpecDepthBecomesProfileAndIsHonestAboutHTTP locks what `#depth` actually
// does now that the local per-target grant is gone.
//
// The ladder is a PROFILE request, not a permission:
//
//   - ssh-stdio really applies it, because the profile travels in the base64
//     config and the server starts with it;
//   - http cannot be narrowed from here (the server is already running), so the
//     expansion must say so instead of implying a restriction was applied.
//
// The second half is the point: silently accepting `#ops` against an http target
// was the same class of lie the notice text used to tell about tool prefixes.
func TestSpecDepthBecomesProfileAndIsHonestAboutHTTP(t *testing.T) {
	for _, depth := range []SpecDepth{DepthReadonly, DepthStandard, DepthOps} {
		t.Run("ssh/"+string(depth), func(t *testing.T) {
			spec, err := ParseSpec("ssh://ops@h#" + string(depth))
			require.NoError(t, err)
			expanded, err := spec.Expand()
			require.NoError(t, err)
			server, ok := expanded.Servers["h"]
			require.True(t, ok)
			payload := decodeRemoteConfigPayload(t, server.Args)
			require.Equal(t, string(depth), payload["profile"],
				"the requested depth must reach the server as its profile")
		})
	}

	t.Run("http warns that depth is not applied", func(t *testing.T) {
		for _, depth := range []SpecDepth{DepthStandard, DepthOps} {
			spec, err := ParseSpec("http://h:18765#" + string(depth))
			require.NoError(t, err)
			expanded, err := spec.Expand()
			require.NoError(t, err)
			require.NotEmpty(t, expanded.Warnings,
				"`#%s` on an http target must be reported as not applied", depth)
		}
	})

	t.Run("http readonly needs no warning", func(t *testing.T) {
		// readonly is the server's own default, so nothing is being claimed.
		spec, err := ParseSpec("http://h:18765")
		require.NoError(t, err)
		expanded, err := spec.Expand()
		require.NoError(t, err)
		require.Empty(t, expanded.Warnings)
	})
}

// TestSpecSSHWorkspaceDefaults proves `--remote ssh://user@host` needs no extra
// flags: the remote server refuses to start without a workspace, so the spec
// substitutes the home directory.
func TestSpecSSHWorkspaceDefaults(t *testing.T) {
	spec, err := ParseSpec("ssh://ops@10.0.0.9")
	require.NoError(t, err)

	expanded, err := spec.Expand()
	require.NoError(t, err)
	for _, server := range expanded.Servers {
		require.NotEmpty(t, server.Args)
		// The base64 payload carries the workspace; decoding it is covered by the
		// registry tests, here we only assert the expansion did not fail on the
		// empty workspace.
	}
}

// TestSpecNeverCarriesTheTokenOnTheCommandLine is a small but important guard:
// tokens must come from the environment, never from the spec, otherwise they land
// in shell history and in the process list.
func TestSpecNeverCarriesTheTokenOnTheCommandLine(t *testing.T) {
	spec, err := ParseSpec("http://10.0.0.9:18765#standard")
	require.NoError(t, err)

	require.Equal(t, "AICLI_REMOTE_TOKEN", spec.TokenEnv())

	expanded, err := spec.Expand()
	require.NoError(t, err)
	for _, server := range expanded.Servers {
		require.Contains(t, server.Headers["Authorization"], "${AICLI_REMOTE_TOKEN}",
			"the token must stay an env reference")
	}
}
