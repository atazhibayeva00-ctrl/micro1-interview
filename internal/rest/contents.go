package rest

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"micro1-interview/internal/gitea"
)

const (
	// GitHub stops inlining file content at 1 MB.
	maxInlineContentBytes = 1024 * 1024
	rawDownloadTimeout    = 50 * time.Second
)

var commitSHA = regexp.MustCompile(`^(?i:[0-9a-f]{40}|[0-9a-f]{64})$`)

// contentResponse is GitHub's shape for a file or directory entry. git_url is
// null because the gateway does not serve the Git blobs API yet.
type contentResponse struct {
	Type            string  `json:"type"`
	Name            string  `json:"name"`
	Path            string  `json:"path"`
	Size            int64   `json:"size"`
	SHA             string  `json:"sha"`
	Encoding        *string `json:"encoding,omitempty"`
	Content         *string `json:"content,omitempty"`
	Target          *string `json:"target,omitempty"`
	SubmoduleGitURL *string `json:"submodule_git_url,omitempty"`
	URL             string  `json:"url"`
	GitURL          *string `json:"git_url"`
	HTMLURL         *string `json:"html_url"`
	DownloadURL     *string `json:"download_url"`
}

func (h *Handler) contents(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	path := strings.Trim(r.PathValue("path"), "/")
	if path != "" && !validPath(path) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	ref := r.URL.Query().Get("ref")

	file, entries, err := h.gitea.Contents(r.Context(), r.Header.Get("Authorization"), owner, repo, path, ref)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	if file != nil {
		out := h.mapContent(owner, repo, ref, file)
		if file.Size >= maxInlineContentBytes {
			none := "none"
			out.Encoding, out.Content = &none, nil
		}
		writeJSON(w, http.StatusOK, out)
		return
	}
	out := make([]contentResponse, 0, len(entries))
	for i := range entries {
		entry := h.mapContent(owner, repo, ref, &entries[i])
		entry.Encoding, entry.Content = nil, nil
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) mapContent(owner, repo, ref string, entry *gitea.Content) contentResponse {
	address := h.repoAPIURL(owner, repo) + "/contents/" + gitea.EscapePath(entry.Path)
	if ref != "" {
		address += "?" + url.Values{"ref": {ref}}.Encode()
	}
	out := contentResponse{
		Type:            entry.Type,
		Name:            entry.Name,
		Path:            entry.Path,
		Size:            entry.Size,
		SHA:             entry.SHA,
		Encoding:        entry.Encoding,
		Content:         entry.Content,
		Target:          entry.Target,
		SubmoduleGitURL: entry.SubmoduleGitURL,
		URL:             address,
		HTMLURL:         entry.HTMLURL,
	}
	if entry.Type == "file" {
		if commit := downloadCommit(entry, ref); commit != "" {
			download := h.publicURL + "/raw/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) +
				"/" + commit + "/" + gitea.EscapePath(entry.Path)
			out.DownloadURL = &download
		}
	}
	return out
}

// downloadCommit picks a commit for the raw link, because a branch name with a
// slash cannot be told apart from the file path in that URL.
func downloadCommit(entry *gitea.Content, ref string) string {
	if entry.LastCommitSHA != nil && commitSHA.MatchString(*entry.LastCommitSHA) {
		return *entry.LastCommitSHA
	}
	if commitSHA.MatchString(ref) {
		return ref
	}
	return ""
}

type treeEntryResponse struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size *int64 `json:"size,omitempty"`
}

type treeResponse struct {
	SHA       string              `json:"sha"`
	URL       string              `json:"url"`
	Tree      []treeEntryResponse `json:"tree"`
	Truncated bool                `json:"truncated"`
}

func (h *Handler) tree(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	sha := r.PathValue("sha")
	if !validSegment(sha) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	// GitHub treats any value of recursive as enabled.
	recursive := r.URL.Query().Has("recursive")
	tree, err := h.gitea.Tree(r.Context(), r.Header.Get("Authorization"), owner, repo, sha, recursive)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	entries := make([]treeEntryResponse, 0, len(tree.Entries))
	for _, entry := range tree.Entries {
		out := treeEntryResponse{Path: entry.Path, Mode: entry.Mode, Type: entry.Type, SHA: entry.SHA}
		if entry.Type == "blob" {
			size := entry.Size
			out.Size = &size
		}
		entries = append(entries, out)
	}
	writeJSON(w, http.StatusOK, treeResponse{
		SHA:       tree.SHA,
		URL:       h.repoAPIURL(owner, repo) + "/git/trees/" + url.PathEscape(tree.SHA),
		Tree:      entries,
		Truncated: tree.Truncated,
	})
}

func (h *Handler) raw(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	sha, path := r.PathValue("sha"), r.PathValue("path")
	if !commitSHA.MatchString(sha) || !validPath(path) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rawDownloadTimeout)
	defer cancel()
	resp, err := h.gitea.Raw(ctx, r.Header.Get("Authorization"), owner, repo, sha, path)
	if err != nil {
		writeLookupError(w, err)
		return
	}
	defer resp.Body.Close()

	// Repository files are untrusted, so browsers must never render them here.
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	if resp.ContentLength >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(resp.ContentLength, 10))
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		slog.Error("stream raw file", "error", err)
	}
}

// writeLookupError reports Gitea's invalid-ref responses as not found, which is
// how GitHub answers a request for an unknown commit or tree.
func writeLookupError(w http.ResponseWriter, err error) {
	var statusErr *gitea.StatusError
	if errors.As(err, &statusErr) &&
		(statusErr.Code == http.StatusBadRequest || statusErr.Code == http.StatusUnprocessableEntity) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeUpstreamError(w, err)
}
