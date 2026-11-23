package rest

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"micro1-interview/internal/gitea"
)

type issueUserResponse struct {
	Login     string `json:"login"`
	ID        int64  `json:"id"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
	Type      string `json:"type"`
	SiteAdmin bool   `json:"site_admin"`
}

type issueLabelResponse struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	Description *string `json:"description"`
}

type issuePullRequestResponse struct {
	HTMLURL string `json:"html_url"`
}

// issueResponse is GitHub's REST issue shape. node_id is omitted so the MCP
// server skips its optional GraphQL enrichment.
type issueResponse struct {
	ID          int64                     `json:"id"`
	Number      int64                     `json:"number"`
	Title       string                    `json:"title"`
	Body        string                    `json:"body"`
	State       string                    `json:"state"`
	Locked      bool                      `json:"locked"`
	User        *issueUserResponse        `json:"user"`
	Labels      []issueLabelResponse      `json:"labels"`
	Assignee    *issueUserResponse        `json:"assignee"`
	Assignees   []issueUserResponse       `json:"assignees"`
	Comments    int                       `json:"comments"`
	URL         string                    `json:"url"`
	HTMLURL     string                    `json:"html_url"`
	CreatedAt   time.Time                 `json:"created_at"`
	UpdatedAt   time.Time                 `json:"updated_at"`
	ClosedAt    *time.Time                `json:"closed_at"`
	PullRequest *issuePullRequestResponse `json:"pull_request,omitempty"`
}

type issueCommentResponse struct {
	ID        int64              `json:"id"`
	Body      string             `json:"body"`
	User      *issueUserResponse `json:"user"`
	HTMLURL   string             `json:"html_url"`
	IssueURL  string             `json:"issue_url"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

func mapIssueUser(user *gitea.IssueUser) *issueUserResponse {
	if user == nil {
		return nil
	}
	return &issueUserResponse{
		Login:     user.Login,
		ID:        user.ID,
		AvatarURL: user.AvatarURL,
		HTMLURL:   user.HTMLURL,
		Type:      "User",
		SiteAdmin: user.IsAdmin,
	}
}

func issueNumber(w http.ResponseWriter, r *http.Request) (int64, bool) {
	number, err := strconv.ParseInt(r.PathValue("number"), 10, 64)
	if err != nil || number < 1 {
		writeError(w, http.StatusNotFound, "Not Found")
		return 0, false
	}
	return number, true
}

func (h *Handler) issue(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	issue, err := h.gitea.Issue(r.Context(), r.Header.Get("Authorization"), owner, repo, number)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	labels := make([]issueLabelResponse, 0, len(issue.Labels))
	for _, label := range issue.Labels {
		labels = append(labels, issueLabelResponse{
			ID:          label.ID,
			Name:        label.Name,
			Color:       strings.TrimPrefix(label.Color, "#"),
			Description: nonEmpty(label.Description),
		})
	}
	assignees := make([]issueUserResponse, 0, len(issue.Assignees))
	for i := range issue.Assignees {
		assignees = append(assignees, *mapIssueUser(&issue.Assignees[i]))
	}
	out := issueResponse{
		ID:        issue.ID,
		Number:    issue.Number,
		Title:     issue.Title,
		Body:      issue.Body,
		State:     issue.State,
		Locked:    issue.IsLocked,
		User:      mapIssueUser(issue.User),
		Labels:    labels,
		Assignees: assignees,
		Comments:  issue.Comments,
		URL:       h.repoAPIURL(owner, repo) + "/issues/" + strconv.FormatInt(issue.Number, 10),
		HTMLURL:   issue.HTMLURL,
		CreatedAt: issue.Created,
		UpdatedAt: issue.Updated,
		ClosedAt:  issue.Closed,
	}
	if len(assignees) > 0 {
		out.Assignee = &assignees[0]
	}
	if issue.PullRequest != nil {
		out.PullRequest = &issuePullRequestResponse{HTMLURL: issue.PullRequest.HTMLURL}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) issueComments(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	comments, err := h.gitea.IssueComments(r.Context(), r.Header.Get("Authorization"), owner, repo, number)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}

	page := positiveQueryInt(r, "page", 1)
	perPage := min(positiveQueryInt(r, "per_page", defaultPerPage), maxPerPage)
	base := h.repoAPIURL(owner, repo) + "/issues/" + strconv.FormatInt(number, 10)

	out := []issueCommentResponse{}
	// Compare by page count first, so a huge page number cannot overflow.
	if page <= (len(comments)+perPage-1)/perPage {
		start := (page - 1) * perPage
		for _, comment := range comments[start:min(start+perPage, len(comments))] {
			out = append(out, issueCommentResponse{
				ID:        comment.ID,
				Body:      comment.Body,
				User:      mapIssueUser(comment.User),
				HTMLURL:   comment.HTMLURL,
				IssueURL:  base,
				CreatedAt: comment.Created,
				UpdatedAt: comment.Updated,
			})
		}
	}
	if link := linkHeader(base+"/comments", page, perPage, len(comments)); link != "" {
		w.Header().Set("Link", link)
	}
	writeJSON(w, http.StatusOK, out)
}

// subIssues reports a clear error, because Gitea has no sub-issue model.
func (h *Handler) subIssues(w http.ResponseWriter, _ *http.Request) {
	writeError(w, http.StatusNotImplemented, "Sub-issues are not supported yet")
}
