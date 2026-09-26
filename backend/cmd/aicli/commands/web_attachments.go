// web_attachments.go — micro web client 的图片附件上传端点（POST /web/api/attachments）。
//
// 与 CLI 的分工：
//   - 字节 → 受校验、受上限约束的本地路径：走共享核心 internal/imageattach
//     （与 CLI 的 /attach、剪贴板读图、路径粘贴完全同一条处理链）；
//   - 路径 → 随消息发送：由 /web/api/input 的 image_paths 回传，并入会话附件列表，
//     下一回合的 sendMessage 与文本一起发送（与 CLI 的 /attach 语义一致）。
//
// 为什么上传不直接改会话附件列表：Web 客户端会刷新/切页，若在上传时就登记，
// "上传了但没发"的图片会粘在下一轮；因此上传只落盘并返回路径，登记发生在发送瞬间。
package commands

import (
	"fmt"
	"io"
	"net/http"

	"github.com/wwsheng009/ai-agent-runtime/internal/imageattach"
)

// maxChatWebUploadFiles 是单次请求允许的图片张数上限（与 CLI 粘贴路径的上限同量级，
// 避免一次请求把内存/磁盘打满）。
const maxChatWebUploadFiles = 8

// chatWebAttachmentEntry 是一次上传中单个文件的处理结果。
type chatWebAttachmentEntry struct {
	Name    string `json:"name"`
	Path    string `json:"path,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	Width   int    `json:"width,omitempty"`
	Height  int    `json:"height,omitempty"`
	Note    string `json:"note,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

// HandleChatWebAPIAttachments 处理 POST /web/api/attachments：
// multipart/form-data，字段名 file（可多份）。返回每个文件的结果；被跳过的文件
// 带 skipped 与一行 Note 说明原因（超体积上限 / 不是可识别的图片），可用的文件带
// path（会话 images artifact 目录下的最终路径）。
func HandleChatWebAPIAttachments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeWebAPIJSON(w, http.StatusMethodNotAllowed, map[string]string{
			"status": "rejected",
			"reason": "method not allowed",
		})
		return
	}
	session := chatWebSession()
	if session == nil {
		writeWebAPIJSON(w, http.StatusConflict, map[string]string{
			"status": "error",
			"reason": "no active chat session",
		})
		return
	}

	limits := chatImageLimits(session)
	bodyLimit := int64(maxChatWebUploadFiles) * (limits.MaxBytes + 1<<20)
	r.Body = http.MaxBytesReader(w, r.Body, bodyLimit)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "解析上传内容失败: " + err.Error(),
		})
		return
	}
	files := r.MultipartForm.File["file"]
	if len(files) == 0 {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": "缺少 file 字段（multipart/form-data）",
		})
		return
	}
	if len(files) > maxChatWebUploadFiles {
		writeWebAPIJSON(w, http.StatusBadRequest, map[string]string{
			"status": "rejected",
			"reason": fmt.Sprintf("一次最多上传 %d 张图片", maxChatWebUploadFiles),
		})
		return
	}

	artifactDir := chatImageArtifactDir(session)
	entries := make([]chatWebAttachmentEntry, 0, len(files))
	for _, header := range files {
		entry := chatWebAttachmentEntry{Name: header.Filename}
		file, err := header.Open()
		if err != nil {
			entry.Skipped = true
			entry.Note = "读取上传内容失败: " + err.Error()
			entries = append(entries, entry)
			continue
		}
		// 多读 1 字节用于判定"是否超过上限"，避免把超大文件整份读进内存。
		data, readErr := io.ReadAll(io.LimitReader(file, limits.MaxBytes+1))
		file.Close()
		if readErr != nil {
			entry.Skipped = true
			entry.Note = "读取上传内容失败: " + readErr.Error()
			entries = append(entries, entry)
			continue
		}
		prepared, prepareErr := imageattach.SaveUploaded(data, header.Filename, artifactDir, limits)
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
	writeWebAPIJSON(w, http.StatusOK, map[string]any{
		"status":      "ok",
		"accepted":    accepted,
		"attachments": entries,
	})
}

// attachChatWebImages 把本次输入携带的图片路径并入会话附件列表，返回接纳数量与
// 一行行说明（跳过原因 / 压缩说明）。
//
// 与 CLI 的 /attach 保持一致：已在上限内的图片会被 imageprep 原样返回，因此
// "上传时已预处理 + 发送时再校验"不会二次压缩；重复路径按忽略大小写去重。
// 附件由下一回合的 sendMessage 发送并在成功后清空（finishSuccessfulChatSend）。
func attachChatWebImages(session *ChatSession, imagePaths []string) (int, []string) {
	if session == nil || len(imagePaths) == 0 {
		return 0, nil
	}
	preparedList := imageattach.PrepareLocalAll(imagePaths, chatImageArtifactDir(session), chatImageLimits(session))
	attached := 0
	var notes []string
	for _, prepared := range preparedList {
		if prepared.Skipped || prepared.Path == "" {
			if prepared.Note != "" {
				notes = append(notes, prepared.Note)
			}
			continue
		}
		if prepared.Note != "" {
			notes = append(notes, prepared.Note)
		}
		if chatImageAttachmentIndex(session, prepared.Path) != 0 {
			continue
		}
		session.ImagePaths = append(session.ImagePaths, prepared.Path)
		attached++
	}
	if attached > 0 {
		refreshChatComposerContext(session)
	}
	return attached, notes
}
