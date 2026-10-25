package main

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/gorilla/mux"

	"github.com/wwsheng009/ai-agent-runtime/internal/gateway/appdevice"
)

// handleAppDevices lists and registers native app push tokens. The token is a
// bearer-like capability for notifications, so GET responses only expose a
// short hint and never the full value.
func handleAppDevices(store *appdevice.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "app devices unavailable"})
			return
		}
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{
				"devices": store.PublicList(),
				"count":   store.Count(),
			})
		case http.MethodPost:
			var req struct {
				Platform string `json:"platform"`
				Token    string `json:"token"`
				Label    string `json:"label"`
			}
			dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
			if err := dec.Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid JSON body"})
				return
			}
			device, created, err := store.Register(req.Platform, req.Token, req.Label)
			if err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
				return
			}
			status := http.StatusOK
			if created {
				status = http.StatusCreated
			}
			writeJSON(w, status, map[string]any{
				"device":  device,
				"created": created,
			})
		default:
			w.Header().Set("Allow", "GET, POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	}
}

// handleAppDeviceDelete revokes one app notification registration.
func handleAppDeviceDelete(store *appdevice.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if store == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"error": "app devices unavailable"})
			return
		}
		id := strings.TrimSpace(mux.Vars(r)["id"])
		if id == "" {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": "device id is required"})
			return
		}
		if !store.Delete(id) {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": appdevice.ErrNotFound.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true, "id": id})
	}
}
