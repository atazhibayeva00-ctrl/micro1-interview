// Package gitea is a minimal client for the Gitea API. It forwards the
// caller's Authorization header and holds no credentials of its own.
package gitea

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxResponseBytes = 4 << 20

// StatusError reports a non-2xx response from Gitea.
type StatusError struct {
	Code int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("gitea returned status %d", e.Code)
}

// User is the subset of Gitea's user object used by the gateway.
type User struct {
	ID          int64     `json:"id"`
	Login       string    `json:"login"`
	FullName    string    `json:"full_name"`
	Email       string    `json:"email"`
	AvatarURL   string    `json:"avatar_url"`
	HTMLURL     string    `json:"html_url"`
	IsAdmin     bool      `json:"is_admin"`
	Location    string    `json:"location"`
	Website     string    `json:"website"`
	Description string    `json:"description"`
	Followers   int       `json:"followers_count"`
	Following   int       `json:"following_count"`
	Created     time.Time `json:"created"`
}

// Client calls a single Gitea instance.
type Client struct {
	baseURL string
	http    *http.Client
	// stream has no overall timeout. Callers bound downloads with a context.
	stream *http.Client
}

// New returns a client for the Gitea instance at baseURL.
func New(baseURL string) *Client {
	// Never replay a caller's credentials to a redirect target.
	noRedirect := func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 15 * time.Second, CheckRedirect: noRedirect},
		stream:  &http.Client{CheckRedirect: noRedirect},
	}
}

// Healthy reports whether Gitea answers its health endpoint.
func (c *Client) Healthy(ctx context.Context) error {
	resp, err := c.do(ctx, "", "/api/healthz")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return checkStatus(resp)
}

// CurrentUser returns the user identified by the Authorization header.
func (c *Client) CurrentUser(ctx context.Context, authorization string) (*User, error) {
	resp, err := c.do(ctx, authorization, "/api/v1/user")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return nil, err
	}
	var user User
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxResponseBytes)).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode gitea user: %w", err)
	}
	return &user, nil
}

// CountOwnedRepos counts repositories owned by uid with the given visibility.
func (c *Client) CountOwnedRepos(ctx context.Context, authorization string, uid int64, private bool) (int, error) {
	query := url.Values{
		"uid":        {strconv.FormatInt(uid, 10)},
		"exclusive":  {"true"},
		"is_private": {strconv.FormatBool(private)},
		"limit":      {"1"},
	}
	resp, err := c.do(ctx, authorization, "/api/v1/repos/search?"+query.Encode())
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if err := checkStatus(resp); err != nil {
		return 0, err
	}
	count, err := strconv.Atoi(resp.Header.Get("X-Total-Count"))
	if err != nil {
		return 0, fmt.Errorf("parse gitea repository count: %w", err)
	}
	return count, nil
}

func (c *Client) do(ctx context.Context, authorization, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	return c.http.Do(req)
}

func checkStatus(resp *http.Response) error {
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return &StatusError{Code: resp.StatusCode}
	}
	return nil
}
