package skills

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/errors"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// ---------------------------------------------------------------------------
// 传输端点：导出 / 导入（§23 G5 / D28 / Q21）
// ---------------------------------------------------------------------------

// profileBundleUploadLimit 是导入请求体（zip）的压缩大小上限。解压后的真实
// 大小由 profilesys 的包内上限（BundleMaxTotalBytes / BundleMaxFileBytes）把关，
// 这里挡的是传输层，避免"压缩比炸弹"把请求体读进内存。
const profileBundleUploadLimit = 8 << 20

// ExportRuntimeProfile 导出 profile 目录为 zip（POST /profiles/{ref}/export）。
//
// 响应体即 zip 包（Q21：目录或 zip，不支持单文件内联——单文件内联会引入第二套
// profile 方言）；包内路径就是 profile 根下的相对路径。只读语义，不写盘、不碰
// 会话与配置；文件数/大小有上限（有界导出，R23）。
func (h *Handler) ExportRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	ref := mux.Vars(r)["ref"]
	target, err := h.resolveRuntimeProfileTarget(ref)
	if err != nil {
		h.writeProfileTargetError(w, err)
		return
	}
	if err := assertRuntimeProfileRootSafe(target.Root); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	files, err := profilesys.CollectBundleFiles(target.Root)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	var buf bytes.Buffer
	if err := profilesys.WriteBundleZip(&buf, files); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "export profile failed", err))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", target.Ref+".zip"))
	w.Header().Set("X-Aicli-Profile-Ref", target.Ref)
	w.Header().Set("X-Aicli-Profile-File-Count", strconv.Itoa(len(files)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(buf.Bytes())
}

// ImportRuntimeProfile 导入 zip 包为新 profile（POST /profiles/import）。
//
// 查询参数：name（可选，须与包内 profile.yaml 声明的 name 一致）、layer
// （user|project，默认 user）、dry_run（true 时只预演不落盘）。
//
// D28 纪律：① 先物化到临时目录并跑**同一个 validate**，失败即拒绝、目标目录不
// 存在；② 绝不自动激活（不写 default、不切会话）；③ 响应给出将写入的路径清单
// （与删除端点同一投影）；④ 目标层同名冲突返回 409，不覆盖。成功时以 rename
// 原子落位，列表随即按 source=root 可见。
func (h *Handler) ImportRuntimeProfile(w http.ResponseWriter, r *http.Request) {
	if err := h.authorizeProfileWrite(r); err != nil {
		h.writeError(w, http.StatusForbidden, err)
		return
	}
	query := r.URL.Query()
	dryRun := strings.EqualFold(strings.TrimSpace(query.Get("dry_run")), "true")
	layer := strings.ToLower(strings.TrimSpace(query.Get("layer")))
	if layer == "" {
		layer = "user"
	}
	layerRoot, err := runtimeProfileLayerRoot(layer)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if r.Body == nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"import request body (application/zip) is required"))
		return
	}
	data, readErr := io.ReadAll(http.MaxBytesReader(w, r.Body, profileBundleUploadLimit))
	if readErr != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed,
			"读取导入包失败（上限 "+strconv.Itoa(profileBundleUploadLimit)+" 字节）："+readErr.Error()))
		return
	}
	if len(data) == 0 {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, "导入包为空"))
		return
	}
	files, err := profilesys.ReadBundleZip(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	if err := os.MkdirAll(layerRoot, 0o755); err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "prepare profiles root failed", err))
		return
	}
	tempDir, err := os.MkdirTemp(layerRoot, ".import-*")
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "create import workspace failed", err))
		return
	}
	defer func() { _ = os.RemoveAll(tempDir) }()
	paths, err := profilesys.ExtractBundle(tempDir, files)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	validation, validateErr := profilesys.ValidateProfileReference(tempDir, "", h.profileResolveOptions(""))
	if validateErr != nil {
		h.writeError(w, http.StatusBadRequest, errors.Wrap(errors.ErrValidationFailed, "导入包校验失败", validateErr))
		return
	}
	if !validation.Valid {
		// D28-1：包能解压但过不了同一个 validate → 拒绝（此时目标目录尚未创建）。
		h.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"ok":            false,
			"imported":      false,
			"activated":     false,
			"dry_run":       dryRun,
			"layer":         layer,
			"valid":         false,
			"error":         "导入包未通过 validate：修正后再导入（D28）",
			"error_count":   validation.ErrorCount,
			"warning_count": validation.WarningCount,
			"issues":        validation.Issues,
			"paths":         paths,
			"file_count":    len(paths),
		})
		return
	}
	// 包内 profile.yaml 的 name 是权威。不能用 validation.ProfileName：它是
	// resolved 名，对"没写 name"的包会退化成临时目录名（.import-xxxx）。
	declared := ""
	if validation.Spec != nil {
		declared = strings.TrimSpace(validation.Spec.Profile.Name)
	}
	name, nameErr := resolveImportedProfileName(query.Get("name"), declared)
	if nameErr != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, nameErr.Error()))
		return
	}
	if err := profilesys.ValidateProfileName(name); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}

	targetRoot := filepath.Join(layerRoot, name)
	response := map[string]interface{}{
		"name":       name,
		"layer":      layer,
		"root":       targetRoot,
		"paths":      paths,
		"file_count": len(paths),
		"valid":      true,
		"dry_run":    dryRun,
		"imported":   false,
		// D28：导入绝不自动激活。
		"activated": false,
		"hint":      "已导入但未激活：设为默认（default）或应用到会话（apply）都是独立动作",
	}
	if err := assertRuntimeProfileRootSafe(targetRoot); err != nil {
		h.writeError(w, http.StatusBadRequest, errors.New(errors.ErrValidationFailed, err.Error()))
		return
	}
	if _, statErr := os.Stat(targetRoot); statErr == nil {
		h.writeError(w, http.StatusConflict, errors.New(errors.ErrConfigInvalid,
			"目标层已存在同名 profile："+targetRoot+"（导入不覆盖：请改名或先删除）"))
		return
	}
	if dryRun {
		response["ok"] = true
		h.writeJSON(w, http.StatusOK, response)
		return
	}
	if err := os.Rename(tempDir, targetRoot); err != nil {
		if _, statErr := os.Stat(targetRoot); statErr == nil {
			h.writeError(w, http.StatusConflict, errors.New(errors.ErrConfigInvalid,
				"目标层已存在同名 profile："+targetRoot+"（导入不覆盖：请改名或先删除）"))
			return
		}
		h.writeError(w, http.StatusInternalServerError, errors.Wrap(errors.ErrConfigInvalid, "import profile failed", err))
		return
	}
	response["ok"] = true
	response["imported"] = true
	response["hint"] = "已导入并落在列表（source: root）：使用前请显式 default/apply（D28：导入绝不自动激活）"
	h.writeJSON(w, http.StatusCreated, response)
}

// resolveImportedProfileName 定夺导入目标名：包内 profile.yaml 声明的 name 是
// 权威；显式 name 必须与之一致（导入不静默改写 profile.yaml，改名请导入后走
// rename）；两者都缺则报错（profile 必须有名字）。
func resolveImportedProfileName(requested, declared string) (string, error) {
	requested = strings.TrimSpace(requested)
	declared = strings.TrimSpace(declared)
	if requested != "" && declared != "" && !strings.EqualFold(requested, declared) {
		return "", fmt.Errorf("导入包声明的 name 是 %q：导入不改写 profile.yaml，改名请导入后用 rename", declared)
	}
	if declared != "" {
		return declared, nil
	}
	if requested != "" {
		return requested, nil
	}
	return "", fmt.Errorf("导入包未声明 profile name：请在 profile.yaml 里补 name，或显式传 name 参数")
}
