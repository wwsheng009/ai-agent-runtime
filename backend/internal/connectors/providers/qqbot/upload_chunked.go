package qqbot

// 大文件分片上传（官方「分片上传（推荐）」流程）：
//
//	1. 预上传  POST /v2/{users|groups}/{id}/upload_prepare
//	            body: file_type/file_size/file_name/md5/sha1/md5_10m
//	            resp: upload_id + block_size + parts[{index,presigned_url,block_size}]
//	                  + upload_config{concurrency,retry_timeout,retry_delay}
//	2. 分片 PUT  把按 block_size 切好的分片逐片 PUT 到 presigned_url
//	3. 分片确认  POST /v2/{users|groups}/{id}/upload_part_finish
//	            body: upload_id/part_index/block_size/md5(分片)
//	4. 完成合并  POST /v2/{users|groups}/{id}/files  body: {file_type, upload_id}
//	            → file_info（随后走 msg_type=7 发送）
//
// 与整文件上传的取舍：整文件 base64（file_data）适合小文件；超过
// chunkedUploadThreshold 走分片（避免 base64 膨胀与服务端单请求体量限制）。
// 分片失败按 upload_config.retry_delay 重试，仍失败则返回错误由调用方回落。

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/wwsheng009/ai-agent-runtime/internal/connectors"
)

const (
	// chunkedUploadThreshold 是走分片上传的体量阈值（超过则分片；可用
	// WithChunkedUploadThreshold 覆盖，测试用）。
	chunkedUploadThreshold = 10 << 20
	// md5WindowBytes 是 md5_10m 的窗口大小（官方：前 10002432 字节 ≈ 9.54MB）。
	md5WindowBytes = 10002432
	// chunkPartRetryLimit 是单分片 PUT/确认的重试上限（在服务端 retry_delay 之上）。
	chunkPartRetryLimit = 3
	// chunkPartRetryDelay 是缺省重试间隔（服务端 upload_config.retry_delay 优先）。
	chunkPartRetryDelay = time.Second
)

// chunkedUploadThresholdFor 返回生效的分片阈值（0/负值回落默认）。
func (p *Provider) chunkedUploadThresholdFor() int64 {
	if p.chunkedThreshold > 0 {
		return p.chunkedThreshold
	}
	return chunkedUploadThreshold
}

// prepareEndpoint 返回预上传端点（单聊 /v2/users/{id}/upload_prepare，
// 群聊 /v2/groups/{id}/upload_prepare）。
func prepareEndpoint(base string, target chatTarget) string {
	if target.kind == chatKindGroup {
		return base + "/v2/groups/" + url.PathEscape(target.openID) + "/upload_prepare"
	}
	return base + "/v2/users/" + url.PathEscape(target.openID) + "/upload_prepare"
}

// partFinishEndpoint 返回分片确认端点。
func partFinishEndpoint(base string, target chatTarget) string {
	if target.kind == chatKindGroup {
		return base + "/v2/groups/" + url.PathEscape(target.openID) + "/upload_part_finish"
	}
	return base + "/v2/users/" + url.PathEscape(target.openID) + "/upload_part_finish"
}

// uploadPrepareResponse 是预上传响应。
type uploadPrepareResponse struct {
	UploadID  string `json:"upload_id"`
	BlockSize string `json:"block_size"`
	Parts     []struct {
		Index        int    `json:"index"`
		PresignedURL string `json:"presigned_url"`
		BlockSize    string `json:"block_size"`
	} `json:"parts"`
	UploadConfig struct {
		Concurrency  int `json:"concurrency"`
		RetryTimeout int `json:"retry_timeout"`
		RetryDelay   int `json:"retry_delay"`
	} `json:"upload_config"`
}

// uploadMediaChunked 走官方分片上传并返回 file_info。
func (p *Provider) uploadMediaChunked(ctx context.Context, cfg connectors.Config, target chatTarget, media connectors.Media, fileType int, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("qqbot: chunked upload requires non-empty data")
	}
	base := p.baseFor(cfg)
	fileName := chunkedUploadFileName(media)
	prepare, err := p.prepareChunkedUpload(ctx, cfg, base, target, fileType, fileName, data)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(prepare.UploadID) == "" || len(prepare.Parts) == 0 {
		return "", errors.New("qqbot: upload_prepare returned empty upload_id or parts")
	}

	retryDelay := time.Duration(prepare.UploadConfig.RetryDelay) * time.Second
	if retryDelay <= 0 {
		retryDelay = chunkPartRetryDelay
	}
	// 分片尺寸：优先响应 block_size，回退分片自身 block_size，再回退均分。
	blockSize, err := parseChunkSize(prepare.BlockSize)
	if err != nil || blockSize <= 0 {
		blockSize = len(data)
		if len(prepare.Parts) > 1 {
			blockSize = (len(data) + len(prepare.Parts) - 1) / len(prepare.Parts)
		}
	}

	for _, part := range prepare.Parts {
		start := part.Index * blockSize
		if start >= len(data) {
			return "", fmt.Errorf("qqbot: upload part index %d out of range", part.Index)
		}
		end := start + blockSize
		if partSize, sizeErr := parseChunkSize(part.BlockSize); sizeErr == nil && partSize > 0 && start+partSize <= len(data) {
			end = start + partSize
		}
		if end > len(data) {
			end = len(data)
		}
		chunk := data[start:end]
		if err := p.uploadChunkPart(ctx, cfg, base, target, prepare.UploadID, part.Index, part.PresignedURL, part.BlockSize, chunk, retryDelay); err != nil {
			return "", err
		}
	}

	// 完成合并：携带 upload_id 调上传接口。
	var merged struct {
		FileInfo string `json:"file_info"`
	}
	if err := p.apiJSON(ctx, cfg, http.MethodPost, filesEndpoint(base, target),
		map[string]any{"file_type": fileType, "upload_id": prepare.UploadID}, &merged); err != nil {
		return "", err
	}
	fileInfo := strings.TrimSpace(merged.FileInfo)
	if fileInfo == "" {
		return "", errors.New("qqbot: chunked upload merge returned empty file_info")
	}
	return fileInfo, nil
}

// prepareChunkedUpload 调预上传接口并返回分片计划。
func (p *Provider) prepareChunkedUpload(ctx context.Context, cfg connectors.Config, base string, target chatTarget, fileType int, fileName string, data []byte) (uploadPrepareResponse, error) {
	sum := md5.Sum(data)
	window := data
	if len(window) > md5WindowBytes {
		window = window[:md5WindowBytes]
	}
	windowSum := md5.Sum(window)
	sha1Sum := sha1.Sum(data)
	payload := map[string]any{
		"file_type": fileType,
		"file_size": strconv.Itoa(len(data)),
		"file_name": fileName,
		"md5":       hex.EncodeToString(sum[:]),
		"sha1":      hex.EncodeToString(sha1Sum[:]),
		"md5_10m":   hex.EncodeToString(windowSum[:]),
	}
	var response uploadPrepareResponse
	if err := p.apiJSON(ctx, cfg, http.MethodPost, prepareEndpoint(base, target), payload, &response); err != nil {
		return uploadPrepareResponse{}, err
	}
	return response, nil
}

// uploadChunkPart 上传单个分片（PUT 预签名 URL + 分片确认），带有限重试。
func (p *Provider) uploadChunkPart(ctx context.Context, cfg connectors.Config, base string, target chatTarget, uploadID string, index int, presignedURL, blockSize string, chunk []byte, retryDelay time.Duration) error {
	if strings.TrimSpace(presignedURL) == "" {
		return fmt.Errorf("qqbot: upload part %d missing presigned url", index)
	}
	var lastErr error
	for attempt := 0; attempt < chunkPartRetryLimit; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelay):
			}
		}
		if err := p.putChunk(ctx, presignedURL, chunk); err != nil {
			lastErr = err
			continue
		}
		sum := md5.Sum(chunk)
		finishPayload := map[string]any{
			"upload_id":  uploadID,
			"part_index": index,
			"block_size": blockSize,
			"md5":        hex.EncodeToString(sum[:]),
		}
		if err := p.apiJSON(ctx, cfg, http.MethodPost, partFinishEndpoint(base, target), finishPayload, nil); err != nil {
			lastErr = err
			continue
		}
		return nil
	}
	if lastErr == nil {
		lastErr = errors.New("qqbot: upload part failed")
	}
	return fmt.Errorf("qqbot: upload part %d failed after %d attempts: %w", index, chunkPartRetryLimit, lastErr)
}

// putChunk 把分片 PUT 到预签名 URL（不携带机器人令牌——预签名 URL 自带鉴权）。
func (p *Provider) putChunk(ctx context.Context, presignedURL string, chunk []byte) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, presignedURL, bytes.NewReader(chunk))
	if err != nil {
		return fmt.Errorf("qqbot: build upload part request failed: %w", err)
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := p.client.Do(request)
	if err != nil {
		return fmt.Errorf("qqbot: upload part request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("qqbot: upload part returned %s", response.Status)
	}
	return nil
}

// parseChunkSize 解析分片大小（官方为字符串字节数；空/非法返回错误）。
func parseChunkSize(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return 0, errors.New("qqbot: empty chunk size")
	}
	size, err := strconv.Atoi(trimmed)
	if err != nil || size <= 0 {
		return 0, fmt.Errorf("qqbot: invalid chunk size %q", raw)
	}
	return size, nil
}

// chunkedUploadFileName 给出分片上传的文件名（media.FileName > 本地路径 base >
// 按媒体类别兜底名）。
func chunkedUploadFileName(media connectors.Media) string {
	if name := strings.TrimSpace(media.FileName); name != "" {
		return name
	}
	if path := strings.TrimSpace(media.Path); path != "" {
		if base := filepath.Base(path); base != "" && base != "." {
			return base
		}
	}
	return "qqbot-media" + extensionForMediaKind(media.Kind)
}
