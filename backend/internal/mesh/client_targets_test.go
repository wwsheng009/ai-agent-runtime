package mesh

import (
	"os"
	"testing"
)

// TestPeerTargetsMatchesViewAggregation locks the fast peer-discovery path to
// the existing BuildView semantics: same live/stale gate, capabilities, token,
// workspace preference and cap.
func TestPeerTargetsMatchesViewAggregation(t *testing.T) {
	resetMeshFileCaches()
	paths := testPaths(t)
	now := NowUTC()
	dead := deadPID(t)

	mk := func(nodeID string, pid int, workspace, baseURL, token string, events, loopback bool) {
		record := viewNodeRecord(nodeID, pid, now, "session-"+nodeID, workspace)
		record.Endpoint = &EndpointInfo{BaseURL: baseURL, Loopback: loopback}
		record.Auth = &AuthInfo{Token: token, Required: true}
		if events {
			record.Capabilities = append(record.Capabilities, "events")
		}
		writeViewNode(t, paths, record)
	}
	mk("node-a", os.Getpid(), `E:\ws\one`, "http://127.0.0.1:61001", "token-a", true, true)
	mk("node-b", os.Getpid(), `E:\ws\two`, "http://127.0.0.1:61002", "token-b", true, true)
	mk("node-c", os.Getpid(), `E:\ws\one`, "http://127.0.0.1:61003", "token-c", false, true)
	mk("node-dead", dead, `E:\ws\one`, "http://127.0.0.1:61004", "token-dead", true, true)
	mk("node-nonloop", os.Getpid(), `E:\ws\one`, "http://10.0.0.2:61005", "token-remote", true, false)

	for _, includeStale := range []bool{false, true} {
		opts := PeerTargetOptions{
			SelfNodeID:      "node-a",
			MaxPeers:        10,
			IncludeStale:    includeStale,
			PreferWorkspace: `E:\ws\one`,
			Now:             now,
			HeartbeatTTL:    DefaultHeartbeatTTL,
		}
		view := BuildView(paths, ViewOptions{SelfNodeID: opts.SelfNodeID, Now: now})
		fromView := PeerTargetsFromView(view, opts)
		direct := PeerTargets(paths, opts)
		if len(fromView) != len(direct) {
			t.Fatalf("includeStale=%v: targets len view=%d direct=%d", includeStale, len(fromView), len(direct))
		}
		for index := range fromView {
			if fromView[index] != direct[index] {
				t.Fatalf("includeStale=%v target[%d] view=%+v direct=%+v",
					includeStale, index, fromView[index], direct[index])
			}
		}
	}
}
