// 远端视频转码（resolver 叶子节点的前置步骤）：HLS/DASH 等流媒体清单先经
// 转码器落成 mp4 再上传（上游 wechat_video_transcode.go 的移植）。
//
// 候选顺序：AICLI_WECHAT_VIDEO_TRANSCODER / AICLI_WECHAT_REMOTE_VIDEO_TRANSCODER
// （自定义，参数 <url> <out>）> ffmpeg 流复制（-c copy）> ffmpeg 重编码
// （libx264/aac）。全部失败返回 error（上游同策略：清单类视频不能原样上传），
// 由调用方（Send 媒体降级）回一条文本提示。
package wechat

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const remoteVideoTranscodeTimeout = 2 * time.Minute

type remoteVideoTranscodeCandidate struct {
	label   string
	command string
	args    func(inputURL string, outputPath string) []string
}

var (
	// lookPathRemoteVideoCommand / runRemoteVideoTranscode 可在测试中替换。
	lookPathRemoteVideoCommand = exec.LookPath
	runRemoteVideoTranscode    = runRemoteVideoTranscodeCommand
)

// remoteVideoTranscodeCandidates 返回按优先级排列的转码器候选。
func remoteVideoTranscodeCandidates() []remoteVideoTranscodeCandidate {
	candidates := make([]remoteVideoTranscodeCandidate, 0, 3)

	if transcoder := strings.TrimSpace(firstNonEmpty(
		os.Getenv("AICLI_WECHAT_VIDEO_TRANSCODER"),
		os.Getenv("AICLI_WECHAT_REMOTE_VIDEO_TRANSCODER"),
	)); transcoder != "" {
		candidates = append(candidates, remoteVideoTranscodeCandidate{
			label:   "custom remote video transcoder",
			command: transcoder,
			args: func(inputURL string, outputPath string) []string {
				return []string{inputURL, outputPath}
			},
		})
	}

	ffmpegCommand := strings.TrimSpace(firstNonEmpty(os.Getenv("AICLI_WECHAT_FFMPEG"), "ffmpeg"))
	candidates = append(candidates,
		remoteVideoTranscodeCandidate{
			label:   "ffmpeg stream copy",
			command: ffmpegCommand,
			args: func(inputURL string, outputPath string) []string {
				return []string{
					"-y", "-nostdin", "-loglevel", "error",
					"-i", inputURL,
					"-c", "copy",
					"-movflags", "+faststart",
					outputPath,
				}
			},
		},
		remoteVideoTranscodeCandidate{
			label:   "ffmpeg re-encode",
			command: ffmpegCommand,
			args: func(inputURL string, outputPath string) []string {
				return []string{
					"-y", "-nostdin", "-loglevel", "error",
					"-i", inputURL,
					"-movflags", "+faststart",
					"-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
					"-c:a", "aac", "-b:a", "128k",
					outputPath,
				}
			},
		},
	)
	return candidates
}

// transcodeRemoteVideoToMP4 依次尝试候选转码器，返回产出的 mp4 路径与 cleanup。
// 全部失败返回聚合错误（调用方降级为文本提示）。
func (p *Provider) transcodeRemoteVideoToMP4(ctx context.Context, rawURL, fileNameHint string) (string, string, func(), error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return "", "", nil, errors.New("wechat remote video url is required")
	}

	transcodeCtx := ctx
	if transcodeCtx == nil {
		transcodeCtx = context.Background()
	}
	var cancel context.CancelFunc
	if _, hasDeadline := transcodeCtx.Deadline(); !hasDeadline {
		transcodeCtx, cancel = context.WithTimeout(transcodeCtx, remoteVideoTranscodeTimeout)
		defer cancel()
	}

	candidates := remoteVideoTranscodeCandidates()
	if len(candidates) == 0 {
		return "", "", nil, errors.New("no wechat remote video transcoder command was configured")
	}
	outputPath, cleanup, err := allocateRemoteVideoOutputPath(fileNameHint)
	if err != nil {
		return "", "", nil, err
	}

	errs := make([]error, 0, len(candidates))
	for _, candidate := range candidates {
		commandPath, err := lookPathRemoteVideoCommand(candidate.command)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s was not found: %w", candidateLabel(candidate), err))
			continue
		}
		if err := runRemoteVideoTranscode(transcodeCtx, commandPath, candidate.args(rawURL, outputPath), outputPath); err != nil {
			errs = append(errs, err)
			continue
		}
		return outputPath, "video/mp4", cleanup, nil
	}

	cleanup()
	return "", "", nil, errors.Join(errs...)
}

// allocateRemoteVideoOutputPath 在出站媒体临时目录里预分配一个 .mp4 输出路径。
func allocateRemoteVideoOutputPath(fileNameHint string) (string, func(), error) {
	dir := filepath.Join(os.TempDir(), "aicli", "wechat", "media", "outbound")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", nil, fmt.Errorf("create wechat media temp dir: %w", err)
	}

	prefix := strings.TrimSuffix(filepath.Base(strings.TrimSpace(fileNameHint)), filepath.Ext(strings.TrimSpace(fileNameHint)))
	prefix = strings.TrimSpace(prefix)
	if prefix == "" {
		prefix = "wechat-video"
	}
	prefix = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, prefix)

	handle, err := os.CreateTemp(dir, prefix+"-*.mp4")
	if err != nil {
		return "", nil, fmt.Errorf("create wechat remote video temp file: %w", err)
	}
	filePath := handle.Name()
	if closeErr := handle.Close(); closeErr != nil {
		_ = os.Remove(filePath)
		return "", nil, fmt.Errorf("close wechat remote video temp file: %w", closeErr)
	}
	cleanup := func() { _ = os.Remove(filePath) }
	return filePath, cleanup, nil
}

func runRemoteVideoTranscodeCommand(ctx context.Context, commandPath string, args []string, outputPath string) error {
	_ = os.Remove(outputPath)
	command := exec.CommandContext(ctx, commandPath, args...)
	output, err := command.CombinedOutput()
	if err != nil {
		_ = os.Remove(outputPath)
		return fmt.Errorf("%s failed: %w: %s", commandLabel(commandPath), err, compactCommandOutput(output))
	}
	info, statErr := os.Stat(outputPath)
	if statErr != nil {
		return fmt.Errorf("%s did not produce %q: %w", commandLabel(commandPath), outputPath, statErr)
	}
	if info.Size() == 0 {
		_ = os.Remove(outputPath)
		return fmt.Errorf("%s produced an empty mp4 file", commandLabel(commandPath))
	}
	return nil
}

func candidateLabel(candidate remoteVideoTranscodeCandidate) string {
	if label := strings.TrimSpace(candidate.label); label != "" {
		return label
	}
	return commandLabel(candidate.command)
}

func commandLabel(commandPath string) string {
	if base := strings.TrimSpace(filepath.Base(commandPath)); base != "" && base != "." {
		return base
	}
	return strings.TrimSpace(commandPath)
}
