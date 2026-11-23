package rest

import (
	"log/slog"
	"net/http"
	"strings"

	"micro1-interview/internal/gitea"
)

// putContentsRequest accepts the whole GitHub body but reads only these fields.
// author and committer are ignored, so every commit is attributed to the
// authenticated user.
type putContentsRequest struct {
	Message *string `json:"message"`
	Content *string `json:"content"`
	Branch  string  `json:"branch"`
	SHA     string  `json:"sha"`
}

type commitPerson struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date,omitempty"`
}

type fileCommitResponse struct {
	SHA       string        `json:"sha"`
	URL       string        `json:"url"`
	HTMLURL   string        `json:"html_url"`
	Message   string        `json:"message"`
	Author    *commitPerson `json:"author,omitempty"`
	Committer *commitPerson `json:"committer,omitempty"`
}

type putContentsResponse struct {
	Content contentResponse    `json:"content"`
	Commit  fileCommitResponse `json:"commit"`
}

func (h *Handler) putContents(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	path := strings.Trim(r.PathValue("path"), "/")
	if !validPath(path) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	authorization, ok := requireAuthorization(w, r)
	if !ok {
		return
	}
	var req putContentsRequest
	if !decodeJSONBody(w, r, maxWriteBodyBytes, &req) {
		return
	}
	switch {
	case req.Message == nil || *req.Message == "":
		writeError(w, http.StatusUnprocessableEntity, `Invalid request. "message" wasn't supplied.`)
		return
	case req.Content == nil:
		writeError(w, http.StatusUnprocessableEntity, `Invalid request. "content" wasn't supplied.`)
		return
	case req.Branch != "" && !validPath(req.Branch):
		writeError(w, http.StatusUnprocessableEntity, "Branch name is invalid")
		return
	case req.SHA != "" && !commitSHA.MatchString(req.SHA):
		writeError(w, http.StatusUnprocessableEntity, `Invalid request. "sha" must be a blob SHA.`)
		return
	}

	result, created, err := h.gitea.PutFile(r.Context(), authorization, owner, repo, path, gitea.FileWrite{
		Message: *req.Message,
		Content: *req.Content,
		Branch:  req.Branch,
		SHA:     req.SHA,
	})
	if err != nil {
		h.writeFileError(w, err, path)
		return
	}
	if result.Content == nil || result.Commit == nil {
		slog.Error("gitea file write response is incomplete")
		writeError(w, http.StatusBadGateway, "Bad Gateway")
		return
	}

	content := h.mapContent(owner, repo, req.Branch, result.Content)
	// GitHub returns file metadata here, never the file body.
	content.Encoding, content.Content = nil, nil

	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, putContentsResponse{
		Content: content,
		Commit:  h.mapFileCommit(owner, repo, result.Commit),
	})
}

func (h *Handler) mapFileCommit(owner, repo string, commit *gitea.FileCommit) fileCommitResponse {
	person := func(user *gitea.CommitUser) *commitPerson {
		if user == nil {
			return nil
		}
		return &commitPerson{Name: user.Name, Email: user.Email, Date: user.Date}
	}
	return fileCommitResponse{
		SHA:       commit.SHA,
		URL:       h.repoAPIURL(owner, repo) + "/git/commits/" + commit.SHA,
		HTMLURL:   commit.HTMLURL,
		Message:   commit.Message,
		Author:    person(commit.Author),
		Committer: person(commit.Committer),
	}
}

// writeFileError maps Gitea's file-write failures to GitHub's status codes.
func (h *Handler) writeFileError(w http.ResponseWriter, err error, path string) {
	if statusCode(err) == http.StatusUnprocessableEntity {
		message := statusMessage(err)
		switch {
		// Gitea's contents API cannot write through or retarget a symlink.
		case strings.Contains(message, giteaSymlink):
			writeError(w, http.StatusUnprocessableEntity, "Updating a symbolic link is not supported by this server")
			return
		case strings.Contains(message, giteaStaleSHA):
			writeError(w, http.StatusConflict, path+" does not match the supplied sha")
			return
		case strings.Contains(message, giteaFileExists):
			writeError(w, http.StatusUnprocessableEntity, `Invalid request. "sha" wasn't supplied.`)
			return
		case strings.Contains(message, "base64"):
			writeError(w, http.StatusUnprocessableEntity, `Invalid request. "content" is not valid Base64.`)
			return
		}
	}
	writeWriteError(w, err)
}
