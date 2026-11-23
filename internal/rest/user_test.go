package rest

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"micro1-interview/internal/gitea"
)

const (
	testAuth = "Bearer test-token"
	fullUser = `{
		"id": 42, "login": "octo", "full_name": "Octo Cat", "email": "octo@example.invalid",
		"avatar_url": "http://forge.test/avatar", "html_url": "http://forge.test/octo",
		"is_admin": true, "location": "Oslo", "website": "https://example.invalid",
		"description": "Builder", "followers_count": 3, "following_count": 4,
		"created": "2026-01-02T03:04:05Z"
	}`
	sparseUser = `{"id": 7, "login": "sparse", "created": "2026-01-02T03:04:05Z"}`
)

// fakeGitea serves a user and repo counts, with optional status overrides.
type fakeGitea struct {
	userBody      string
	userStatus    int
	searchStatus  int
	publicCount   string
	privateCount  string
	userCalls     atomic.Int32
	receivedAuth  atomic.Value
	healthStatus  int
	redirectUsers bool
}

func (f *fakeGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/healthz":
		w.WriteHeader(orDefault(f.healthStatus, http.StatusOK))
	case "/api/v1/user":
		f.userCalls.Add(1)
		f.receivedAuth.Store(r.Header.Get("Authorization"))
		if f.redirectUsers {
			http.Redirect(w, r, "/redirected", http.StatusFound)
			return
		}
		w.WriteHeader(orDefault(f.userStatus, http.StatusOK))
		io.WriteString(w, f.userBody)
	case "/api/v1/repos/search":
		if r.URL.Query().Get("exclusive") != "true" || r.URL.Query().Get("uid") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		count := f.publicCount
		if r.URL.Query().Get("is_private") == "true" {
			count = f.privateCount
		}
		w.Header().Set("X-Total-Count", count)
		w.WriteHeader(orDefault(f.searchStatus, http.StatusOK))
		io.WriteString(w, `{"ok":true,"data":[]}`)
	case "/redirected":
		f.receivedAuth.Store("redirect-followed")
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func orDefault(value, fallback int) int {
	if value == 0 {
		return fallback
	}
	return value
}

func startGateway(t *testing.T, fake *fakeGitea) *httptest.Server {
	t.Helper()
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)
	gateway := httptest.NewServer(NewHandler(gitea.New(upstream.URL)))
	t.Cleanup(gateway.Close)
	return gateway
}

func get(t *testing.T, url, authorization string) (int, map[string]any, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("response is not JSON: %q", raw)
	}
	return resp.StatusCode, body, string(raw)
}

func TestCurrentUserMapsEveryField(t *testing.T) {
	fake := &fakeGitea{userBody: fullUser, publicCount: "5", privateCount: "2"}
	gateway := startGateway(t, fake)

	status, body, _ := get(t, gateway.URL+"/api/v3/user", testAuth)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	want := map[string]any{
		"login": "octo", "id": 42.0, "avatar_url": "http://forge.test/avatar",
		"html_url": "http://forge.test/octo", "type": "User", "site_admin": true,
		"name": "Octo Cat", "blog": "https://example.invalid", "location": "Oslo",
		"email": "octo@example.invalid", "bio": "Builder", "public_repos": 5.0,
		"followers": 3.0, "following": 4.0, "created_at": "2026-01-02T03:04:05Z",
		"total_private_repos": 2.0, "owned_private_repos": 2.0,
	}
	for key, value := range want {
		if body[key] != value {
			t.Errorf("%s = %v, want %v", key, body[key], value)
		}
	}
	if got := fake.receivedAuth.Load(); got != testAuth {
		t.Errorf("forwarded Authorization = %v", got)
	}
}

func TestCurrentUserEmptyFieldsAreNull(t *testing.T) {
	gateway := startGateway(t, &fakeGitea{userBody: sparseUser, publicCount: "0", privateCount: "0"})

	status, body, _ := get(t, gateway.URL+"/api/v3/user", testAuth)
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	for _, key := range []string{"name", "blog", "location", "email", "bio"} {
		value, present := body[key]
		if !present || value != nil {
			t.Errorf("%s = %v (present %v), want null", key, value, present)
		}
	}
	if body["public_repos"] != 0.0 || body["total_private_repos"] != 0.0 {
		t.Errorf("zero counts must stay present: %v", body)
	}
}

func TestCurrentUserMissingTokenSkipsGitea(t *testing.T) {
	fake := &fakeGitea{userBody: fullUser}
	gateway := startGateway(t, fake)

	status, body, _ := get(t, gateway.URL+"/api/v3/user", "")
	if status != http.StatusUnauthorized || body["message"] != "Requires authentication" || body["status"] != "401" {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	if fake.userCalls.Load() != 0 {
		t.Error("gateway called Gitea without credentials")
	}
}

func TestCurrentUserMapsUpstreamErrors(t *testing.T) {
	const secret = "internal-stack-trace"
	tests := []struct {
		upstream, want int
		message        string
	}{
		{http.StatusUnauthorized, http.StatusUnauthorized, "Bad credentials"},
		{http.StatusForbidden, http.StatusForbidden, "Forbidden"},
		{http.StatusNotFound, http.StatusNotFound, "Not Found"},
		{http.StatusInternalServerError, http.StatusBadGateway, "Bad Gateway"},
		{http.StatusServiceUnavailable, http.StatusBadGateway, "Bad Gateway"},
	}
	for _, test := range tests {
		t.Run(http.StatusText(test.upstream), func(t *testing.T) {
			gateway := startGateway(t, &fakeGitea{userBody: secret, userStatus: test.upstream})
			status, body, raw := get(t, gateway.URL+"/api/v3/user", testAuth)
			if status != test.want || body["message"] != test.message {
				t.Errorf("status = %d, body = %v", status, body)
			}
			if strings.Contains(raw, secret) {
				t.Error("upstream body leaked to client")
			}
		})
	}
}

func TestCurrentUserRepoCounts(t *testing.T) {
	t.Run("forbidden omits counts", func(t *testing.T) {
		gateway := startGateway(t, &fakeGitea{userBody: fullUser, searchStatus: http.StatusForbidden})
		status, body, _ := get(t, gateway.URL+"/api/v3/user", testAuth)
		if status != http.StatusOK || body["login"] != "octo" {
			t.Fatalf("status = %d, body = %v", status, body)
		}
		for _, key := range []string{"public_repos", "total_private_repos", "owned_private_repos"} {
			if _, present := body[key]; present {
				t.Errorf("%s must be omitted", key)
			}
		}
	})
	t.Run("server error fails", func(t *testing.T) {
		gateway := startGateway(t, &fakeGitea{userBody: fullUser, searchStatus: http.StatusInternalServerError})
		if status, _, _ := get(t, gateway.URL+"/api/v3/user", testAuth); status != http.StatusBadGateway {
			t.Errorf("status = %d", status)
		}
	})
	t.Run("malformed count fails", func(t *testing.T) {
		gateway := startGateway(t, &fakeGitea{userBody: fullUser, publicCount: "many", privateCount: "1"})
		if status, _, _ := get(t, gateway.URL+"/api/v3/user", testAuth); status != http.StatusBadGateway {
			t.Errorf("status = %d", status)
		}
	})
}

func TestCurrentUserMalformedUpstreamJSON(t *testing.T) {
	gateway := startGateway(t, &fakeGitea{userBody: "not-json"})
	status, body, raw := get(t, gateway.URL+"/api/v3/user", testAuth)
	if status != http.StatusBadGateway || body["message"] != "Bad Gateway" || strings.Contains(raw, "not-json") {
		t.Errorf("status = %d, body = %s", status, raw)
	}
}

func TestCurrentUserDoesNotFollowRedirects(t *testing.T) {
	fake := &fakeGitea{redirectUsers: true}
	gateway := startGateway(t, fake)
	if status, _, _ := get(t, gateway.URL+"/api/v3/user", testAuth); status != http.StatusBadGateway {
		t.Errorf("status = %d", status)
	}
	if fake.receivedAuth.Load() == "redirect-followed" {
		t.Error("gateway followed an upstream redirect")
	}
}

func TestGiteaUnreachable(t *testing.T) {
	upstream := httptest.NewServer(http.NotFoundHandler())
	address := upstream.URL
	upstream.Close()
	gateway := httptest.NewServer(NewHandler(gitea.New(address)))
	t.Cleanup(gateway.Close)

	for _, path := range []string{"/api/v3/user", "/healthz"} {
		status, body, raw := get(t, gateway.URL+path, testAuth)
		if status != http.StatusBadGateway || body["message"] != "Bad Gateway" {
			t.Errorf("%s: status = %d, body = %v", path, status, body)
		}
		if strings.Contains(raw, strings.TrimPrefix(address, "http://")) {
			t.Errorf("%s leaked the upstream address: %s", path, raw)
		}
	}
}

func TestHealth(t *testing.T) {
	gateway := startGateway(t, &fakeGitea{})
	if status, body, _ := get(t, gateway.URL+"/healthz", ""); status != http.StatusOK || body["status"] != "ok" {
		t.Errorf("healthy: status = %d, body = %v", status, body)
	}
	unhealthy := startGateway(t, &fakeGitea{healthStatus: http.StatusServiceUnavailable})
	if status, _, _ := get(t, unhealthy.URL+"/healthz", ""); status != http.StatusBadGateway {
		t.Errorf("unhealthy: status = %d", status)
	}
}

func TestUnknownRouteIsJSON404(t *testing.T) {
	gateway := startGateway(t, &fakeGitea{})
	status, body, _ := get(t, gateway.URL+"/api/v3/not-a-route", testAuth)
	if status != http.StatusNotFound || body["message"] != "Not Found" {
		t.Errorf("status = %d, body = %v", status, body)
	}
}
