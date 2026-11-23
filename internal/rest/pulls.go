package rest

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"micro1-interview/internal/gitea"
)

const (
	// maxDiffBytes bounds the memory one diff request can use.
	maxDiffBytes     = 10 << 20
	diffTimeout      = 30 * time.Second
	maxPullBodyBytes = 1 << 20
)

// Gitea marks a pull request as a draft through a title prefix.
const draftTitlePrefix = "WIP: "

var giteaDraftPrefixes = []string{"WIP:", "[WIP]"}

type pullRepoResponse struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	FullName    string  `json:"full_name"`
	Description *string `json:"description"`
	Private     bool    `json:"private"`
}

type pullBranchResponse struct {
	Label string            `json:"label"`
	Ref   string            `json:"ref"`
	SHA   string            `json:"sha"`
	Repo  *pullRepoResponse `json:"repo"`
}

type milestoneResponse struct {
	Title string `json:"title"`
}

// pullResponse is GitHub's pull request shape. mergeable is null while unknown.
type pullResponse struct {
	ID                  int64                `json:"id"`
	Number              int64                `json:"number"`
	State               string               `json:"state"`
	Locked              bool                 `json:"locked"`
	Title               string               `json:"title"`
	Body                string               `json:"body"`
	User                *issueUserResponse   `json:"user"`
	Labels              []issueLabelResponse `json:"labels"`
	Milestone           *milestoneResponse   `json:"milestone"`
	Assignees           []issueUserResponse  `json:"assignees"`
	RequestedReviewers  []issueUserResponse  `json:"requested_reviewers"`
	Head                pullBranchResponse   `json:"head"`
	Base                pullBranchResponse   `json:"base"`
	Draft               bool                 `json:"draft"`
	Merged              bool                 `json:"merged"`
	Mergeable           *bool                `json:"mergeable"`
	MergeableState      string               `json:"mergeable_state"`
	MergedBy            *issueUserResponse   `json:"merged_by"`
	MergeCommitSHA      *string              `json:"merge_commit_sha"`
	Comments            *int                 `json:"comments,omitempty"`
	Commits             *int                 `json:"commits,omitempty"`
	Additions           int                  `json:"additions"`
	Deletions           int                  `json:"deletions"`
	ChangedFiles        int                  `json:"changed_files"`
	MaintainerCanModify bool                 `json:"maintainer_can_modify"`
	URL                 string               `json:"url"`
	HTMLURL             string               `json:"html_url"`
	DiffURL             string               `json:"diff_url"`
	CreatedAt           time.Time            `json:"created_at"`
	UpdatedAt           time.Time            `json:"updated_at"`
	ClosedAt            *time.Time           `json:"closed_at"`
	MergedAt            *time.Time           `json:"merged_at"`
}

func mapIssueUsers(users []gitea.IssueUser) []issueUserResponse {
	out := make([]issueUserResponse, 0, len(users))
	for i := range users {
		out = append(out, *mapIssueUser(&users[i]))
	}
	return out
}

func mapPullBranch(branch *gitea.PRBranch) pullBranchResponse {
	ref := branch.Ref
	// Gitea rewrites the ref once the head branch is deleted. The label keeps its name.
	if strings.HasPrefix(ref, "refs/pull/") && branch.Label != "" {
		ref = branch.Label
	}
	out := pullBranchResponse{Label: ref, Ref: ref, SHA: branch.SHA}
	if repo := branch.Repo; repo != nil {
		out.Label = repo.Owner.Login + ":" + ref
		out.Repo = &pullRepoResponse{
			ID:          repo.ID,
			Name:        repo.Name,
			FullName:    repo.FullName,
			Description: nonEmpty(repo.Description),
			Private:     repo.Private,
		}
	}
	return out
}

// displayTitle removes the prefix Gitea needs to mark a draft.
func displayTitle(pull *gitea.Pull) string {
	if !pull.Draft {
		return pull.Title
	}
	for _, prefix := range giteaDraftPrefixes {
		if len(pull.Title) >= len(prefix) && strings.EqualFold(pull.Title[:len(prefix)], prefix) {
			return strings.TrimSpace(pull.Title[len(prefix):])
		}
	}
	return pull.Title
}

// mergeability never reports a conflict from Gitea's false value, because that
// value also means the check has not finished.
func mergeability(pull *gitea.Pull) (*bool, string) {
	switch {
	case pull.State != "open":
		return nil, "unknown"
	case pull.Draft:
		return nil, "draft"
	case pull.Mergeable:
		mergeable := true
		return &mergeable, "clean"
	}
	return nil, "unknown"
}

func (h *Handler) mapPull(owner, repo string, pull *gitea.Pull) pullResponse {
	labels := make([]issueLabelResponse, 0, len(pull.Labels))
	for _, label := range pull.Labels {
		labels = append(labels, issueLabelResponse{
			ID:          label.ID,
			Name:        label.Name,
			Color:       strings.TrimPrefix(label.Color, "#"),
			Description: nonEmpty(label.Description),
		})
	}
	mergeable, mergeableState := mergeability(pull)
	out := pullResponse{
		ID:                  pull.ID,
		Number:              pull.Number,
		State:               pull.State,
		Locked:              pull.IsLocked,
		Title:               displayTitle(pull),
		Body:                pull.Body,
		User:                mapIssueUser(pull.User),
		Labels:              labels,
		Assignees:           mapIssueUsers(pull.Assignees),
		RequestedReviewers:  mapIssueUsers(pull.RequestedReviewers),
		Head:                mapPullBranch(&pull.Head),
		Base:                mapPullBranch(&pull.Base),
		Draft:               pull.Draft,
		Merged:              pull.Merged,
		Mergeable:           mergeable,
		MergeableState:      mergeableState,
		MergedBy:            mapIssueUser(pull.MergedBy),
		MergeCommitSHA:      pull.MergeCommitSHA,
		Additions:           pull.Additions,
		Deletions:           pull.Deletions,
		ChangedFiles:        pull.ChangedFiles,
		MaintainerCanModify: pull.AllowMaintainerEdit,
		URL:                 h.repoAPIURL(owner, repo) + "/pulls/" + strconv.FormatInt(pull.Number, 10),
		HTMLURL:             pull.HTMLURL,
		DiffURL:             pull.HTMLURL + ".diff",
		CreatedAt:           pull.Created,
		UpdatedAt:           pull.Updated,
		ClosedAt:            pull.Closed,
		MergedAt:            pull.MergedAt,
	}
	if pull.Milestone != nil {
		out.Milestone = &milestoneResponse{Title: pull.Milestone.Title}
	}
	return out
}

// addPullCounts fills the counts Gitea does not report accurately on the pull
// request itself. Gitea's comments value also includes review comments.
func (h *Handler) addPullCounts(ctx context.Context, authorization, owner, repo string, out *pullResponse) error {
	if err := h.keepPendingReviewers(ctx, authorization, owner, repo, out); err != nil {
		return err
	}
	_, commits, err := h.gitea.PullCommits(ctx, authorization, owner, repo, out.Number, 1, 1)
	switch {
	case err == nil:
		out.Commits = &commits
	case statusCode(err) != http.StatusNotFound:
		return err
	}
	comments, err := h.gitea.IssueComments(ctx, authorization, owner, repo, out.Number)
	switch {
	case err == nil:
		count := len(comments)
		out.Comments = &count
	case statusCode(err) != http.StatusForbidden:
		return err
	}
	return nil
}

// keepPendingReviewers removes users who already reviewed. Gitea keeps them in
// requested_reviewers, while GitHub lists only reviews still awaited.
func (h *Handler) keepPendingReviewers(ctx context.Context, authorization, owner, repo string, out *pullResponse) error {
	reviews, err := h.gitea.PullReviews(ctx, authorization, owner, repo, out.Number)
	if err != nil {
		if errors.Is(err, gitea.ErrTooManyReviews) {
			return nil
		}
		return err
	}
	pending := map[int64]bool{}
	for i := range reviews {
		if reviews[i].State == "REQUEST_REVIEW" && reviews[i].User != nil {
			pending[reviews[i].User.ID] = true
		}
	}
	kept := make([]issueUserResponse, 0, len(out.RequestedReviewers))
	for _, reviewer := range out.RequestedReviewers {
		if pending[reviewer.ID] {
			kept = append(kept, reviewer)
		}
	}
	out.RequestedReviewers = kept
	return nil
}

// validPullHead accepts "branch" or "owner:branch".
func validPullHead(head string) bool {
	owner, branch, qualified := strings.Cut(head, ":")
	if !qualified {
		return validPath(head)
	}
	return validSegment(owner) && validPath(branch) && !strings.Contains(branch, ":")
}

type createPullRequest struct {
	Title               string `json:"title"`
	Head                string `json:"head"`
	Base                string `json:"base"`
	Body                string `json:"body"`
	Draft               bool   `json:"draft"`
	MaintainerCanModify *bool  `json:"maintainer_can_modify"`
}

func (h *Handler) createPull(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	authorization, ok := requireAuthorization(w, r)
	if !ok {
		return
	}
	var req createPullRequest
	if !decodeJSONBody(w, r, maxPullBodyBytes, &req) {
		return
	}
	title := strings.TrimSpace(req.Title)
	switch {
	case title == "":
		writeError(w, http.StatusUnprocessableEntity, "Pull request title is required")
		return
	case !validPullHead(req.Head):
		writeError(w, http.StatusUnprocessableEntity, "Head branch is invalid")
		return
	case !validPath(req.Base):
		writeError(w, http.StatusUnprocessableEntity, "Base branch is invalid")
		return
	}

	// Gitea would open an empty pull request, which GitHub refuses.
	count, err := h.gitea.CompareCommitCount(r.Context(), authorization, owner, repo, req.Base, req.Head)
	if err != nil {
		if statusCode(err) == http.StatusNotFound {
			if _, repoErr := h.gitea.Repository(r.Context(), authorization, owner, repo); repoErr != nil {
				writeWriteError(w, repoErr)
				return
			}
			writeError(w, http.StatusUnprocessableEntity, "Head or base branch does not exist")
			return
		}
		writeWriteError(w, err)
		return
	}
	if count == 0 {
		writeError(w, http.StatusUnprocessableEntity, "No commits between "+req.Base+" and "+req.Head)
		return
	}

	if req.Draft {
		title = draftTitlePrefix + title
	}
	pull, err := h.gitea.CreatePull(r.Context(), authorization, owner, repo, gitea.PullCreate{
		Title:               title,
		Head:                req.Head,
		Base:                req.Base,
		Body:                req.Body,
		AllowMaintainerEdit: req.MaintainerCanModify,
	})
	if err != nil {
		switch statusCode(err) {
		case http.StatusConflict:
			writeError(w, http.StatusUnprocessableEntity, "A pull request already exists for "+req.Head)
		case http.StatusNotFound:
			writeError(w, http.StatusUnprocessableEntity, "Head or base branch does not exist")
		default:
			writeWriteError(w, err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, h.mapPull(owner, repo, pull))
}

type requestReviewersRequest struct {
	Reviewers     []string `json:"reviewers"`
	TeamReviewers []string `json:"team_reviewers"`
}

func (h *Handler) requestReviewers(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	authorization, ok := requireAuthorization(w, r)
	if !ok {
		return
	}
	var req requestReviewersRequest
	if !decodeJSONBody(w, r, 1<<20, &req) {
		return
	}
	for _, name := range append(append([]string{}, req.Reviewers...), req.TeamReviewers...) {
		if !validSegment(name) {
			writeError(w, http.StatusUnprocessableEntity, "Reviewer name is invalid")
			return
		}
	}
	if _, err := h.gitea.Pull(r.Context(), authorization, owner, repo, number); err != nil {
		writeWriteError(w, err)
		return
	}
	if err := h.gitea.RequestReviewers(r.Context(), authorization, owner, repo, number, req.Reviewers, req.TeamReviewers); err != nil {
		if statusCode(err) == http.StatusNotFound {
			writeError(w, http.StatusUnprocessableEntity, "Reviews may only be requested from existing users and teams")
			return
		}
		writeWriteError(w, err)
		return
	}
	pull, err := h.gitea.Pull(r.Context(), authorization, owner, repo, number)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	out := h.mapPull(owner, repo, pull)
	if err := h.keepPendingReviewers(r.Context(), authorization, owner, repo, &out); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) pull(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	authorization := r.Header.Get("Authorization")
	accept := strings.ToLower(r.Header.Get("Accept"))
	if strings.Contains(accept, ".patch") {
		writeError(w, http.StatusUnsupportedMediaType, "The patch media type is not supported")
		return
	}
	if strings.Contains(accept, ".diff") {
		h.writePullDiff(w, r, authorization, owner, repo, number)
		return
	}
	pull, err := h.gitea.Pull(r.Context(), authorization, owner, repo, number)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	out := h.mapPull(owner, repo, pull)
	if err := h.addPullCounts(r.Context(), authorization, owner, repo, &out); err != nil {
		writeUpstreamError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handler) writePullDiff(w http.ResponseWriter, r *http.Request, authorization, owner, repo string, number int64) {
	ctx, cancel := context.WithTimeout(r.Context(), diffTimeout)
	defer cancel()
	diff, err := h.gitea.PullDiff(ctx, authorization, owner, repo, number, maxDiffBytes)
	if err != nil {
		if errors.Is(err, gitea.ErrDiffTooLarge) {
			writeError(w, http.StatusNotAcceptable, "Sorry, the diff exceeded the maximum size")
			return
		}
		writeUpstreamError(w, err)
		return
	}
	// Diffs hold untrusted repository content, so browsers must never render them.
	w.Header().Set("Content-Type", "application/vnd.github.v3.diff; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Content-Length", strconv.Itoa(len(diff)))
	if _, err := w.Write(diff); err != nil {
		slog.Error("write diff", "error", err)
	}
}

type pullFileResponse struct {
	Filename         string  `json:"filename"`
	PreviousFilename *string `json:"previous_filename,omitempty"`
	Status           string  `json:"status"`
	Additions        int     `json:"additions"`
	Deletions        int     `json:"deletions"`
	Changes          int     `json:"changes"`
	BlobURL          string  `json:"blob_url"`
	RawURL           string  `json:"raw_url"`
}

func (h *Handler) pullFiles(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	page := positiveQueryInt(r, "page", 1)
	perPage := min(positiveQueryInt(r, "per_page", defaultPerPage), maxPerPage)
	files, total, err := h.gitea.PullFiles(r.Context(), r.Header.Get("Authorization"), owner, repo, number, page, perPage)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	out := make([]pullFileResponse, 0, len(files))
	for _, file := range files {
		status := file.Status
		switch status {
		case "deleted":
			status = "removed"
		case "changed":
			// Gitea's "changed" is GitHub's "modified". GitHub's "changed" means mode only.
			status = "modified"
		}
		out = append(out, pullFileResponse{
			Filename:         file.Filename,
			PreviousFilename: nonEmpty(file.PreviousFilename),
			Status:           status,
			Additions:        file.Additions,
			Deletions:        file.Deletions,
			Changes:          file.Changes,
			BlobURL:          file.HTMLURL,
			RawURL:           file.RawURL,
		})
	}
	h.writePullPage(w, owner, repo, number, "files", page, perPage, total, out)
}

func (h *Handler) writePullPage(w http.ResponseWriter, owner, repo string, number int64, resource string, page, perPage, total int, body any) {
	base := h.repoAPIURL(owner, repo) + "/pulls/" + strconv.FormatInt(number, 10) + "/" + resource
	if link := linkHeader(base, page, perPage, total); link != "" {
		w.Header().Set("Link", link)
	}
	writeJSON(w, http.StatusOK, body)
}

type gitPersonResponse struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date,omitempty"`
}

type gitCommitResponse struct {
	Message   string             `json:"message"`
	Author    *gitPersonResponse `json:"author"`
	Committer *gitPersonResponse `json:"committer"`
}

type pullCommitResponse struct {
	SHA       string             `json:"sha"`
	HTMLURL   string             `json:"html_url"`
	Commit    gitCommitResponse  `json:"commit"`
	Author    *issueUserResponse `json:"author"`
	Committer *issueUserResponse `json:"committer"`
}

func mapGitPerson(user *gitea.CommitUser) *gitPersonResponse {
	if user == nil {
		return nil
	}
	return &gitPersonResponse{Name: user.Name, Email: user.Email, Date: user.Date}
}

func (h *Handler) pullCommits(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	page := positiveQueryInt(r, "page", 1)
	perPage := min(positiveQueryInt(r, "per_page", defaultPerPage), maxPerPage)
	commits, total, err := h.gitea.PullCommits(r.Context(), r.Header.Get("Authorization"), owner, repo, number, page, perPage)
	if err != nil {
		writeUpstreamError(w, err)
		return
	}
	out := make([]pullCommitResponse, 0, len(commits))
	for i := range commits {
		commit := &commits[i]
		out = append(out, pullCommitResponse{
			SHA:     commit.SHA,
			HTMLURL: commit.HTMLURL,
			Commit: gitCommitResponse{
				Message:   commit.Commit.Message,
				Author:    mapGitPerson(commit.Commit.Author),
				Committer: mapGitPerson(commit.Commit.Committer),
			},
			Author:    mapIssueUser(commit.Author),
			Committer: mapIssueUser(commit.Committer),
		})
	}
	h.writePullPage(w, owner, repo, number, "commits", page, perPage, total, out)
}

type pullReviewResponse struct {
	ID          int64              `json:"id"`
	User        *issueUserResponse `json:"user"`
	Body        string             `json:"body"`
	State       string             `json:"state"`
	HTMLURL     string             `json:"html_url"`
	CommitID    string             `json:"commit_id"`
	SubmittedAt *time.Time         `json:"submitted_at,omitempty"`
}

// reviewState maps Gitea's review state. An empty result means the record is a
// pending review request, which GitHub does not list as a review.
func reviewState(review *gitea.Review) string {
	if review.Dismissed {
		return "DISMISSED"
	}
	switch review.State {
	case "APPROVED", "PENDING":
		return review.State
	case "COMMENT":
		return "COMMENTED"
	case "REQUEST_CHANGES":
		return "CHANGES_REQUESTED"
	}
	return ""
}

func (h *Handler) pullReviews(w http.ResponseWriter, r *http.Request) {
	owner, repo, ok := repoFromRequest(w, r)
	if !ok {
		return
	}
	number, ok := issueNumber(w, r)
	if !ok {
		return
	}
	reviews, err := h.gitea.PullReviews(r.Context(), r.Header.Get("Authorization"), owner, repo, number)
	if err != nil {
		if errors.Is(err, gitea.ErrTooManyReviews) {
			writeError(w, http.StatusUnprocessableEntity, "Listing is limited to "+strconv.Itoa(gitea.MaxListedReviews)+" reviews")
			return
		}
		writeUpstreamError(w, err)
		return
	}
	mapped := make([]pullReviewResponse, 0, len(reviews))
	for i := range reviews {
		review := &reviews[i]
		state := reviewState(review)
		if state == "" {
			continue
		}
		mapped = append(mapped, pullReviewResponse{
			ID:          review.ID,
			User:        mapIssueUser(review.User),
			Body:        review.Body,
			State:       state,
			HTMLURL:     review.HTMLURL,
			CommitID:    review.CommitID,
			SubmittedAt: review.SubmittedAt,
		})
	}
	page := positiveQueryInt(r, "page", 1)
	perPage := min(positiveQueryInt(r, "per_page", defaultPerPage), maxPerPage)
	h.writePullPage(w, owner, repo, number, "reviews", page, perPage, len(mapped), pageOf(mapped, page, perPage))
}

// pageOf returns one page of items. It compares by page count first, so a huge
// page number cannot overflow.
func pageOf[T any](items []T, page, perPage int) []T {
	if page > (len(items)+perPage-1)/perPage {
		return []T{}
	}
	start := (page - 1) * perPage
	return items[start:min(start+perPage, len(items))]
}
