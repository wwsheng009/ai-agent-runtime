package skills

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentconfig "github.com/wwsheng009/ai-agent-runtime/internal/agentconfig"
	profilesys "github.com/wwsheng009/ai-agent-runtime/internal/profile"
)

// Batch 13 slice 2：导出 / 导入（§23 G5 / D28 / Q21）。
//
// 测试纪律：
//   - 导出：包内容 = profile 根相对路径全集（含嵌套），原子写残留不进包；
//   - 导入：先 validate 再落位、绝不自动激活（不写 default/不碰会话/不写配置）、
//     目标冲突 409 不覆盖、dry_run 只预演；
//   - 安全：遍历路径/绝对路径/符号链接条目/缺 profile.yaml/超限一律拒绝，
//     且**磁盘上不留下任何痕迹**（zip-slip 断言）。

// newProfileTransferHarness 把 `layer=user` 的层根重定向到测试临时目录
// （runtimeProfileLayerRoot 读 HOME/USERPROFILE，不能污染真实用户目录），
// 并把宿主配置的 profiles.root 指向该层根，使导入结果在列表里可见。
func newProfileTransferHarness(t *testing.T) (*profilesAPIHarness, string) {
	t.Helper()
	h := newProfilesAPIHarness(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	layerRoot := filepath.Join(home, ".aicli", "profiles")
	require.NoError(t, os.MkdirAll(layerRoot, 0o755))
	h.handler.SetAICLIConfig(&agentconfig.Config{
		ConfigFilePath: h.config,
		Profiles:       &agentconfig.ProfilesConfig{Root: layerRoot},
	})
	return h, layerRoot
}

// rawDo 发一次原始字节请求（导入端点收 application/zip，不能用 JSON 编码的 do）。
func (h *profilesAPIHarness) rawDo(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:34567"
	req.Header.Set("Content-Type", "application/zip")
	rec := httptest.NewRecorder()
	h.router.ServeHTTP(rec, req)
	return rec
}

func decodePayload(t *testing.T, rec *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	payload := map[string]interface{}{}
	if rec.Body.Len() > 0 {
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload), rec.Body.String())
	}
	return payload
}

// exportBundle 走真实端点导出 zip（顺带断言响应契约：application/zip + 计数头）。
func (h *profilesAPIHarness) exportBundle(t *testing.T, ref string) []byte {
	t.Helper()
	rec := h.rawDo(t, http.MethodPost, "/api/runtime/profiles/"+ref+"/export", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, "application/zip", rec.Header().Get("Content-Type"))
	assert.Contains(t, rec.Header().Get("Content-Disposition"), ref+".zip")
	assert.NotEmpty(t, rec.Header().Get("X-Aicli-Profile-File-Count"))
	return rec.Body.Bytes()
}

func (h *profilesAPIHarness) importZip(t *testing.T, query string, zipBytes []byte) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()
	rec := h.rawDo(t, http.MethodPost, "/api/runtime/profiles/import"+query, zipBytes)
	return rec, decodePayload(t, rec)
}

func createTemplateProfile(t *testing.T, h *profilesAPIHarness, name, root string) {
	t.Helper()
	rec, _ := h.do(t, http.MethodPost, "/api/runtime/profiles", map[string]interface{}{
		"name":     name,
		"template": "coding",
		"root":     root,
	})
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
}

// buildRawZip 造一个不做过滤的 zip（用于喂给导入端点的安全断言）。
func buildRawZip(t *testing.T, entries []struct {
	Name string
	Data []byte
	Mode os.FileMode
}) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		header := &zip.FileHeader{Name: e.Name, Method: zip.Deflate}
		if e.Mode != 0 {
			header.SetMode(e.Mode)
		}
		w, err := zw.CreateHeader(header)
		require.NoError(t, err)
		_, err = w.Write(e.Data)
		require.NoError(t, err)
	}
	require.NoError(t, zw.Close())
	return buf.Bytes()
}

func bundlePathSet(files []profilesys.BundleFile) map[string]bool {
	out := make(map[string]bool, len(files))
	for _, f := range files {
		out[f.Path] = true
	}
	return out
}

func listEntry(t *testing.T, payload map[string]interface{}, name string) map[string]interface{} {
	t.Helper()
	entries, ok := payload["profiles"].([]interface{})
	require.True(t, ok, "profiles 缺失：%v", payload)
	for _, raw := range entries {
		entry, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		if entry["name"] == name {
			return entry
		}
	}
	return nil
}

// TestRuntimeProfilesAPI_ExportImportRoundTrip 钉住 G5 主链路：导出（多文件、
// 跳过临时文件）→ 导入到 user 层（先 validate）→ 落在列表且**未激活**。
func TestRuntimeProfilesAPI_ExportImportRoundTrip(t *testing.T) {
	h, layerRoot := newProfileTransferHarness(t)
	srcRoot := h.profileRoot("batch13-share")
	createTemplateProfile(t, h, "batch13-share", srcRoot)

	extraRel := filepath.Join("agents", "reviewer", "prompts", "system.md")
	require.NoError(t, os.MkdirAll(filepath.Join(srcRoot, filepath.Dir(extraRel)), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, extraRel), []byte("system prompt"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(srcRoot, "profile.yaml.tmp"), []byte("junk"), 0o644))

	zipBytes := h.exportBundle(t, "batch13-share")
	files, err := profilesys.ReadBundleZip(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	require.NoError(t, err, "导出的包必须能按同一实现读回")
	paths := bundlePathSet(files)
	assert.True(t, paths["profile.yaml"], "包内必须有 profile.yaml：%v", paths)
	assert.True(t, paths["agents/reviewer/prompts/system.md"], "嵌套文件必须进包：%v", paths)
	assert.False(t, paths["profile.yaml.tmp"], "原子写残留不该进包：%v", paths)

	configBefore := readFileOrEmpty(t, h.config)
	_, configStatErr := os.Stat(h.config)
	rec, payload := h.importZip(t, "?layer=user", zipBytes)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, true, payload["ok"])
	assert.Equal(t, true, payload["imported"])
	assert.Equal(t, "batch13-share", payload["name"])
	assert.Equal(t, "user", payload["layer"])
	assert.Equal(t, filepath.Join(layerRoot, "batch13-share"), payload["root"])
	assert.Equal(t, false, payload["activated"], "D28：导入绝不自动激活")
	assert.NotEmpty(t, payload["paths"], "D28：必须给出将写入的路径清单")
	assert.NotEmpty(t, payload["hint"])

	targetRoot := filepath.Join(layerRoot, "batch13-share")
	assert.FileExists(t, filepath.Join(targetRoot, "profile.yaml"))
	imported, err := os.ReadFile(filepath.Join(targetRoot, extraRel))
	require.NoError(t, err)
	assert.Equal(t, "system prompt", string(imported))
	assert.NoFileExists(t, filepath.Join(targetRoot, "profile.yaml.tmp"))
	if configStatErr != nil {
		assert.NoFileExists(t, h.config, "导入不得创建宿主配置")
	} else {
		assert.Equal(t, configBefore, readFileOrEmpty(t, h.config), "导入不得写宿主配置")
	}

	// 列表可见（source=root），且 default 未被导入改动（A11/A12 作用域）
	rec, payload = h.do(t, http.MethodGet, "/api/runtime/profiles", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	if value, ok := payload["default_profile"]; ok {
		assert.Equal(t, "", value, "导入不得写 default")
	}
	entry := listEntry(t, payload, "batch13-share")
	require.NotNil(t, entry, "导入的 profile 必须落在列表：%v", payload)
	assert.Equal(t, "root", entry["source"])
	assert.Equal(t, false, entry["is_default"])
	assert.Equal(t, true, entry["valid"], "导入前已过 validate，列表里应当有效：%v", entry)

	// 同名重导 → 409，且不覆盖既有内容
	rec, _ = h.importZip(t, "?layer=user", zipBytes)
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	after, err := os.ReadFile(filepath.Join(targetRoot, extraRel))
	require.NoError(t, err)
	assert.Equal(t, "system prompt", string(after), "冲突时不得改动既有 profile")
}

// TestRuntimeProfilesAPI_ImportDryRunAndNameContract 钉住 dry_run 只预演，以及
// 「包内 name 是权威、显式 name 不得悄悄改名」的命名契约。
func TestRuntimeProfilesAPI_ImportDryRunAndNameContract(t *testing.T) {
	h, layerRoot := newProfileTransferHarness(t)
	srcRoot := h.profileRoot("batch13-dry")
	createTemplateProfile(t, h, "batch13-dry", srcRoot)
	zipBytes := h.exportBundle(t, "batch13-dry")

	rec, payload := h.importZip(t, "?layer=user&dry_run=true", zipBytes)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, true, payload["dry_run"])
	assert.Equal(t, false, payload["imported"])
	assert.NotEmpty(t, payload["paths"])
	assert.NoDirExists(t, filepath.Join(layerRoot, "batch13-dry"), "dry_run 不得落盘")

	// 显式改名（与包内 name 不一致）→ 400：导入不改写 profile.yaml，改名走 rename
	rec, _ = h.importZip(t, "?layer=user&name=batch13-renamed", zipBytes)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.NoDirExists(t, filepath.Join(layerRoot, "batch13-renamed"))

	// 非法层名 → 400
	rec, _ = h.importZip(t, "?layer=global", zipBytes)
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())

	// 正常导入（包内 name）→ 201
	rec, _ = h.importZip(t, "?layer=user", zipBytes)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.DirExists(t, filepath.Join(layerRoot, "batch13-dry"))
}

// TestRuntimeProfilesAPI_ImportRejectsUnsafeArchive 钉住 D28/R23 的输入安全：
// 恶意或不合规的包一律 400，且**磁盘不留痕**。
func TestRuntimeProfilesAPI_ImportRejectsUnsafeArchive(t *testing.T) {
	h, layerRoot := newProfileTransferHarness(t)
	srcRoot := h.profileRoot("batch13-safe")
	createTemplateProfile(t, h, "batch13-safe", srcRoot)
	validYAML, err := os.ReadFile(filepath.Join(srcRoot, "profile.yaml"))
	require.NoError(t, err)
	escapeTargets := []string{
		filepath.Join(layerRoot, "escape.yaml"),
		filepath.Join(filepath.Dir(layerRoot), "escape.yaml"),
	}

	type entry = struct {
		Name string
		Data []byte
		Mode os.FileMode
	}
	cases := []struct {
		name    string
		entries []entry
		want    string
	}{
		{
			name: "路径遍历",
			entries: []entry{
				{Name: "profile.yaml", Data: validYAML},
				{Name: "../escape.yaml", Data: []byte("x")},
			},
			want: "traversal",
		},
		{
			name: "绝对路径",
			entries: []entry{
				{Name: "profile.yaml", Data: validYAML},
				{Name: "/abs.yaml", Data: []byte("x")},
			},
			want: "absolute",
		},
		{
			name: "符号链接条目",
			entries: []entry{
				{Name: "profile.yaml", Data: validYAML},
				{Name: "link.yaml", Data: []byte(escapeTargets[0]), Mode: os.ModeSymlink | 0o777},
			},
			want: "symlink",
		},
		{
			name:    "缺根 profile.yaml",
			entries: []entry{{Name: "agents/a.md", Data: []byte("x")}},
			want:    "profile.yaml",
		},
		{
			name: "单文件超限",
			entries: []entry{
				{Name: "profile.yaml", Data: validYAML},
				{Name: "big.bin", Data: bytes.Repeat([]byte("a"), profilesys.BundleMaxFileBytes+1)},
			},
			want: "exceeds",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, payload := h.importZip(t, "?layer=user", buildRawZip(t, tc.entries))
			require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Contains(t, payload["error"], tc.want, "错误信息应当说清拒绝原因：%v", payload)
			for _, escapeTarget := range escapeTargets {
				assert.NoFileExists(t, escapeTarget, "拒绝的包不得在磁盘留痕")
			}
			entries, err := os.ReadDir(layerRoot)
			require.NoError(t, err)
			assert.Empty(t, entries, "拒绝的导入不得留下任何目录（含临时目录）：%v", entries)
		})
	}
}

// TestRuntimeProfilesAPI_ImportRejectsInvalidProfile 钉住 D28-1：包能解压但过不了
// 同一个 validate 时必须拒绝，且目标目录不出现（不落半成品）。
func TestRuntimeProfilesAPI_ImportRejectsInvalidProfile(t *testing.T) {
	h, layerRoot := newProfileTransferHarness(t)
	type entry = struct {
		Name string
		Data []byte
		Mode os.FileMode
	}
	zipBytes := buildRawZip(t, []entry{
		{Name: "profile.yaml", Data: []byte("name: [unclosed\n")},
		{Name: "agents/a/prompts.md", Data: []byte("x")},
	})
	rec, payload := h.importZip(t, "?layer=user", zipBytes)
	require.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	// 两条拒绝路径（Resolve 失败 → 包装错误；spec 级问题 → issues）都必须显式报错，
	// 不允许"静默半成功"。
	assert.NotEmpty(t, payload["error"], "拒绝必须带可读原因：%v", payload)
	entries, err := os.ReadDir(layerRoot)
	require.NoError(t, err)
	assert.Empty(t, entries, "拒绝的导入不得留下任何目录（含临时目录）：%v", entries)
}

func readFileOrEmpty(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(raw)
}
