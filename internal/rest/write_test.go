package rest

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"micro1-interview/internal/gitea"
)

const (
	testSHA      = "448507c0258d35e66c31bb6107512a00fd044afc"
	testBlobSHA  = "b6fc4c620b67d95f953a5c1c1230aaab5db5a1b0"
	upstreamRepo = "/api/v1/repos/o/r"
	gatewayRepo  = "/api/v3/repos/o/r"
	secretDetail = "secret-internal-detail"

	giteaFileResult = `{
		"content": {"name": "a.txt", "path": "dir/a.txt", "sha": "` + testBlobSHA + `", "type": "file", "size": 5,
			"encoding": "base64", "content": "aGVsbG8=", "html_url": "http://forge.test/o/r/src/branch/b1/dir/a.txt",
			"last_commit_sha": "c787616c6fbdd58a7a963a36bba88daa3589cfe5"},
		"commit": {"sha": "c787616c6fbdd58a7a963a36bba88daa3589cfe5", "html_url": "http://forge.test/o/r/commit/c787616",
			"message": "add a\n",
			"author": {"name": "Octo", "email": "octo@example.invalid", "date": "2026-11-23T20:42:30Z"},
			"committer": {"name": "Octo", "email": "octo@example.invalid", "date": "2026-11-23T20:42:30Z"}}
	}`
)

type upstreamCall struct {
	Method, Path, Authorization string
	Body                        []byte
}

type scriptedReply struct {
	status   int
	body     string
	location string
}

// scriptedGitea answers from a fixed table and records every request it gets.
type scriptedGitea struct {
	mu     sync.Mutex
	calls  []upstreamCall
	routes map[string]scriptedReply
}

func (s *scriptedGitea) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	s.mu.Lock()
	s.calls = append(s.calls, upstreamCall{r.Method, r.URL.Path, r.Header.Get("Authorization"), body})
	s.mu.Unlock()

	reply, ok := s.routes[r.Method+" "+r.URL.Path]
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if reply.location != "" {
		w.Header().Set("Location", reply.location)
	}
	w.WriteHeader(reply.status)
	io.WriteString(w, reply.body)
}

func (s *scriptedGitea) recorded() []upstreamCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

func (s *scriptedGitea) callsTo(method, path string) []upstreamCall {
	var matches []upstreamCall
	for _, call := range s.recorded() {
		if call.Method == method && call.Path == path {
			matches = append(matches, call)
		}
	}
	return matches
}

func startScripted(t *testing.T, routes map[string]scriptedReply) (string, *scriptedGitea) {
	t.Helper()
	fake := &scriptedGitea{routes: routes}
	upstream := httptest.NewServer(fake)
	t.Cleanup(upstream.Close)
	gateway := httptest.NewServer(NewHandler(gitea.New(upstream.URL), "http://gateway.test"))
	t.Cleanup(gateway.Close)
	return gateway.URL, fake
}

var noRedirects = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}}

func sendRequest(t *testing.T, method, url, authorization string, body io.Reader, headers map[string]string) (int, http.Header, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	resp, err := noRedirects.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, resp.Header, raw
}

func jsonMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("not a JSON object: %q", raw)
	}
	return out
}

func sortedKeys(t *testing.T, raw []byte) []string {
	t.Helper()
	keys := make([]string, 0)
	for key := range jsonMap(t, raw) {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func TestWritesForwardOnlyAllowedFields(t *testing.T) {
	t.Run("create ref", func(t *testing.T) {
		gateway, fake := startScripted(t, map[string]scriptedReply{
			"GET " + upstreamRepo + "/git/commits/" + testSHA: {status: 200, body: `{"sha":"` + testSHA + `"}`},
			"POST " + upstreamRepo + "/branches":              {status: 201, body: `{}`},
		})
		body := `{"ref":"refs/heads/feat/x","sha":"` + testSHA + `","force":true,"key":"ignored"}`
		status, _, raw := sendRequest(t, http.MethodPost, gateway+gatewayRepo+"/git/refs", testAuth, strings.NewReader(body), nil)
		if status != http.StatusCreated {
			t.Fatalf("status = %d, body = %s", status, raw)
		}
		got := jsonMap(t, raw)
		object, _ := got["object"].(map[string]any)
		if got["ref"] != "refs/heads/feat/x" || object["sha"] != testSHA || object["type"] != "commit" {
			t.Errorf("response = %s", raw)
		}

		calls := fake.callsTo(http.MethodPost, upstreamRepo+"/branches")
		if len(calls) != 1 {
			t.Fatalf("%d branch requests reached Gitea, want 1", len(calls))
		}
		if keys := sortedKeys(t, calls[0].Body); !slices.Equal(keys, []string{"new_branch_name", "old_ref_name"}) {
			t.Errorf("forwarded fields = %v", keys)
		}
		sent := jsonMap(t, calls[0].Body)
		if sent["new_branch_name"] != "feat/x" || sent["old_ref_name"] != testSHA || calls[0].Authorization != testAuth {
			t.Errorf("forwarded %v with auth %q", sent, calls[0].Authorization)
		}
	})

	t.Run("put contents", func(t *testing.T) {
		extras := `"force_push":true,"new_branch":"other","author":{"name":"x","email":"x@example.invalid"},` +
			`"committer":{"name":"y","email":"y@example.invalid"},"signoff":true,"from_path":"old.txt"`
		for _, tc := range []struct {
			name, fields string
			upstream     int
			wantStatus   int
			wantKeys     []string
		}{
			{"create", `"message":"add a","content":"aGVsbG8=","branch":"b1"`, 201, 201, []string{"branch", "content", "message"}},
			{"update", `"message":"upd","content":"aGVsbG8=","branch":"b1","sha":"` + testBlobSHA + `"`, 200, 200,
				[]string{"branch", "content", "message", "sha"}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				gateway, fake := startScripted(t, map[string]scriptedReply{
					"PUT " + upstreamRepo + "/contents/dir/a.txt": {status: tc.upstream, body: giteaFileResult},
				})
				status, _, raw := sendRequest(t, http.MethodPut, gateway+gatewayRepo+"/contents/dir/a.txt", testAuth,
					strings.NewReader("{"+tc.fields+","+extras+"}"), nil)
				if status != tc.wantStatus {
					t.Fatalf("status = %d, body = %s", status, raw)
				}
				calls := fake.callsTo(http.MethodPut, upstreamRepo+"/contents/dir/a.txt")
				if len(calls) != 1 {
					t.Fatalf("%d writes reached Gitea, want 1", len(calls))
				}
				if keys := sortedKeys(t, calls[0].Body); !slices.Equal(keys, tc.wantKeys) {
					t.Errorf("forwarded fields = %v, want %v", keys, tc.wantKeys)
				}
				if calls[0].Authorization != testAuth {
					t.Errorf("forwarded Authorization = %q", calls[0].Authorization)
				}

				// GitHub returns file metadata and the commit, never the file body.
				got := jsonMap(t, raw)
				content, _ := got["content"].(map[string]any)
				commit, _ := got["commit"].(map[string]any)
				if content["path"] != "dir/a.txt" || content["sha"] != testBlobSHA {
					t.Errorf("content = %v", content)
				}
				for _, key := range []string{"content", "encoding"} {
					if _, present := content[key]; present {
						t.Errorf("response content must not include %s", key)
					}
				}
				author, _ := commit["author"].(map[string]any)
				if commit["sha"] != "c787616c6fbdd58a7a963a36bba88daa3589cfe5" || commit["message"] != "add a\n" ||
					author["name"] != "Octo" || author["date"] != "2026-11-23T20:42:30Z" {
					t.Errorf("commit = %v", commit)
				}
			})
		}
	})
}

func TestWritesDoNotFollowRedirects(t *testing.T) {
	gateway, fake := startScripted(t, map[string]scriptedReply{
		"GET " + upstreamRepo + "/git/commits/" + testSHA: {status: 200, body: `{}`},
		"POST " + upstreamRepo + "/branches":              {status: 302, location: "/elsewhere"},
		"PUT " + upstreamRepo + "/contents/a.txt":         {status: 302, location: "/elsewhere"},
	})
	for _, tc := range []struct{ name, method, path, body string }{
		{"create ref", http.MethodPost, "/git/refs", `{"ref":"refs/heads/x","sha":"` + testSHA + `"}`},
		{"put contents", http.MethodPut, "/contents/a.txt", `{"message":"m","content":"eA=="}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, _, raw := sendRequest(t, tc.method, gateway+gatewayRepo+tc.path, testAuth, strings.NewReader(tc.body), nil)
			if status != http.StatusBadGateway {
				t.Errorf("status = %d, body = %s", status, raw)
			}
		})
	}
	for _, call := range fake.recorded() {
		if call.Path == "/elsewhere" {
			t.Errorf("gateway followed a redirect with %s %s", call.Method, call.Authorization)
		}
	}
	if n := len(fake.callsTo(http.MethodPut, upstreamRepo+"/contents/a.txt")); n != 1 {
		t.Errorf("write was sent %d times, want 1", n)
	}
}

func TestWriteErrorsDoNotLeakGiteaText(t *testing.T) {
	reply := func(status int, message string) scriptedReply {
		body, _ := json.Marshal(map[string]string{"message": message})
		return scriptedReply{status: status, body: string(body)}
	}
	for _, tc := range []struct {
		name, method, path, body string
		route                    string
		upstream                 scriptedReply
		wantStatus               int
	}{
		{"put internal error", http.MethodPut, "/contents/a.txt", `{"message":"m","content":"eA=="}`,
			"PUT " + upstreamRepo + "/contents/a.txt", reply(500, secretDetail), 502},
		{"put stale sha", http.MethodPut, "/contents/a.txt", `{"message":"m","content":"eA==","sha":"` + testBlobSHA + `"}`,
			"PUT " + upstreamRepo + "/contents/a.txt", reply(422, "sha does not match [given: x, expected: "+secretDetail+"]"), 409},
		{"put other validation", http.MethodPut, "/contents/a.txt", `{"message":"m","content":"eA=="}`,
			"PUT " + upstreamRepo + "/contents/a.txt", reply(422, secretDetail), 422},
		{"put locked", http.MethodPut, "/contents/a.txt", `{"message":"m","content":"eA=="}`,
			"PUT " + upstreamRepo + "/contents/a.txt", reply(423, secretDetail), 403},
		{"put forbidden", http.MethodPut, "/contents/a.txt", `{"message":"m","content":"eA=="}`,
			"PUT " + upstreamRepo + "/contents/a.txt", reply(403, secretDetail), 403},
		{"branch duplicate", http.MethodPost, "/git/refs", `{"ref":"refs/heads/x","sha":"` + testSHA + `"}`,
			"POST " + upstreamRepo + "/branches", reply(409, secretDetail), 422},
		{"branch internal error", http.MethodPost, "/git/refs", `{"ref":"refs/heads/x","sha":"` + testSHA + `"}`,
			"POST " + upstreamRepo + "/branches", reply(500, secretDetail), 502},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway, _ := startScripted(t, map[string]scriptedReply{
				"GET " + upstreamRepo + "/git/commits/" + testSHA: {status: 200, body: `{}`},
				tc.route: tc.upstream,
			})
			status, _, raw := sendRequest(t, tc.method, gateway+gatewayRepo+tc.path, testAuth, strings.NewReader(tc.body), nil)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d, body = %s", status, tc.wantStatus, raw)
			}
			if strings.Contains(string(raw), secretDetail) {
				t.Errorf("Gitea's error text reached the client: %s", raw)
			}
		})
	}
}

func TestBadInputIsRejectedBeforeGitea(t *testing.T) {
	const okFile = `"message":"m","content":"eA=="`
	for _, tc := range []struct {
		name, method, path, auth, body string
		want                           int
	}{
		{"ref is a tag", http.MethodPost, "/git/refs", testAuth, `{"ref":"refs/tags/v1","sha":"` + testSHA + `"}`, 422},
		{"ref has no prefix", http.MethodPost, "/git/refs", testAuth, `{"ref":"x","sha":"` + testSHA + `"}`, 422},
		{"ref has dot segment", http.MethodPost, "/git/refs", testAuth, `{"ref":"refs/heads/a/../b","sha":"` + testSHA + `"}`, 422},
		{"ref sha too short", http.MethodPost, "/git/refs", testAuth, `{"ref":"refs/heads/x","sha":"abc123"}`, 422},
		{"ref body is not JSON", http.MethodPost, "/git/refs", testAuth, `{`, 400},
		{"ref without credentials", http.MethodPost, "/git/refs", "", `{"ref":"refs/heads/x","sha":"` + testSHA + `"}`, 401},
		{"file without message", http.MethodPut, "/contents/a.txt", testAuth, `{"content":"eA=="}`, 422},
		{"file with empty message", http.MethodPut, "/contents/a.txt", testAuth, `{"message":"","content":"eA=="}`, 422},
		{"file without content", http.MethodPut, "/contents/a.txt", testAuth, `{"message":"m"}`, 422},
		{"file branch is dot dot", http.MethodPut, "/contents/a.txt", testAuth, `{` + okFile + `,"branch":".."}`, 422},
		{"file sha is malformed", http.MethodPut, "/contents/a.txt", testAuth, `{` + okFile + `,"sha":"nope"}`, 422},
		{"file body is not JSON", http.MethodPut, "/contents/a.txt", testAuth, `not json`, 400},
		{"file without credentials", http.MethodPut, "/contents/a.txt", "", `{` + okFile + `}`, 401},
		{"blob sha is malformed", http.MethodGet, "/git/blobs/not-a-sha", testAuth, "", 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway, fake := startScripted(t, nil)
			status, _, raw := sendRequest(t, tc.method, gateway+gatewayRepo+tc.path, tc.auth, strings.NewReader(tc.body), nil)
			if status != tc.want {
				t.Errorf("status = %d, want %d, body = %s", status, tc.want, raw)
			}
			if calls := fake.recorded(); len(calls) != 0 {
				t.Errorf("%d request(s) reached Gitea: %v", len(calls), calls)
			}
		})
	}

	// The mux or the gateway may stop an encoded dot segment, but never Gitea.
	t.Run("encoded dot segment in path", func(t *testing.T) {
		for _, path := range []string{"/contents/dir/%2e%2e/x.txt", "/contents/%2e%2e/x.txt", "/contents/dir/%2e/x.txt"} {
			gateway, fake := startScripted(t, nil)
			status, _, _ := sendRequest(t, http.MethodPut, gateway+gatewayRepo+path, testAuth, strings.NewReader(`{`+okFile+`}`), nil)
			if status >= 200 && status < 300 {
				t.Errorf("%s succeeded with %d", path, status)
			}
			if calls := fake.recorded(); len(calls) != 0 {
				t.Errorf("%s reached Gitea: %v", path, calls)
			}
		}
	})
}

func TestPutContentsBodyTooLarge(t *testing.T) {
	t.Run("declared size", func(t *testing.T) {
		fake := &scriptedGitea{}
		upstream := httptest.NewServer(fake)
		t.Cleanup(upstream.Close)
		handler := NewHandler(gitea.New(upstream.URL), "http://gateway.test")

		req := httptest.NewRequest(http.MethodPut, gatewayRepo+"/contents/big.bin", strings.NewReader(`{}`))
		req.ContentLength = maxWriteBodyBytes + 1
		req.Header.Set("Authorization", testAuth)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, req)

		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, body = %s", recorder.Code, recorder.Body)
		}
		if calls := fake.recorded(); len(calls) != 0 {
			t.Errorf("oversized write reached Gitea: %d request(s)", len(calls))
		}
	})

	// A chunked request declares no size, so the streaming limit must stop it.
	t.Run("undeclared size", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPut, "/", io.NopCloser(strings.NewReader(`{"content":"`+strings.Repeat("A", 64)+`"}`)))
		req.ContentLength = -1
		recorder := httptest.NewRecorder()
		var out putContentsRequest
		if decodeJSONBody(recorder, req, 16, &out) {
			t.Fatal("oversized body was accepted")
		}
		if recorder.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("status = %d, body = %s", recorder.Code, recorder.Body)
		}
	})
}

func TestBlobRawBytesAreExact(t *testing.T) {
	blobReply := func(data []byte, size int) scriptedReply {
		body, _ := json.Marshal(map[string]any{
			"sha": testBlobSHA, "size": size, "encoding": "base64",
			"content": base64.StdEncoding.EncodeToString(data),
		})
		return scriptedReply{status: 200, body: string(body)}
	}
	path := gatewayRepo + "/git/blobs/" + testBlobSHA
	upstream := "GET " + upstreamRepo + "/git/blobs/" + testBlobSHA

	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"binary", []byte("\x00\xff\xfeabc\n")},
		{"empty", []byte{}},
		{"text without newline", []byte("hello")},
	} {
		for _, accept := range []string{"application/vnd.github.raw", "application/vnd.github.v3.raw"} {
			t.Run(tc.name+" "+accept, func(t *testing.T) {
				gateway, _ := startScripted(t, map[string]scriptedReply{upstream: blobReply(tc.data, len(tc.data))})
				status, header, raw := sendRequest(t, http.MethodGet, gateway+path, testAuth, nil, map[string]string{"Accept": accept})
				if status != http.StatusOK || string(raw) != string(tc.data) {
					t.Errorf("status = %d, body = %q, want %q", status, raw, tc.data)
				}
				if header.Get("Content-Type") != "application/octet-stream" || header.Get("X-Content-Type-Options") != "nosniff" {
					t.Errorf("unsafe headers: %v", header)
				}
			})
		}
	}

	t.Run("json form", func(t *testing.T) {
		gateway, _ := startScripted(t, map[string]scriptedReply{upstream: blobReply([]byte("hello"), 5)})
		status, _, raw := sendRequest(t, http.MethodGet, gateway+path, testAuth, nil, nil)
		got := jsonMap(t, raw)
		if status != http.StatusOK || got["content"] != "aGVsbG8=" || got["encoding"] != "base64" || got["sha"] != testBlobSHA {
			t.Errorf("status = %d, body = %s", status, raw)
		}
	})

	t.Run("size mismatch is an error", func(t *testing.T) {
		gateway, _ := startScripted(t, map[string]scriptedReply{upstream: blobReply([]byte("hello"), 3)})
		status, _, _ := sendRequest(t, http.MethodGet, gateway+path, testAuth, nil, map[string]string{"Accept": "application/vnd.github.raw"})
		if status != http.StatusBadGateway {
			t.Errorf("status = %d", status)
		}
	})

	t.Run("unknown blob is not found", func(t *testing.T) {
		gateway, _ := startScripted(t, map[string]scriptedReply{upstream: {status: 400, body: `{"message":"object does not exist"}`}})
		status, _, _ := sendRequest(t, http.MethodGet, gateway+path, testAuth, nil, nil)
		if status != http.StatusNotFound {
			t.Errorf("status = %d", status)
		}
	})
}
