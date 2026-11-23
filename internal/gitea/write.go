package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

// CommitExists returns nil when sha names a commit the caller can read.
func (c *Client) CommitExists(ctx context.Context, authorization, owner, repo, sha string) error {
	var out struct {
		SHA string `json:"sha"`
	}
	path := repoPath(owner, repo) + "/git/commits/" + url.PathEscape(sha)
	_, err := c.getJSON(ctx, authorization, path, maxResponseBytes, &out)
	return err
}

// CreateBranch creates a branch that starts at the commit fromSHA.
func (c *Client) CreateBranch(ctx context.Context, authorization, owner, repo, name, fromSHA string) error {
	body, err := json.Marshal(map[string]string{"new_branch_name": name, "old_ref_name": fromSHA})
	if err != nil {
		return err
	}
	resp, err := c.send(ctx, c.write, http.MethodPost, authorization, repoPath(owner, repo)+"/branches", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp)
}

// FileWrite holds the only fields the gateway forwards when writing a file.
// Gitea options such as force_push and new_branch are deliberately absent.
type FileWrite struct {
	Message string `json:"message"`
	// Content is base64 encoded.
	Content string `json:"content"`
	Branch  string `json:"branch,omitempty"`
	// SHA is the blob SHA of the file being replaced, and is empty for new files.
	SHA string `json:"sha,omitempty"`
}

// CommitUser is the author or committer of a commit.
type CommitUser struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	Date  string `json:"date"`
}

// FileCommit is the commit created by a file write.
type FileCommit struct {
	SHA       string      `json:"sha"`
	HTMLURL   string      `json:"html_url"`
	Message   string      `json:"message"`
	Author    *CommitUser `json:"author"`
	Committer *CommitUser `json:"committer"`
}

// FileResult is Gitea's response to a file write.
type FileResult struct {
	Content *Content    `json:"content"`
	Commit  *FileCommit `json:"commit"`
}

// PutFile creates or updates a file and reports whether it was created.
func (c *Client) PutFile(ctx context.Context, authorization, owner, repo, path string, file FileWrite) (*FileResult, bool, error) {
	body, err := json.Marshal(file)
	if err != nil {
		return nil, false, err
	}
	resp, err := c.send(ctx, c.write, http.MethodPut, authorization, repoPath(owner, repo)+"/contents/"+EscapePath(path), body)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, false, err
	}
	var out FileResult
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&out); err != nil {
		return nil, false, fmt.Errorf("decode gitea file write: %w", err)
	}
	return &out, resp.StatusCode == http.StatusCreated, nil
}

// Blob is a Git blob. Content is base64 encoded.
type Blob struct {
	SHA      string `json:"sha"`
	Size     int64  `json:"size"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

// Blob returns a Git blob by SHA.
func (c *Client) Blob(ctx context.Context, authorization, owner, repo, sha string) (*Blob, error) {
	var out Blob
	path := repoPath(owner, repo) + "/git/blobs/" + url.PathEscape(sha)
	if _, err := c.getJSON(ctx, authorization, path, maxContentsBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
