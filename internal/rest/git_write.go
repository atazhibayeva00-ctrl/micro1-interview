package rest

import (
	"encoding/base64"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"micro1-interview/internal/gitea"
)

type createRefRequest struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

// createRef creates a branch. Only refs/heads/* is supported, so tags and other
// reference kinds are refused rather than silently misfiled.
func (h *Handler) createRef(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	authorization, ok := requireAuthorization(w, r)
	if !ok {
		return
	}
	var req createRefRequest
	if !decodeJSONBody(w, r, 1<<20, &req) {
		return
	}

	name, isBranch := strings.CutPrefix(req.Ref, "refs/heads/")
	if !isBranch || !validPath(name) {
		writeError(w, http.StatusUnprocessableEntity, "Only branch references (refs/heads/*) with a valid name are supported")
		return
	}
	if !commitSHA.MatchString(req.SHA) {
		writeError(w, http.StatusUnprocessableEntity, "Object does not exist")
		return
	}
	sha := strings.ToLower(req.SHA)

	// Gitea answers an unknown source commit with a bare 500, so check first.
	if err := h.gitea.CommitExists(r.Context(), authorization, owner, repo, sha); err != nil {
		switch statusCode(err) {
		case http.StatusBadRequest, http.StatusNotFound, http.StatusUnprocessableEntity:
			// Tell a missing repository apart from a missing commit.
			if _, repoErr := h.gitea.Repository(r.Context(), authorization, owner, repo); repoErr != nil {
				writeWriteError(w, repoErr)
				return
			}
			writeError(w, http.StatusUnprocessableEntity, "Object does not exist")
		default:
			writeWriteError(w, err)
		}
		return
	}

	if err := h.gitea.CreateBranch(r.Context(), authorization, owner, repo, name, sha); err != nil {
		switch statusCode(err) {
		case http.StatusConflict:
			writeError(w, http.StatusUnprocessableEntity, "Reference already exists")
		case http.StatusUnprocessableEntity:
			writeError(w, http.StatusUnprocessableEntity, "Reference name is invalid")
		default:
			writeWriteError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, refResponse{
		Ref:    req.Ref,
		URL:    h.repoAPIURL(owner, repo) + "/git/ref/" + gitea.EscapePath("heads/"+name),
		Object: refObject{SHA: sha, Type: "commit"},
	})
}

type blobResponse struct {
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
	URL      string `json:"url"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// blob serves a Git blob. Gitea ignores the raw media type and always returns
// base64 JSON, so the gateway decodes it. Callers verify the bytes against the
// blob SHA, so the raw form must be exact.
func (h *Handler) blob(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	sha := r.PathValue("sha")
	if !commitSHA.MatchString(sha) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	blob, err := h.gitea.Blob(r.Context(), r.Header.Get("Authorization"), owner, repo, sha)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	data, err := base64.StdEncoding.DecodeString(blob.Content)
	if blob.Encoding != "base64" || err != nil || int64(len(data)) != blob.Size {
		slog.Error("unusable gitea blob", "encoding", blob.Encoding, "decode_error", err, "size", blob.Size, "decoded", len(data))
		writeError(w, http.StatusBadGateway, "Bad Gateway")
		return
	}

	if wantsRawBlob(r.Header.Get("Accept")) {
		// Repository files are untrusted, so browsers must never render them here.
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		if _, err := w.Write(data); err != nil {
			slog.Error("write blob", "error", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, blobResponse{
		SHA:      blob.SHA,
		Size:     blob.Size,
		URL:      h.repoAPIURL(owner, repo) + "/git/blobs/" + sha,
		Content:  blob.Content,
		Encoding: "base64",
	})
}

func wantsRawBlob(accept string) bool {
	accept = strings.ToLower(accept)
	return strings.Contains(accept, "vnd.github.raw") || strings.Contains(accept, "vnd.github.v3.raw")
}
