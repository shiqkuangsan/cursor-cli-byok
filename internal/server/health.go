package server

import (
	"encoding/json"
	"net/http"
)

type HealthResponse struct {
	Status        string `json:"status"`
	InstanceID    string `json:"instance_id"`
	DaemonVersion string `json:"daemon_version"`
}

func healthHandler(instanceID, daemonVersion string) http.Handler {
	response := HealthResponse{
		Status:        "ok",
		InstanceID:    instanceID,
		DaemonVersion: daemonVersion,
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		_ = json.NewEncoder(writer).Encode(response)
	})
}
