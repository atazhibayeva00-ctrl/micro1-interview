package gitea

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const (
	// Gitea inlines blobs up to 10 MiB, which is about 14 MiB once base64 encoded.
	maxContentsBytes = 32 << 20
	treePageSize     = 1000
	// MaxTreeEntries matches GitHub's limit for a single tree response.
	MaxTreeEntries = 100000
)

// Repository is the subset of Gitea's repository object used by the gateway.
type Repository struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	Description   string `json:"description"`
	Private       bool   `json:"private"`
	Internal      bool   `json:"internal"`
	Fork          bool   `json:"fork"`
	Archived      bool   `json:"archived"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	} `json:"owner"`
}

// GitRef is a single Git reference.
type GitRef struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA  string `json:"sha"`
		Type string `json:"type"`
	} `json:"object"`
}

// Branch is the subset of Gitea's branch object used by the gateway.
type Branch struct {
	Name   string `json:"name"`
	Commit struct {
		ID string `json:"id"`
	} `json:"commit"`
	Protected bool `json:"protected"`
}

// Content is a file or directory entry from Gitea's contents API.
type Content struct {
	Name            string  `json:"name"`
	Path            string  `json:"path"`
	SHA             string  `json:"sha"`
	Type            string  `json:"type"`
	Size            int64   `json:"size"`
	Encoding        *string `json:"encoding"`
	Content         *string `json:"content"`
	Target          *string `json:"target"`
	HTMLURL         *string `json:"html_url"`
	SubmoduleGitURL *string `json:"submodule_git_url"`
	LastCommitSHA   *string `json:"last_commit_sha"`
}

// TreeEntry is one entry of a Git tree.
type TreeEntry struct {
	Path string `json:"path"`
	Mode string `json:"mode"`
	Type string `json:"type"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

// Tree is a Git tree, combined from all of Gitea's pages.
type Tree struct {
	SHA       string
	Entries   []TreeEntry
	Truncated bool
}

// EscapePath escapes each segment of a slash-separated path.
func EscapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func repoPath(owner, repo string) string {
	return "/api/v1/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo)
}

func (c *Client) getJSON(ctx context.Context, authorization, path string, limit int64, out any) (http.Header, error) {
	resp, err := c.do(ctx, authorization, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, limit)).Decode(out); err != nil {
		return nil, fmt.Errorf("decode gitea response: %w", err)
	}
	return resp.Header, nil
}

// Repository returns a repository the caller can read.
func (c *Client) Repository(ctx context.Context, authorization, owner, repo string) (*Repository, error) {
	var out Repository
	if _, err := c.getJSON(ctx, authorization, repoPath(owner, repo), maxResponseBytes, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Ref returns the reference named exactly ref, such as "heads/main". Gitea
// matches by prefix, so a near match is reported as not found.
func (c *Client) Ref(ctx context.Context, authorization, owner, repo, ref string) (*GitRef, error) {
	var raw json.RawMessage
	path := repoPath(owner, repo) + "/git/refs/" + EscapePath(ref)
	if _, err := c.getJSON(ctx, authorization, path, maxResponseBytes, &raw); err != nil {
		return nil, err
	}
	var refs []GitRef
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		if err := json.Unmarshal(raw, &refs); err != nil {
			return nil, fmt.Errorf("decode gitea refs: %w", err)
		}
	} else {
		var single GitRef
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, fmt.Errorf("decode gitea ref: %w", err)
		}
		refs = []GitRef{single}
	}
	for i := range refs {
		if refs[i].Ref == "refs/"+ref {
			return &refs[i], nil
		}
	}
	return nil, &StatusError{Code: http.StatusNotFound}
}

// Branches returns one page of branches and the total branch count.
func (c *Client) Branches(ctx context.Context, authorization, owner, repo string, page, perPage int) ([]Branch, int, error) {
	query := url.Values{"page": {strconv.Itoa(page)}, "limit": {strconv.Itoa(perPage)}}
	var branches []Branch
	header, err := c.getJSON(ctx, authorization, repoPath(owner, repo)+"/branches?"+query.Encode(), maxResponseBytes, &branches)
	if err != nil {
		return nil, 0, err
	}
	total, err := strconv.Atoi(header.Get("X-Total-Count"))
	if err != nil {
		return nil, 0, fmt.Errorf("parse gitea branch count: %w", err)
	}
	return branches, total, nil
}

// Contents returns a file, or the entries of a directory.
func (c *Client) Contents(ctx context.Context, authorization, owner, repo, path, ref string) (*Content, []Content, error) {
	target := repoPath(owner, repo) + "/contents"
	if path != "" {
		target += "/" + EscapePath(path)
	}
	if ref != "" {
		target += "?" + url.Values{"ref": {ref}}.Encode()
	}
	var raw json.RawMessage
	if _, err := c.getJSON(ctx, authorization, target, maxContentsBytes, &raw); err != nil {
		return nil, nil, err
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		entries := []Content{}
		if err := json.Unmarshal(raw, &entries); err != nil {
			return nil, nil, fmt.Errorf("decode gitea directory: %w", err)
		}
		return nil, entries, nil
	}
	var file Content
	if err := json.Unmarshal(raw, &file); err != nil {
		return nil, nil, fmt.Errorf("decode gitea file: %w", err)
	}
	return &file, nil, nil
}

// Tree returns a Git tree, reading every Gitea page up to MaxTreeEntries.
func (c *Client) Tree(ctx context.Context, authorization, owner, repo, sha string, recursive bool) (*Tree, error) {
	tree := &Tree{Entries: []TreeEntry{}}
	for page := 1; ; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "per_page": {strconv.Itoa(treePageSize)}}
		if recursive {
			query.Set("recursive", "true")
		}
		var body struct {
			SHA       string      `json:"sha"`
			Entries   []TreeEntry `json:"tree"`
			Truncated bool        `json:"truncated"`
		}
		path := repoPath(owner, repo) + "/git/trees/" + url.PathEscape(sha) + "?" + query.Encode()
		if _, err := c.getJSON(ctx, authorization, path, maxContentsBytes, &body); err != nil {
			return nil, err
		}
		tree.SHA = body.SHA
		tree.Entries = append(tree.Entries, body.Entries...)
		if len(tree.Entries) > MaxTreeEntries {
			tree.Entries = tree.Entries[:MaxTreeEntries]
			tree.Truncated = true
			return tree, nil
		}
		if !body.Truncated || len(body.Entries) == 0 {
			return tree, nil
		}
	}
}

// Raw opens a file at a commit. The caller must close the response body.
func (c *Client) Raw(ctx context.Context, authorization, owner, repo, sha, path string) (*http.Response, error) {
	target := c.baseURL + repoPath(owner, repo) + "/raw/" + EscapePath(path) + "?" + url.Values{"ref": {sha}}.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := c.stream.Do(req)
	if err != nil {
		return nil, err
	}
	if err := checkStatus(resp); err != nil {
		resp.Body.Close()
		return nil, err
	}
	return resp, nil
}
