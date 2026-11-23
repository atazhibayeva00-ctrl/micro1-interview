package ui_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"micro1-interview/internal/gitea"
	"micro1-interview/internal/rest"
)

// newServer serves the full gateway so the tests cover how the UI is mounted.
func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(upstream.Close)
	srv := httptest.NewServer(rest.NewHandler(gitea.New(upstream.URL)))
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url string) (*http.Response, string) {
	t.Helper()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err := client.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp, string(body)
}

func TestIndexIsServedWithSecurityHeaders(t *testing.T) {
	srv := newServer(t)
	resp, body := get(t, srv.URL+"/ui/")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("content type = %q, want text/html", resp.Header.Get("Content-Type"))
	}
	if !strings.Contains(body, `src="app.js"`) {
		t.Error("index.html does not load app.js")
	}
	csp := resp.Header.Get("Content-Security-Policy")
	for _, want := range []string{"default-src 'self'", "script-src 'self'", "connect-src 'self'", "frame-ancestors 'none'"} {
		if !strings.Contains(csp, want) {
			t.Errorf("CSP %q is missing %q", csp, want)
		}
	}
	if strings.Contains(csp, "unsafe-inline") || strings.Contains(csp, "unsafe-eval") {
		t.Errorf("CSP %q must not allow inline or eval scripts", csp)
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestStaticAssetsAreServed(t *testing.T) {
	srv := newServer(t)
	for path, wantType := range map[string]string{
		"/ui/app.js":    "javascript",
		"/ui/style.css": "text/css",
	} {
		resp, body := get(t, srv.URL+path)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("%s status = %d, want 200", path, resp.StatusCode)
		}
		if !strings.Contains(resp.Header.Get("Content-Type"), wantType) {
			t.Errorf("%s content type = %q, want %q", path, resp.Header.Get("Content-Type"), wantType)
		}
		if body == "" {
			t.Errorf("%s is empty", path)
		}
		if resp.Header.Get("Content-Security-Policy") == "" {
			t.Errorf("%s has no CSP header", path)
		}
	}
}

func TestUIPathRedirectsToTrailingSlash(t *testing.T) {
	srv := newServer(t)
	resp, _ := get(t, srv.URL+"/ui")
	if resp.StatusCode != http.StatusTemporaryRedirect || resp.Header.Get("Location") != "/ui/" {
		t.Errorf("GET /ui = %d -> %q, want 307 -> /ui/", resp.StatusCode, resp.Header.Get("Location"))
	}
}

func TestUnknownUIAssetIsNotFound(t *testing.T) {
	srv := newServer(t)
	if resp, _ := get(t, srv.URL+"/ui/missing.js"); resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
}

func TestUIDoesNotChangeAPIRoutes(t *testing.T) {
	srv := newServer(t)

	resp, body := get(t, srv.URL+"/healthz")
	if resp.StatusCode != http.StatusOK || !strings.Contains(body, `"ok"`) {
		t.Errorf("/healthz = %d %q, want 200 ok", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Security-Policy") != "" {
		t.Error("API responses must not carry the UI CSP")
	}

	resp, body = get(t, srv.URL+"/api/v3/user")
	if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(body, "Requires authentication") {
		t.Errorf("/api/v3/user without auth = %d %q, want 401", resp.StatusCode, body)
	}

	resp, body = get(t, srv.URL+"/nope")
	if resp.StatusCode != http.StatusNotFound || !strings.Contains(body, "Not Found") {
		t.Errorf("/nope = %d %q, want JSON 404", resp.StatusCode, body)
	}
}
