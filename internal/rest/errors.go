package rest

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"micro1-interview/internal/gitea"
)

type errorResponse struct {
	Message string `json:"message"`
	Status  string `json:"status"`
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write response", "error", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorResponse{Message: message, Status: strconv.Itoa(status)})
}

// writeUpstreamError maps a Gitea failure to a GitHub-format error without
// exposing upstream response details.
func writeUpstreamError(w http.ResponseWriter, err error) {
	var statusErr *gitea.StatusError
	if errors.As(err, &statusErr) {
		switch statusErr.Code {
		case http.StatusUnauthorized:
			writeError(w, http.StatusUnauthorized, "Bad credentials")
			return
		case http.StatusForbidden:
			writeError(w, http.StatusForbidden, "Forbidden")
			return
		case http.StatusNotFound:
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}
	}
	slog.Error("gitea request failed", "error", err)
	writeError(w, http.StatusBadGateway, "Bad Gateway")
}
