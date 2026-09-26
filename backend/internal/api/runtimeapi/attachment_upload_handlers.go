// attachment_upload_handlers.go — POST /api/runtime/uploads：会话图片附件上传。
//
// 与 micro web client 的 /web/api/attachments 共用同一核心 internal/imageattach：
// 上传只落盘（体积/真实格式判定 + 压缩，内容哈希命名），**不改任何会话状态**；
// 路径由调用方随后在 POST /sessions/{id}/runtime/commands 的 submit_prompt.images
// 里回传，由 actor 既有的 ImagePaths 通道组成多模态消息（internal/chat/commands.go）。
//
// 安全边界：submit_prompt.images 只接受落在附件根目录内的路径（resolveRuntimeUploadImages）。
// 否则任何能调 runtime API 的客户端都能用一段 JSON 让服务端去读机器上的任意本地文件。
package runtimeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/gorilla/mux"
	"github.com/wwsheng009/ai-agent-runtime/internal/imageattach"
)

const (
	// maxRuntimeUploadFiles 是单次请求允许的图片张数。
	maxRuntimeUploadFiles = 8
	// runtimeUploadFieldName 是 multipart 里的文件字段名（与 micro web 端点一致）。
	runtimeUploadFieldName = "file"
)

// RuntimeUploadOptions 是上传端点的配置。
type RuntimeUploadOptions struct {
	// RootDir 是附件根目录（服务端选定，客户端不可指定）；为空时端点统一 503，
	// 与 /fs/* 的「服务未注入即可解释降级」口径一致（规划 §5.7）。
	RootDir string
	// Limits 是单图上限；零值用 imageattach.DefaultLimits()（1568px / 32MB）。
	Limits imageattach.Limits
}

// runtimeUploadRoot 保存已注册的附件根目录，供 submit_prompt.images 做路径边界校验。
// 用包级 atomic 值而不是 Handler 字段：注册函数是唯一写入点，这样不必改动 Handler
// 结构体与它的所有构造路径（runtime-server 与测试都直接 new Handler）。
var runtimeUploadRoot atomic.Value

// RuntimeUploadRootDir 返回已注册的附件根目录（未注册时为空字符串）。
func RuntimeUploadRootDir() string {
	if value, ok := runtimeUploadRoot.Load().(string); ok {
		return value
	}
	return ""
}

type runtimeUploadHandlers struct {
	rootDir string
	limits  imageattach.Limits
}

// RegisterRuntimeUploadRoutes 在 /api/runtime 子路由上注册 /uploads
// （router 已经是 /api/runtime 子路由，因此这里只写相对路径）。
func RegisterRuntimeUploadRoutes(router *mux.Router, opts RuntimeUploadOptions) {
	if router == nil {
		return
	}
	rootDir := strings.TrimSpace(opts.RootDir)
	if rootDir != "" {
		runtimeUploadRoot.Store(rootDir)
	}
	handlers := &runtimeUploadHandlers{rootDir: rootDir, limits: opts.Limits}
	router.HandleFunc("/uploads", handlers.upload).Methods(http.MethodPost)
}

// runtimeUploadEntry 是单个文件的处理结果（与 micro web 端点同形，前端可共用解析）。
type runtimeUploadEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Note    string `json:"note,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

func (h *runtimeUploadHandlers) upload(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(h.rootDir) == "" {
		writeRuntimeUploadJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"ok":     false,
			"error":  "uploads_unavailable",
			"reason": "本服务未配置附件上传目录",
		})
		return
	}
	limits := h.limits
	if limits.MaxBytes <= 0 {
		// 只补缺失的体积上限，不能整struct替换——否则调用方显式给的
		// MaxDimension（例如测试/自定义部署里的更强压缩）会被默认值悄悄覆盖。
		limits.MaxBytes = imageattach.DefaultLimits().MaxBytes
	}

	// 先按「张数 × 单图上限 + multipart 余量」限流，避免把超大请求读进内存。
	r.Body = http.MaxBytesReader(w, r.Body, int64(maxRuntimeUploadFiles)*(limits.MaxBytes+1<<20))
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeRuntimeUploadJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok":     false,
			"error":  "invalid_multipart",
			"reason": err.Error(),
		})
		return
	}
	files := r.MultipartForm.File[runtimeUploadFieldName]
	if len(files) == 0 {
		writeRuntimeUploadJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok":     false,
			"error":  "missing_file",
			"reason": "缺少 " + runtimeUploadFieldName + " 字段（multipart/form-data）",
		})
		return
	}
	if len(files) > maxRuntimeUploadFiles {
		writeRuntimeUploadJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok":     false,
			"error":  "too_many_files",
			"reason": fmt.Sprintf("一次最多上传 %d 张图片", maxRuntimeUploadFiles),
		})
		return
	}

	entries := make([]runtimeUploadEntry, 0, len(files))
	for _, header := range files {
		entry := runtimeUploadEntry{Name: header.Filename}
		file, err := header.Open()
		if err != nil {
			entry.Skipped = true
			entry.Note = "读取上传内容失败: " + err.Error()
			entries = append(entries, entry)
			continue
		}
		data, readErr := io.ReadAll(io.LimitReader(file, limits.MaxBytes+1))
		file.Close()
		if readErr != nil {
			entry.Skipped = true
			entry.Note = "读取上传内容失败: " + readErr.Error()
			entries = append(entries, entry)
			continue
		}
		prepared, prepareErr := imageattach.SaveUploaded(data, header.Filename, h.rootDir, limits)
		if prepareErr != nil {
			entry.Skipped = true
			entry.Note = prepareErr.Error()
			entries = append(entries, entry)
			continue
		}
		entry.Path = prepared.Path
		entry.Bytes = prepared.Bytes
		entry.Width = prepared.Width
		entry.Height = prepared.Height
		entry.Note = prepared.Note
		entry.Skipped = prepared.Skipped
		entries = append(entries, entry)
	}

	accepted := 0
	for _, entry := range entries {
		if entry.Path != "" {
			accepted++
		}
	}
	writeRuntimeUploadJSON(w, http.StatusOK, map[string]interface{}{
		"ok":          true,
		"accepted":    accepted,
		"attachments": entries,
	})
}

// resolveRuntimeUploadImages 过滤 submit_prompt.images：
//   - 只接受落在附件根目录内的路径（其余以 note 回报，绝不静默放行）；
//   - 对通过校验的路径做一次幂等预处理（超限/坏图同样以 note 回报）。
//
// 返回可发送路径与说明；两者都可能为空（说明为空表示一切正常）。
func resolveRuntimeUploadImages(paths []string) ([]string, []string) {
	if len(paths) == 0 {
		return nil, nil
	}
	root := RuntimeUploadRootDir()
	if root == "" {
		return nil, []string{"本服务未配置附件上传目录（POST /api/runtime/uploads 不可用），已忽略 images"}
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, []string{"附件目录不可解析: " + err.Error()}
	}

	allowed := make([]string, 0, len(paths))
	var notes []string
	for _, raw := range paths {
		candidate := strings.TrimSpace(raw)
		if candidate == "" {
			continue
		}
		absPath, absErr := filepath.Abs(candidate)
		if absErr != nil {
			notes = append(notes, "已忽略 "+candidate+"：路径无效")
			continue
		}
		if !runtimeUploadPathWithin(absRoot, absPath) {
			notes = append(notes, "已忽略 "+candidate+"：不在附件目录内（只接受 POST /api/runtime/uploads 产出的路径）")
			continue
		}
		allowed = append(allowed, absPath)
	}
	if len(allowed) == 0 {
		return nil, notes
	}

	prepared := imageattach.PrepareLocalAll(allowed, absRoot, imageattach.DefaultLimits())
	out := make([]string, 0, len(prepared))
	for _, item := range prepared {
		if item.Skipped || item.Path == "" {
			if item.Note != "" {
				notes = append(notes, item.Note)
			}
			continue
		}
		out = append(out, item.Path)
	}
	return out, notes
}

// runtimeUploadPathWithin 判断 path 是否落在 root 之内（含 root 自身）。
func runtimeUploadPathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func writeRuntimeUploadJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// attachRuntimeImageNotes 把本回合实际挂上的图片数与说明写进 submit_prompt 响应。
// 不带 images 的请求不会出现这两个字段——旧客户端的响应契约完全不变。
func attachRuntimeImageNotes(payload map[string]interface{}, images []string, notes []string) {
	if payload == nil {
		return
	}
	if len(images) > 0 {
		payload["attached_images"] = len(images)
	}
	if len(notes) > 0 {
		payload["image_notes"] = notes
	}
}
