package skills

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/stretchr/testify/require"
	"github.com/wwsheng009/ai-agent-runtime/internal/filetransport"
	"github.com/wwsheng009/ai-agent-runtime/internal/skill"
)

func TestRuntimeFileTransferWriteAppendRead(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "novel", "chapter-7.md")

	handler := NewHandler(skill.NewRegistry(nil), nil, nil)
	handler.SetFileTransferService(filetransport.NewLocalService())

	router := mux.NewRouter()
	handler.RegisterRoutes(router)

	writePayload, err := json.Marshal(map[string]string{
		"path":        target,
		"data_base64": base64.StdEncoding.EncodeToString([]byte("第一段")),
	})
	require.NoError(t, err)
	writeReq := httptest.NewRequest(http.MethodPost, "/api/runtime/fs/write-file", strings.NewReader(string(writePayload)))
	writeRec := httptest.NewRecorder()
	router.ServeHTTP(writeRec, writeReq)
	require.Equal(t, http.StatusOK, writeRec.Code)

	appendPayload, err := json.Marshal(map[string]string{
		"path":        target,
		"data_base64": base64.StdEncoding.EncodeToString([]byte("\n第二段")),
	})
	require.NoError(t, err)
	appendReq := httptest.NewRequest(http.MethodPost, "/api/runtime/fs/append-file", strings.NewReader(string(appendPayload)))
	appendRec := httptest.NewRecorder()
	router.ServeHTTP(appendRec, appendReq)
	require.Equal(t, http.StatusAccepted, appendRec.Code)

	readPayload, err := json.Marshal(map[string]string{"path": target})
	require.NoError(t, err)
	readReq := httptest.NewRequest(http.MethodPost, "/api/runtime/fs/read-file", strings.NewReader(string(readPayload)))
	readRec := httptest.NewRecorder()
	router.ServeHTTP(readRec, readReq)
	require.Equal(t, http.StatusOK, readRec.Code)

	var payload map[string]map[string]interface{}
	require.NoError(t, json.Unmarshal(readRec.Body.Bytes(), &payload))
	encoded, _ := payload["file"]["data_base64"].(string)
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Equal(t, "第一段\n第二段", string(decoded))
}
