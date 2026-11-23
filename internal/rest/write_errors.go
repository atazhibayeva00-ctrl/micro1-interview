package rest

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"micro1-interview/internal/gitea"
)

// maxWriteBodyBytes caps a write request. A 10 MiB file is about 14 MiB once
// base64 encoded, so this leaves ample room.
const maxWriteBodyBytes = 50 << 20

// Gitea reports a stale blob SHA as a plain 422, so the text is the only way
// to tell it apart. A conformance test runs against real Gitea to catch any
// change in this wording when Gitea is upgraded.
const (
	giteaStaleSHA   = "sha does not match"
	giteaFileExists = "already exists"
	giteaSymlink    = "symbolic link"
)

func statusCode(err error) int {
	var statusErr *gitea.StatusError
	if errors.As(err, &statusErr) {
		return statusErr.Code
	}
	return 0
}

func statusMessage(err error) string {
	var statusErr *gitea.StatusError
	if errors.As(err, &statusErr) {
		return strings.ToLower(statusErr.Message)
	}
	return ""
}

// writeWriteError maps a failed write to a GitHub-format error without
// exposing Gitea's own message.
func writeWriteError(w http.ResponseWriter, err error) {
	switch statusCode(err) {
	case http.StatusConflict:
		writeError(w, http.StatusConflict, "Conflict")
	case http.StatusUnprocessableEntity:
		writeError(w, http.StatusUnprocessableEntity, "Validation Failed")
	case http.StatusLocked:
		writeError(w, http.StatusForbidden, "Repository is archived or locked")
	default:
		writeUpstreamError(w, err)
	}
}

// decodeJSONBody reads a capped JSON request body and reports any failure.
func decodeJSONBody(w http.ResponseWriter, r *http.Request, limit int64, out any) bool {
	// Refuse a declared oversize body without spending CPU parsing it.
	if r.ContentLength > limit {
		writeError(w, http.StatusRequestEntityTooLarge, "Request Entity Too Large")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if err := json.NewDecoder(r.Body).Decode(out); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "Request Entity Too Large")
			return false
		}
		writeError(w, http.StatusBadRequest, "Problems parsing JSON")
		return false
	}
	return true
}

// requireAuthorization stops anonymous writes before they reach Gitea.
func requireAuthorization(w http.ResponseWriter, r *http.Request) (string, bool) {
	authorization := r.Header.Get("Authorization")
	if authorization == "" {
		writeError(w, http.StatusUnauthorized, "Requires authentication")
		return "", false
	}
	return authorization, true
}
