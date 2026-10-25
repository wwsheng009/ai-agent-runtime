package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/appdevice"
)

func TestAppDevicesAPI(t *testing.T) {
	store := appdevice.NewStore()
	handler := handleAppDevices(store)

	body := `{"platform":"android","token":"secret-token-123","label":"phone"}`
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/app/devices", strings.NewReader(body)))
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status = %d body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "secret-token-123") {
		t.Fatalf("create response leaked token: %s", rr.Body.String())
	}
	var created struct {
		Device  appdevice.PublicDevice `json:"device"`
		Created bool                   `json:"created"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if !created.Created || created.Device.ID == "" {
		t.Fatalf("create response = %+v", created)
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/app/devices",
		strings.NewReader(`{"platform":"android","token":"secret-token-123","label":"new"}`)))
	if rr.Code != http.StatusOK {
		t.Fatalf("upsert status = %d body=%s", rr.Code, rr.Body.String())
	}
	var updated struct {
		Device  appdevice.PublicDevice `json:"device"`
		Created bool                   `json:"created"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Created || updated.Device.ID != created.Device.ID || updated.Device.Label != "new" {
		t.Fatalf("upsert = %+v", updated)
	}

	rr = httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/app/devices", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list status = %d", rr.Code)
	}
	if strings.Contains(rr.Body.String(), "secret-token-123") {
		t.Fatalf("list response leaked token: %s", rr.Body.String())
	}

	rr = httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/api/app/devices/"+created.Device.ID, nil)
	delReq = mux.SetURLVars(delReq, map[string]string{"id": created.Device.ID})
	handleAppDeviceDelete(store).ServeHTTP(rr, delReq)
	if rr.Code != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", rr.Code, rr.Body.String())
	}
	if store.Count() != 0 {
		t.Fatalf("count after delete = %d", store.Count())
	}

	rr = httptest.NewRecorder()
	delReq = httptest.NewRequest(http.MethodDelete, "/api/app/devices/"+created.Device.ID, nil)
	delReq = mux.SetURLVars(delReq, map[string]string{"id": created.Device.ID})
	handleAppDeviceDelete(store).ServeHTTP(rr, delReq)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d", rr.Code)
	}
}

func TestAppDevicesAPIRejectsInvalidAndNilStore(t *testing.T) {
	store := appdevice.NewStore()
	handler := handleAppDevices(store)
	for _, body := range []string{
		`{"platform":"symbian","token":"x"}`,
		`{"platform":"ios","token":""}`,
		`not-json`,
	} {
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/app/devices", strings.NewReader(body)))
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("body %q status = %d", body, rr.Code)
		}
	}

	rr := httptest.NewRecorder()
	handleAppDevices(nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/app/devices", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil store status = %d", rr.Code)
	}
}
