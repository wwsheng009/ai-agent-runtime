package tools

import "github.com/wwsheng009/ai-agent-runtime/internal/shelljob"

// ShellJobRuntime aliases the shared shell-job seam. The production
// implementation is shelljob.ManagerRuntime; there is no second backend.
type ShellJobRuntime = shelljob.Runtime

// ShellJobSnapshot is the terminal or timed-out view of one job.
type ShellJobSnapshot = shelljob.Snapshot

// ShellJobOutput is one cursor read from a job.
type ShellJobOutput = shelljob.Output
