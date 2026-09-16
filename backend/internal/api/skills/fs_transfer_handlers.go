package skills

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/filebrowse"
	"github.com/wwsheng009/ai-agent-runtime/internal/fsscope"
)

// 传输端点的请求体上限：init/complete 是纯 JSON 元数据；chunk 是原始字节，
// 取服务端分片上限（8 MiB）之上的余量，真正的一致性由服务层校验（规划 §5.4/§5.8）。
const (
	fsUploadInitBodyLimit     = 64 << 10
	fsUploadCompleteBodyLimit = 64 << 10
	fsUploadChunkBodyLimit    = 16 << 20
)

// uploadInit 处理 POST /fs/upload/init：创建上传会话（或秒传）。
func (h *fsBrowserHandlers) uploadInit(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	var payload struct {
		Scope          string `json:"scope"`
		Dir            string `json:"dir"`
		Name           string `json:"name"`
		Size           int64  `json:"size"`
		SHA256         string `json:"sha256"`
		ChunkSize      int64  `json:"chunk_size"`
		ConflictPolicy string `json:"conflict_policy"`
	}
	if err := fsDecodeJSONBody(w, r, fsUploadInitBodyLimit, &payload); err != nil {
		fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadTargetInvalid, "invalid upload init payload: "+err.Error()), fsscope.CodeFSWriteFailed)
		return
	}
	result, err := h.service.InitUpload(r.Context(), filebrowse.UploadInitRequest{
		Scope:          payload.Scope,
		Dir:            payload.Dir,
		Name:           payload.Name,
		Size:           payload.Size,
		SHA256:         payload.SHA256,
		ChunkSize:      payload.ChunkSize,
		ConflictPolicy: payload.ConflictPolicy,
	})
	if err != nil {
		fsWriteError(w, r, err, fsscope.CodeFSWriteFailed)
		return
	}
	fsWriteJSON(w, http.StatusOK, result)
}

// uploadChunk 处理 PUT /fs/upload/{upload_id}/chunk：body 为原始字节（不 base64）。
//
// 线序来源优先级：Content-Range → X-Upload-Offset → ?start=；三者都缺失时按 0 处理，
// 由服务层的 offset 校验兜底（不盲信客户端）。
func (h *fsBrowserHandlers) uploadChunk(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	rangeStart, rangeEnd, rangeTotal, hasRange, rangeErr := fsParseContentRange(r.Header.Get("Content-Range"))
	if rangeErr != nil {
		fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, rangeErr.Error()), fsscope.CodeFSWriteFailed)
		return
	}
	start := int64(0)
	if value := strings.TrimSpace(r.Header.Get("X-Upload-Offset")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, "X-Upload-Offset must be a non-negative integer"), fsscope.CodeFSWriteFailed)
			return
		}
		start = parsed
	}
	if value := strings.TrimSpace(r.URL.Query().Get("start")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 {
			fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, "start must be a non-negative integer"), fsscope.CodeFSWriteFailed)
			return
		}
		if start != 0 && start != parsed {
			fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, "start query parameter does not match the declared range"), fsscope.CodeFSWriteFailed)
			return
		}
		start = parsed
	}
	if hasRange {
		if start != 0 && start != rangeStart {
			fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, "X-Upload-Offset does not match Content-Range"), fsscope.CodeFSWriteFailed)
			return
		}
		start = rangeStart
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, fsUploadChunkBodyLimit))
	if err != nil {
		message := "read chunk body failed: " + err.Error()
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			message = fmt.Sprintf("chunk body exceeds the %d byte limit", fsUploadChunkBodyLimit)
		}
		fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, message), fsscope.CodeFSWriteFailed)
		return
	}
	if len(data) == 0 {
		fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, "chunk body is empty"), fsscope.CodeFSWriteFailed)
		return
	}
	if hasRange && rangeEnd-rangeStart+1 != int64(len(data)) {
		fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadChunkInvalid, "chunk length does not match Content-Range"), fsscope.CodeFSWriteFailed)
		return
	}
	length := int64(len(data))
	result, err := h.service.PutChunk(r.Context(), filebrowse.UploadChunkRequest{
		UploadID:  mux.Vars(r)["upload_id"],
		Start:     start,
		End:       start + length - 1,
		TotalSize: rangeTotal,
		Data:      data,
		SHA256:    r.Header.Get("X-Chunk-Sha256"),
	})
	if err != nil {
		fsWriteError(w, r, err, fsscope.CodeFSWriteFailed)
		return
	}
	fsWriteJSON(w, http.StatusOK, result)
}

// uploadStatus 处理 GET /fs/upload/{upload_id}：续传探测（真实 offset 以磁盘为准）。
func (h *fsBrowserHandlers) uploadStatus(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	result, err := h.service.UploadStatus(r.Context(), mux.Vars(r)["upload_id"])
	if err != nil {
		fsWriteError(w, r, err, fsscope.CodeFSWriteFailed)
		return
	}
	fsWriteJSON(w, http.StatusOK, result)
}

// uploadComplete 处理 POST /fs/upload/{upload_id}/complete：校验 sha256 后原子落盘。
func (h *fsBrowserHandlers) uploadComplete(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	var payload struct {
		SHA256 string `json:"sha256"`
	}
	if err := fsDecodeJSONBody(w, r, fsUploadCompleteBodyLimit, &payload); err != nil {
		fsWriteError(w, r, fsUploadBadRequest(filebrowse.CodeUploadTargetInvalid, "invalid upload complete payload: "+err.Error()), fsscope.CodeFSWriteFailed)
		return
	}
	result, err := h.service.CompleteUpload(r.Context(), filebrowse.UploadCompleteRequest{
		UploadID: mux.Vars(r)["upload_id"],
		SHA256:   payload.SHA256,
	})
	if err != nil {
		fsWriteError(w, r, err, fsscope.CodeFSWriteFailed)
		return
	}
	fsWriteJSON(w, http.StatusOK, map[string]interface{}{
		"file":       map[string]interface{}{"path": result.Path, "abs_path": result.AbsPath, "size": result.Size},
		"path":       result.Path,
		"action":     result.Action,
		"sha256":     result.SHA256,
		"elapsed_ms": result.ElapsedMS,
	})
}

// uploadAbort 处理 DELETE /fs/upload/{upload_id}：中止并清理临时文件（幂等）。
func (h *fsBrowserHandlers) uploadAbort(w http.ResponseWriter, r *http.Request) {
	if !h.requireService(w, r) {
		return
	}
	uploadID := mux.Vars(r)["upload_id"]
	if err := h.service.AbortUpload(r.Context(), uploadID); err != nil {
		fsWriteError(w, r, err, fsscope.CodeFSWriteFailed)
		return
	}
	fsWriteJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "upload_id": uploadID})
}

// fsDecodeJSONBody 解析 JSON body；body 为空按零值处理（complete 的 sha256 是可选的）。
func fsDecodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, target interface{}) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, limit))
	if err := decoder.Decode(target); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

// fsUploadBadRequest 构造请求级 400 错误（复用 filebrowse 的机器码与 fsscope 的错误体）。
func fsUploadBadRequest(code, message string) error {
	return fsscope.NewError(code, http.StatusBadRequest, message)
}

// fsParseContentRange 解析 `Content-Range: bytes <start>-<end>/<total|*>`。
// 返回 (start, end, total, 是否出现, error)；total 为 0 表示未声明（`*`）。
func fsParseContentRange(value string) (int64, int64, int64, bool, error) {
	raw := strings.TrimSpace(value)
	if raw == "" {
		return 0, 0, 0, false, nil
	}
	const unit = "bytes"
	if len(raw) <= len(unit) || !strings.EqualFold(raw[:len(unit)], unit) {
		return 0, 0, 0, true, errors.New("Content-Range must use the bytes unit")
	}
	spec := strings.TrimSpace(raw[len(unit):])
	halves := strings.SplitN(spec, "/", 2)
	if len(halves) != 2 {
		return 0, 0, 0, true, errors.New("Content-Range must be formatted as bytes start-end/total")
	}
	bounds := strings.SplitN(halves[0], "-", 2)
	if len(bounds) != 2 {
		return 0, 0, 0, true, errors.New("Content-Range must declare a start and an end")
	}
	start, err := strconv.ParseInt(strings.TrimSpace(bounds[0]), 10, 64)
	if err != nil || start < 0 {
		return 0, 0, 0, true, errors.New("Content-Range start must be a non-negative integer")
	}
	end, err := strconv.ParseInt(strings.TrimSpace(bounds[1]), 10, 64)
	if err != nil || end < start {
		return 0, 0, 0, true, errors.New("Content-Range end must be a non-negative integer not smaller than start")
	}
	total := int64(0)
	if trimmed := strings.TrimSpace(halves[1]); trimmed != "" && trimmed != "*" {
		parsed, parseErr := strconv.ParseInt(trimmed, 10, 64)
		if parseErr != nil || parsed < 0 {
			return 0, 0, 0, true, errors.New("Content-Range total must be a non-negative integer or *")
		}
		total = parsed
	}
	return start, end, total, true, nil
}
