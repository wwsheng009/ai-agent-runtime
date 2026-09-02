package commands

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui"
	outputpkg "github.com/wwsheng009/ai-agent-runtime/cmd/aicli/ui/render/output"
)

// NewReplayCommand builds the `aicli replay <file>` subcommand.
//
// B2 场景：会话录屏 + 离线回放。读取 ReplayArchive 文件（JSON，每个 entry
// 带 schema/checksum），fail-closed 校验后回放到 VirtualTerminalSink，
// 输出文本投影（Rows）。replay 进程不打开任何 physical/process writer——
// primary 是 virtual sink，永不触达 console。
func NewReplayCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "replay <file>",
		Short: "离线回放会话录屏到虚拟终端（不触达 console）",
		Long: `读取 ReplayArchive 录屏文件（JSON，entry 带 schema/checksum），
按 fail-closed 规则校验后回放到 VirtualTerminalSink，输出屏幕文本投影。

支持两种输入：
  - JSON ReplayArchive 文件（完整 schema/checksum 校验）；
  - 裸 wire ANSI 字节文件（--render-output-file 产物），自动包装为
    单条 synthetic committed entry 回放。

--replay-verify 只校验不执行回放；校验失败返回非零退出码。`,
		Example: `  aicli replay session.archive.json
  aicli replay --replay-verify session.archive.json
  aicli replay --render-output-file 录制的 mirror.ans`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return handleReplayCommand(cmd, args[0])
		},
	}
	cmd.Flags().Bool("replay-verify", false,
		"只校验 archive 完整性（schema/checksum），不执行回放；失败返回非零退出码")
	return cmd
}

func handleReplayCommand(cmd *cobra.Command, filePath string) error {
	verifyOnly, _ := cmd.Flags().GetBool("replay-verify")
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	// 1. 读取并解码 archive（JSON 优先；裸 wire bytes 自动包装）。
	file, err := readReplayInputFile(filePath, errOut)
	if err != nil {
		return fmt.Errorf("replay: %w", err)
	}

	// 2. 全量 fail-closed 校验（schema/checksum/provenance）。
	valid, firstErr := outputpkg.VerifyReplayArchive(file, outputpkg.ReplayCommittedWire)
	if firstErr != nil {
		return fmt.Errorf("replay: archive verification failed: %w", firstErr)
	}

	if verifyOnly {
		fmt.Fprintf(out, "replay: archive verified: %d entries OK\n", valid)
		return nil
	}

	// 3. 回放到 virtual terminal（不触达 physical/process writer）。
	emu := ui.NewVtTerminalEmulator()
	virtual := outputpkg.NewVirtualTerminalSink("replay-virtual", emu, outputpkg.VirtualSinkOptions{})
	for i := range file.Entries {
		env, err := outputpkg.ReplayEnvelopeFromArchive(file.Entries[i], outputpkg.ReplayCommittedWire)
		if err != nil {
			return fmt.Errorf("replay entry %d: %w", i, err)
		}
		if err := replayEnvelopeToVirtual(virtual, env, i, errOut); err != nil {
			return fmt.Errorf("replay entry %d: %w", i, err)
		}
	}

	// 4. 输出文本投影。
	printReplayProjection(out, virtual.Projection())
	return nil
}

// replayEnvelopeToVirtual 把单个 envelope 的 payload 提交到 virtual sink。
// geometry 缺失（raw 输入）时回退 80x24 并警告。
func replayEnvelopeToVirtual(virtual *outputpkg.VirtualTerminalSink, env *outputpkg.ReplayEnvelope, index int, errOut io.Writer) error {
	intent := env.ReplayBatchFromEnvelope(fmt.Sprintf("replay-%d-%s", index, env.ReplayRecordID()))
	if intent.Terminal.Geometry.Width < 1 || intent.Terminal.Geometry.Height < 1 {
		fmt.Fprintf(errOut, "Info: replay entry %d: missing geometry, using 80x24\n", index)
		intent.Terminal.Geometry.Width = 80
		intent.Terminal.Geometry.Height = 24
		if intent.Terminal.Profile.ID == "" {
			intent.Terminal.Profile.ID = "ansi"
			intent.Terminal.Profile.Version = 1
		}
	}
	batch := outputpkg.RenderBatch{
		RenderIntent:          intent,
		SessionID:             env.Provenance.SourceSessionID,
		Sequence:              env.Provenance.SourceSequence,
		BatchID:               env.Record.Output.BatchID,
		RouteEpoch:            env.Provenance.SourceRouteEpoch,
		ProjectionTargetID:    "replay-virtual",
		ProjectionTargetClass: outputpkg.TargetClassVirtual,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res := virtual.Submit(ctx, batch)
	if res.Status != outputpkg.DeliveryCommitted {
		return fmt.Errorf("virtual submit: %s/%s (%s)", res.Status, res.ErrorClass, res.Err)
	}
	return nil
}

// printReplayProjection 把虚拟终端投影写入 out（协议输出，纯文本）。
func printReplayProjection(out io.Writer, proj outputpkg.VirtualProjectionSnapshot) {
	fmt.Fprintf(out, "=== Replay Projection (%dx%d, validity=%s) ===\n",
		proj.Width, proj.Height, proj.Validity)
	for _, row := range proj.Rows {
		fmt.Fprintf(out, "%s\n", row)
	}
	if len(proj.Scrollback) > 0 {
		fmt.Fprintf(out, "--- Scrollback (%d lines) ---\n", len(proj.Scrollback))
		for _, row := range proj.Scrollback {
			fmt.Fprintf(out, "%s\n", row)
		}
	}
	fmt.Fprintln(out, "=== End Replay ===")
}
// readReplayInputFile 读取 replay 输入文件：
//   - 若能解码为 JSON ReplayArchiveFile → 直接返回；
//   - 否则视为裸 wire ANSI 字节（--render-output-file 产物），
//     自动包装为 single synthetic committed entry。
func readReplayInputFile(path string, errOut io.Writer) (*outputpkg.ReplayArchiveFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	// 优先尝试 JSON archive 解码。
	if file, decodeErr := outputpkg.DecodeReplayArchive(data); decodeErr == nil {
		return file, nil
	}
	// 回退：裸 wire bytes → 单条 synthetic committed entry。
	entry := newSyntheticWireEntry(data, path)
	file := &outputpkg.ReplayArchiveFile{
		FileSchemaVersion: outputpkg.ReplayArchiveFileSchemaVersion,
		Entries:           []outputpkg.ReplayArchiveEntry{entry},
	}
	fmt.Fprintf(errOut, "Info: replay: %s treated as raw wire bytes (wrapped in synthetic entry)\n", path)
	return file, nil
}

// newSyntheticWireEntry 把裸 wire bytes 包装为 committed ReplayArchiveEntry。
func newSyntheticWireEntry(payload []byte, sourcePath string) outputpkg.ReplayArchiveEntry {
	geom := outputpkg.TerminalGeometry{Width: 80, Height: 24}
	profile := outputpkg.TerminalProfileRef{ID: "ansi", Version: 1}
	now := time.Now()
	entry := outputpkg.ReplayArchiveEntry{
		SchemaMajor: outputpkg.ReplayArchiveSchemaMajor,
		SchemaMinor: outputpkg.ReplayArchiveSchemaMinor,
		Record: outputpkg.DeliveryRecord{
			RecordID:      "synthetic:" + sourcePath,
			SchemaVersion: outputpkg.SchemaVersion,
			Batch: outputpkg.RecordedBatch{
				SessionID:             "synthetic-replay",
				BatchID:               "synthetic-replay",
				IntentID:              "synthetic-intent",
				Sequence:              1,
				RouteEpoch:            1,
				ProjectionTargetID:    "pt-synthetic",
				ProjectionTargetClass: outputpkg.TargetClassVirtual,
				Kind:                  outputpkg.TransactionFrame,
				Source:                "replay",
				Cause:                 "raw-file:" + sourcePath,
				Terminal:              outputpkg.RenderTerminalContext{Geometry: geom, Profile: profile},
				BytesLength:           len(payload),
			},
			Output: outputpkg.RecordedOutputReceipt{
				BatchID:               "synthetic-replay",
				Sequence:              1,
				RouteEpoch:            1,
				ProjectionTargetID:    "pt-synthetic",
				ProjectionTargetClass: outputpkg.TargetClassVirtual,
				Primary: &outputpkg.RecordedTargetReceipt{
					BatchID:            "synthetic-replay",
					Sequence:           1,
					Status:             outputpkg.DeliveryCommitted,
					Certainty:          outputpkg.WriteCertaintyFull,
					ProjectionTargetID: "pt-synthetic",
				},
			},
			SealedAt: now,
		},
		PayloadSource: outputpkg.CapturedDelivery{
			SchemaVersion:      outputpkg.SchemaVersion,
			CaptureEntryID:     "synthetic:" + sourcePath,
			SessionID:          "synthetic-replay",
			BatchID:            "synthetic-replay",
			Sequence:           1,
			RouteEpoch:         1,
			ProjectionTargetID: "pt-synthetic",
			TargetClass:        outputpkg.TargetClassVirtual,
			Mode:               outputpkg.RecordedFullAvailable,
			BytesLength:        len(payload),
			Transaction:        outputpkg.TransactionFrame,
			CapturedAt:         now,
		},
		Payload: append([]byte(nil), payload...),
	}
	entry.PayloadChecksum = outputpkg.ReplayArchiveChecksum(entry)
	return entry
}