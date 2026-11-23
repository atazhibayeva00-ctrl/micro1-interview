package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	listPageSize = 100
	// MaxListedReviews bounds how many reviews one request reads.
	MaxListedReviews = 1000
	// MaxListedStatuses bounds how many commit statuses one request reads.
	MaxListedStatuses = 1000
)

var (
	// ErrTooManyReviews reports a pull request above MaxListedReviews.
	ErrTooManyReviews = errors.New("pull request has too many reviews")
	// ErrTooManyStatuses reports a commit above MaxListedStatuses.
	ErrTooManyStatuses = errors.New("commit has too many statuses")
	// ErrDiffTooLarge reports a diff above the caller's size limit.
	ErrDiffTooLarge = errors.New("diff is too large")
)

// PRBranch is the head or base of a pull request.
type PRBranch struct {
	Label string      `json:"label"`
	Ref   string      `json:"ref"`
	SHA   string      `json:"sha"`
	Repo  *Repository `json:"repo"`
}

// Pull is the subset of Gitea's pull request used by the gateway.
type Pull struct {
	ID                  int64       `json:"id"`
	Number              int64       `json:"number"`
	State               string      `json:"state"`
	Title               string      `json:"title"`
	Body                string      `json:"body"`
	User                *IssueUser  `json:"user"`
	Labels              []Label     `json:"labels"`
	Assignees           []IssueUser `json:"assignees"`
	RequestedReviewers  []IssueUser `json:"requested_reviewers"`
	Head                PRBranch    `json:"head"`
	Base                PRBranch    `json:"base"`
	Draft               bool        `json:"draft"`
	Merged              bool        `json:"merged"`
	Mergeable           bool        `json:"mergeable"`
	MergedBy            *IssueUser  `json:"merged_by"`
	MergeCommitSHA      *string     `json:"merge_commit_sha"`
	Additions           int         `json:"additions"`
	Deletions           int         `json:"deletions"`
	ChangedFiles        int         `json:"changed_files"`
	AllowMaintainerEdit bool        `json:"allow_maintainer_edit"`
	IsLocked            bool        `json:"is_locked"`
	HTMLURL             string      `json:"html_url"`
	Created             time.Time   `json:"created_at"`
	Updated             time.Time   `json:"updated_at"`
	Closed              *time.Time  `json:"closed_at"`
	MergedAt            *time.Time  `json:"merged_at"`
	Milestone           *struct {
		Title string `json:"title"`
	} `json:"milestone"`
}

// PullCreate holds the only fields the gateway forwards when creating a pull
// request. Gitea options such as labels and due_date are deliberately absent.
type PullCreate struct {
	Title               string `json:"title"`
	Head                string `json:"head"`
	Base                string `json:"base"`
	Body                string `json:"body,omitempty"`
	AllowMaintainerEdit *bool  `json:"allow_maintainer_edit,omitempty"`
}

// ChangedFile is one file changed by a pull request.
type ChangedFile struct {
	Filename         string `json:"filename"`
	PreviousFilename string `json:"previous_filename"`
	Status           string `json:"status"`
	Additions        int    `json:"additions"`
	Deletions        int    `json:"deletions"`
	Changes          int    `json:"changes"`
	HTMLURL          string `json:"html_url"`
	RawURL           string `json:"raw_url"`
}

// PullCommit is one commit in a pull request.
type PullCommit struct {
	SHA     string `json:"sha"`
	HTMLURL string `json:"html_url"`
	Commit  struct {
		Message   string      `json:"message"`
		Author    *CommitUser `json:"author"`
		Committer *CommitUser `json:"committer"`
	} `json:"commit"`
	Author    *IssueUser `json:"author"`
	Committer *IssueUser `json:"committer"`
}

// Review is a pull request review.
type Review struct {
	ID          int64      `json:"id"`
	User        *IssueUser `json:"user"`
	State       string     `json:"state"`
	Body        string     `json:"body"`
	CommitID    string     `json:"commit_id"`
	Stale       bool       `json:"stale"`
	Dismissed   bool       `json:"dismissed"`
	SubmittedAt *time.Time `json:"submitted_at"`
	HTMLURL     string     `json:"html_url"`
}

// ReviewComment is a line comment in a review.
type ReviewComment struct {
	ID               int64      `json:"id"`
	ReviewID         int64      `json:"pull_request_review_id"`
	Body             string     `json:"body"`
	User             *IssueUser `json:"user"`
	Resolver         *IssueUser `json:"resolver"`
	Path             string     `json:"path"`
	Position         int32      `json:"position"`
	OriginalPosition int32      `json:"original_position"`
	HTMLURL          string     `json:"html_url"`
	Created          time.Time  `json:"created_at"`
	Updated          time.Time  `json:"updated_at"`
}

// CommitStatus is one status reported on a commit.
type CommitStatus struct {
	ID          int64     `json:"id"`
	Status      string    `json:"status"`
	Context     string    `json:"context"`
	Description string    `json:"description"`
	TargetURL   string    `json:"target_url"`
	Created     time.Time `json:"created_at"`
	Updated     time.Time `json:"updated_at"`
}

// CombinedStatus is the overall status of a commit.
type CombinedStatus struct {
	State    string         `json:"state"`
	SHA      string         `json:"sha"`
	Statuses []CommitStatus `json:"statuses"`
}

func pullPath(owner, repo string, number int64) string {
	return repoPath(owner, repo) + "/pulls/" + strconv.FormatInt(number, 10)
}

// CreatePull opens a pull request.
func (c *Client) CreatePull(ctx context.Context, authorization, owner, repo string, create PullCreate) (*Pull, error) {
	body, err := json.Marshal(create)
	if err != nil {
		return nil, err
	}
	resp, err := c.send(ctx, c.write, http.MethodPost, authorization, repoPath(owner, repo)+"/pulls", body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var out Pull
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxContentsBytes)).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode gitea pull request: %w", err)
	}
	return &out, nil
}

// Pull returns one pull request.
func (c *Client) Pull(ctx context.Context, authorization, owner, repo string, number int64) (*Pull, error) {
	var out Pull
	if _, err := c.getJSON(ctx, authorization, pullPath(owner, repo, number), maxContentsBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// CompareCommitCount returns how many commits head has that base lacks. It
// stops reading at the count, so a large comparison stays cheap to parse.
func (c *Client) CompareCommitCount(ctx context.Context, authorization, owner, repo, base, head string) (int, error) {
	resp, err := c.do(ctx, authorization, repoPath(owner, repo)+"/compare/"+EscapePath(base+"..."+head))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return 0, err
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, maxContentsBytes))
	if _, err := decoder.Token(); err != nil {
		return 0, fmt.Errorf("decode gitea comparison: %w", err)
	}
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return 0, fmt.Errorf("decode gitea comparison: %w", err)
		}
		if key == "total_commits" {
			var count int
			if err := decoder.Decode(&count); err != nil {
				return 0, fmt.Errorf("decode gitea comparison: %w", err)
			}
			return count, nil
		}
		var skipped json.RawMessage
		if err := decoder.Decode(&skipped); err != nil {
			return 0, fmt.Errorf("decode gitea comparison: %w", err)
		}
	}
	return 0, errors.New("gitea comparison has no commit count")
}

// RequestReviewers asks users and teams to review a pull request.
func (c *Client) RequestReviewers(ctx context.Context, authorization, owner, repo string, number int64, reviewers, teams []string) error {
	body, err := json.Marshal(map[string][]string{"reviewers": reviewers, "team_reviewers": teams})
	if err != nil {
		return err
	}
	resp, err := c.send(ctx, c.write, http.MethodPost, authorization, pullPath(owner, repo, number)+"/requested_reviewers", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp)
}

// PullDiff returns a pull request's diff, up to limit bytes.
func (c *Client) PullDiff(ctx context.Context, authorization, owner, repo string, number, limit int64) ([]byte, error) {
	resp, err := c.send(ctx, c.stream, http.MethodGet, authorization, pullPath(owner, repo, number)+".diff", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	diff, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(diff)) > limit {
		return nil, ErrDiffTooLarge
	}
	return diff, nil
}

func totalCount(header http.Header) (int, error) {
	total, err := strconv.Atoi(header.Get("X-Total-Count"))
	if err != nil {
		return 0, fmt.Errorf("parse gitea total count: %w", err)
	}
	return total, nil
}

// PullFiles returns one page of changed files and the total count.
func (c *Client) PullFiles(ctx context.Context, authorization, owner, repo string, number int64, page, perPage int) ([]ChangedFile, int, error) {
	query := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(perPage)}}
	files := []ChangedFile{}
	header, err := c.getJSON(ctx, authorization, pullPath(owner, repo, number)+"/files?"+query.Encode(), maxContentsBytes, &files)
	if err != nil {
		return nil, 0, err
	}
	total, err := totalCount(header)
	if err != nil {
		return nil, 0, err
	}
	return files, total, nil
}

// PullCommits returns one page of commits and the total count.
func (c *Client) PullCommits(ctx context.Context, authorization, owner, repo string, number int64, page, perPage int) ([]PullCommit, int, error) {
	query := url.Values{
		"page":         {strconv.Itoa(page)},
		"limit":        {strconv.Itoa(perPage)},
		"files":        {"false"},
		"verification": {"false"},
	}
	commits := []PullCommit{}
	header, err := c.getJSON(ctx, authorization, pullPath(owner, repo, number)+"/commits?"+query.Encode(), maxContentsBytes, &commits)
	if err != nil {
		return nil, 0, err
	}
	total, err := totalCount(header)
	if err != nil {
		return nil, 0, err
	}
	return commits, total, nil
}

// PullReviews returns every review on a pull request.
func (c *Client) PullReviews(ctx context.Context, authorization, owner, repo string, number int64) ([]Review, error) {
	out := []Review{}
	for page := 1; ; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(listPageSize)}}
		var reviews []Review
		header, err := c.getJSON(ctx, authorization, pullPath(owner, repo, number)+"/reviews?"+query.Encode(), maxContentsBytes, &reviews)
		if err != nil {
			return nil, err
		}
		total, err := totalCount(header)
		if err != nil {
			return nil, err
		}
		if total > MaxListedReviews {
			return nil, ErrTooManyReviews
		}
		out = append(out, reviews...)
		if len(reviews) == 0 || len(out) >= total {
			return out, nil
		}
	}
}

// ReviewComments returns the line comments of one review.
func (c *Client) ReviewComments(ctx context.Context, authorization, owner, repo string, number, reviewID int64) ([]ReviewComment, error) {
	comments := []ReviewComment{}
	path := pullPath(owner, repo, number) + "/reviews/" + strconv.FormatInt(reviewID, 10) + "/comments"
	if _, err := c.getJSON(ctx, authorization, path, maxContentsBytes, &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// CombinedCommitStatus returns a commit's overall state and all of its statuses.
func (c *Client) CombinedCommitStatus(ctx context.Context, authorization, owner, repo, sha string) (*CombinedStatus, error) {
	out := &CombinedStatus{Statuses: []CommitStatus{}}
	for page := 1; ; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(listPageSize)}}
		var body struct {
			State      string         `json:"state"`
			SHA        string         `json:"sha"`
			TotalCount int            `json:"total_count"`
			Statuses   []CommitStatus `json:"statuses"`
		}
		path := repoPath(owner, repo) + "/commits/" + url.PathEscape(sha) + "/status?" + query.Encode()
		if _, err := c.getJSON(ctx, authorization, path, maxContentsBytes, &body); err != nil {
			return nil, err
		}
		if body.TotalCount > MaxListedStatuses {
			return nil, ErrTooManyStatuses
		}
		out.State, out.SHA = body.State, body.SHA
		out.Statuses = append(out.Statuses, body.Statuses...)
		if len(body.Statuses) == 0 || len(out.Statuses) >= body.TotalCount {
			return out, nil
		}
	}
}
