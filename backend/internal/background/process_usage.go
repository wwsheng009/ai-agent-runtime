package background

// processUsage is one instantaneous sample of a running process's resource
// usage. CPUSeconds is cumulative CPU time (user+kernel); callers derive a
// percentage from the delta between two samples (2026-10-11).
type processUsage struct {
	RSSKB      int64
	CPUSeconds float64
	OK         bool
}

// sampleProcessUsage reads RSS and cumulative CPU time for a pid. It is
// implemented per platform (process_usage_<os>.go); unsupported platforms
// return OK=false.
