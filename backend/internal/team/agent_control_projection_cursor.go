package team

import (
	"hash/fnv"
	"strings"
	"time"
)

func agentControlTeamProjectionSeq(teamID string, localSeq int64, createdAt time.Time) int64 {
	if localSeq <= 0 {
		return 0
	}
	millis := createdAt.UnixMilli()
	if millis <= 0 {
		millis = localSeq
	}
	return millis<<20 | int64(agentControlTeamProjectionBucket(teamID))<<12 | (localSeq & 0xfff)
}

func agentControlTeamProjectionBucket(teamID string) uint32 {
	teamID = strings.ToLower(strings.TrimSpace(teamID))
	if teamID == "" {
		return 0
	}
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(teamID))
	return hash.Sum32() & 0xff
}
