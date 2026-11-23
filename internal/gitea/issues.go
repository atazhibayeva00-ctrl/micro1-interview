package gitea

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"time"
)

const (
	issuePageSize = 100
	// MaxListedIssues bounds how many issues one list request reads.
	MaxListedIssues = 10000
)

// ErrTooManyIssues reports a list that exceeds MaxListedIssues.
var ErrTooManyIssues = errors.New("repository has too many matching issues")

// Label is a Gitea issue label.
type Label struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

// IssueUser is the subset of a user embedded in issues and comments.
type IssueUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	AvatarURL string `json:"avatar_url"`
	HTMLURL   string `json:"html_url"`
	IsAdmin   bool   `json:"is_admin"`
}

// Issue is the subset of Gitea's issue object used by the gateway.
type Issue struct {
	ID          int64       `json:"id"`
	Number      int64       `json:"number"`
	Title       string      `json:"title"`
	Body        string      `json:"body"`
	State       string      `json:"state"`
	User        *IssueUser  `json:"user"`
	Labels      []Label     `json:"labels"`
	Assignees   []IssueUser `json:"assignees"`
	Comments    int         `json:"comments"`
	IsLocked    bool        `json:"is_locked"`
	HTMLURL     string      `json:"html_url"`
	Created     time.Time   `json:"created_at"`
	Updated     time.Time   `json:"updated_at"`
	Closed      *time.Time  `json:"closed_at"`
	PullRequest *struct {
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
}

// Comment is the subset of Gitea's issue comment used by the gateway.
type Comment struct {
	ID      int64      `json:"id"`
	Body    string     `json:"body"`
	User    *IssueUser `json:"user"`
	HTMLURL string     `json:"html_url"`
	Created time.Time  `json:"created_at"`
	Updated time.Time  `json:"updated_at"`
}

// Issue returns one issue. Gitea also returns pull requests by number.
func (c *Client) Issue(ctx context.Context, authorization, owner, repo string, number int64) (*Issue, error) {
	var out Issue
	path := repoPath(owner, repo) + "/issues/" + strconv.FormatInt(number, 10)
	if _, err := c.getJSON(ctx, authorization, path, maxContentsBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// IssueComments returns every comment on an issue, because Gitea does not page them.
func (c *Client) IssueComments(ctx context.Context, authorization, owner, repo string, number int64) ([]Comment, error) {
	comments := []Comment{}
	path := repoPath(owner, repo) + "/issues/" + strconv.FormatInt(number, 10) + "/comments"
	if _, err := c.getJSON(ctx, authorization, path, maxContentsBytes, &comments); err != nil {
		return nil, err
	}
	return comments, nil
}

// Issues returns all issues in a state ("open", "closed", or "all"), excluding
// pull requests, sorted newest first by issue number.
func (c *Client) Issues(ctx context.Context, authorization, owner, repo, state string) ([]Issue, error) {
	byNumber := map[int64]Issue{}
	for page := 1; ; page++ {
		query := url.Values{
			"type":  {"issues"},
			"state": {state},
			"page":  {strconv.Itoa(page)},
			"limit": {strconv.Itoa(issuePageSize)},
		}
		var issues []Issue
		header, err := c.getJSON(ctx, authorization, repoPath(owner, repo)+"/issues?"+query.Encode(), maxContentsBytes, &issues)
		if err != nil {
			return nil, err
		}
		total, err := strconv.Atoi(header.Get("X-Total-Count"))
		if err != nil {
			return nil, fmt.Errorf("parse gitea issue count: %w", err)
		}
		if total > MaxListedIssues {
			return nil, ErrTooManyIssues
		}
		for _, issue := range issues {
			byNumber[issue.Number] = issue
		}
		if len(issues) < issuePageSize || page*issuePageSize >= total {
			break
		}
	}
	out := make([]Issue, 0, len(byNumber))
	for _, issue := range byNumber {
		out = append(out, issue)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Number > out[j].Number })
	return out, nil
}
