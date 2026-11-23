package rest

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"micro1-interview/internal/gitea"
)

const (
	defaultPerPage = 30
	maxPerPage     = 100
)

// validSegment rejects names that could change which Gitea route is called.
func validSegment(segment string) bool {
	return segment != "" && segment != "." && segment != ".." && !strings.ContainsAny(segment, "/\\")
}

// validPath reports whether every segment of a slash-separated path is safe.
func validPath(path string) bool {
	for segment := range strings.SplitSeq(path, "/") {
		if !validSegment(segment) {
			return false
		}
	}
	return true
}

func repoFromRequest(w http.ResponseWriter, r *http.Request) (owner, repo string, ok bool) {
	owner, repo = r.PathValue("owner"), r.PathValue("repo")
	if !validSegment(owner) || !validSegment(repo) {
		writeError(w, http.StatusNotFound, "Not Found")
		return "", "", false
	}
	return owner, repo, true
}

func (h *Handler) repoAPIURL(owner, repo string) string {
	return h.publicURL + "/api/v3/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

type repositoryOwner struct {
	Login string `json:"login"`
	ID    int64  `json:"id"`
}

type repositoryResponse struct {
	ID            int64           `json:"id"`
	Name          string          `json:"name"`
	FullName      string          `json:"full_name"`
	Owner         repositoryOwner `json:"owner"`
	Private       bool            `json:"private"`
	Visibility    string          `json:"visibility"`
	Fork          bool            `json:"fork"`
	Archived      bool            `json:"archived"`
	HTMLURL       string          `json:"html_url"`
	Description   *string         `json:"description"`
	DefaultBranch string          `json:"default_branch"`
}

func (h *Handler) repository(w http.ResponseWriter, r *http.Request) {
	owner, name, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	repo, err := h.gitea.Repository(r.Context(), r.Header.Get("Authorization"), owner, name)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	visibility := "public"
	switch {
	case repo.Private:
		visibility = "private"
	case repo.Internal:
		visibility = "internal"
	}
	writeJSON(w, http.StatusOK, repositoryResponse{
		ID:            repo.ID,
		Name:          repo.Name,
		FullName:      repo.FullName,
		Owner:         repositoryOwner{Login: repo.Owner.Login, ID: repo.Owner.ID},
		Private:       repo.Private,
		Visibility:    visibility,
		Fork:          repo.Fork,
		Archived:      repo.Archived,
		HTMLURL:       repo.HTMLURL,
		Description:   nonEmpty(repo.Description),
		DefaultBranch: repo.DefaultBranch,
	})
}

type refObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type refResponse struct {
	Ref    string    `json:"ref"`
	URL    string    `json:"url"`
	Object refObject `json:"object"`
}

func (h *Handler) gitRef(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	name := strings.TrimPrefix(r.PathValue("ref"), "refs/")
	if !validPath(name) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	ref, err := h.gitea.Ref(r.Context(), r.Header.Get("Authorization"), owner, repo, name)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, refResponse{
		Ref:    ref.Ref,
		URL:    h.repoAPIURL(owner, repo) + "/git/ref/" + gitea.EscapePath(name),
		Object: refObject{SHA: ref.Object.SHA, Type: ref.Object.Type},
	})
}

type branchCommit struct {
	SHA string `json:"sha"`
	URL string `json:"url"`
}

type branchResponse struct {
	Name      string       `json:"name"`
	Commit    branchCommit `json:"commit"`
	Protected bool         `json:"protected"`
}

func (h *Handler) branches(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	page := positiveQueryInt(r, "page", 1)
	perPage := min(positiveQueryInt(r, "per_page", defaultPerPage), maxPerPage)

	branches, total, err := h.gitea.Branches(r.Context(), r.Header.Get("Authorization"), owner, repo, page, perPage)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	base := h.repoAPIURL(owner, repo)
	out := make([]branchResponse, 0, len(branches))
	for _, branch := range branches {
		out = append(out, branchResponse{
			Name:      branch.Name,
			Commit:    branchCommit{SHA: branch.Commit.ID, URL: base + "/commits/" + url.PathEscape(branch.Commit.ID)},
			Protected: branch.Protected,
		})
	}
	if link := linkHeader(base+"/branches", page, perPage, total); link != "" {
		w.Header().Set("Link", link)
	}
	writeJSON(w, http.StatusOK, out)
}

func positiveQueryInt(r *http.Request, key string, fallback int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(key))
	if err != nil || value < 1 {
		return fallback
	}
	return value
}

// linkHeader builds GitHub's pagination header using the gateway's public URL.
func linkHeader(base string, page, perPage, total int) string {
	last := max((total+perPage-1)/perPage, 1)
	link := func(target int, rel string) string {
		return fmt.Sprintf(`<%s?page=%d&per_page=%d>; rel="%s"`, base, target, perPage, rel)
	}
	var links []string
	if page > 1 {
		links = append(links, link(min(page-1, last), "prev"), link(1, "first"))
	}
	if page < last {
		links = append(links, link(page+1, "next"), link(last, "last"))
	}
	return strings.Join(links, ", ")
}
