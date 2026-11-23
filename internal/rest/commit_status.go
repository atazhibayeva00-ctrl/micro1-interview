package rest

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"micro1-interview/internal/gitea"
)

type repoStatusResponse struct {
	ID          int64     `json:"id"`
	State       string    `json:"state"`
	Context     string    `json:"context"`
	Description *string   `json:"description"`
	TargetURL   *string   `json:"target_url"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type combinedStatusResponse struct {
	State      string               `json:"state"`
	SHA        string               `json:"sha"`
	TotalCount int                  `json:"total_count"`
	Statuses   []repoStatusResponse `json:"statuses"`
}

// statusState maps Gitea's state. GitHub has no warning state.
func statusState(state string) string {
	if state == "warning" {
		return "failure"
	}
	return state
}

// combinedState maps Gitea's overall state to GitHub's, which has no error state.
func combinedState(state string) string {
	if state == "error" {
		return "failure"
	}
	return statusState(state)
}

func (h *Handler) loadCommitStatus(w http.ResponseWriter, r *http.Request) (*gitea.CombinedStatus, bool) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return nil, false
	}
	sha := r.PathValue("sha")
	if !commitSHA.MatchString(sha) {
		writeError(w, http.StatusNotFound, "Not Found")
		return nil, false
	}
	status, err := h.gitea.CombinedCommitStatus(r.Context(), r.Header.Get("Authorization"), owner, repo, sha)
	if err != nil {
		if errors.Is(err, gitea.ErrTooManyStatuses) {
			writeError(w, http.StatusUnprocessableEntity, "Listing is limited to "+strconv.Itoa(gitea.MaxListedStatuses)+" statuses")
			return nil, false
		}
		writeLookupError(w, err)
		return nil, false
	}
	return status, true
}

func (h *Handler) commitStatus(w http.ResponseWriter, r *http.Request) {
	status, ok := h.loadCommitStatus(w, r)
	if !ok {
		return
	}
	statuses := make([]repoStatusResponse, 0, len(status.Statuses))
	for _, item := range status.Statuses {
		statuses = append(statuses, repoStatusResponse{
			ID:          item.ID,
			State:       statusState(item.Status),
			Context:     item.Context,
			Description: nonEmpty(item.Description),
			TargetURL:   nonEmpty(item.TargetURL),
			CreatedAt:   item.Created,
			UpdatedAt:   item.Updated,
		})
	}
	writeJSON(w, http.StatusOK, combinedStatusResponse{
		State:      combinedState(status.State),
		SHA:        status.SHA,
		TotalCount: len(statuses),
		Statuses:   statuses,
	})
}

type checkRunResponse struct {
	ID          int64      `json:"id"`
	HeadSHA     string     `json:"head_sha"`
	Name        string     `json:"name"`
	Status      string     `json:"status"`
	Conclusion  *string    `json:"conclusion"`
	HTMLURL     *string    `json:"html_url"`
	DetailsURL  *string    `json:"details_url"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
}

type checkRunsResponse struct {
	TotalCount int                `json:"total_count"`
	CheckRuns  []checkRunResponse `json:"check_runs"`
}

// checkRuns derives check runs from commit statuses, because Gitea has no check
// runs API and its Actions report results as commit statuses.
func (h *Handler) checkRuns(w http.ResponseWriter, r *http.Request) {
	status, ok := h.loadCommitStatus(w, r)
	if !ok {
		return
	}
	runs := make([]checkRunResponse, 0, len(status.Statuses))
	for _, item := range status.Statuses {
		run := checkRunResponse{
			ID:         item.ID,
			HeadSHA:    status.SHA,
			Name:       item.Context,
			Status:     "in_progress",
			HTMLURL:    nonEmpty(item.TargetURL),
			DetailsURL: nonEmpty(item.TargetURL),
			StartedAt:  item.Created,
		}
		if item.Status != "pending" {
			conclusion := "failure"
			if item.Status == "success" {
				conclusion = "success"
			}
			completed := item.Updated
			run.Status, run.Conclusion, run.CompletedAt = "completed", &conclusion, &completed
		}
		runs = append(runs, run)
	}
	page := positiveQueryInt(r, "page", 1)
	perPage := min(positiveQueryInt(r, "per_page", defaultPerPage), maxPerPage)
	base := h.repoAPIURL(r.PathValue("owner"), r.PathValue("repo")) + "/commits/" + r.PathValue("sha") + "/check-runs"
	if link := linkHeader(base, page, perPage, len(runs)); link != "" {
		w.Header().Set("Link", link)
	}
	writeJSON(w, http.StatusOK, checkRunsResponse{TotalCount: len(runs), CheckRuns: pageOf(runs, page, perPage)})
}
