package rest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"micro1-interview/internal/gitea"
)

const (
	comparePath = "GET " + upstreamRepo + "/compare/main...feat/x"
	createPath  = "POST " + upstreamRepo + "/pulls"
	giteaPull   = `{"id": 9, "number": 3, "state": "open", "title": "WIP: Draft title", "draft": true,
		"html_url": "http://forge.test/o/r/pulls/3",
		"head": {"label": "feat/x", "ref": "feat/x", "sha": "` + testSHA + `",
			"repo": {"id": 1, "name": "r", "full_name": "o/r", "owner": {"login": "o"}}},
		"base": {"label": "main", "ref": "main", "sha": "` + testBlobSHA + `"},
		"created_at": "2026-01-02T03:04:05Z", "updated_at": "2026-01-02T03:04:05Z"}`
	hasCommits = `{"total_commits": 1, "commits": []}`
)

func TestCreatePullForwardsOnlyAllowedFields(t *testing.T) {
	gateway, fake := startScripted(t, map[string]scriptedReply{
		comparePath: {status: 200, body: hasCommits},
		createPath:  {status: 201, body: giteaPull},
	})
	body := `{"title":"Draft title","head":"feat/x","base":"main","body":"text","draft":true,
		"maintainer_can_modify":true,"labels":[1],"assignees":["x"],"milestone":2,"due_date":"2030-01-01T00:00:00Z"}`
	status, _, raw := sendRequest(t, http.MethodPost, gateway+gatewayRepo+"/pulls", testAuth, strings.NewReader(body), nil)
	if status != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", status, raw)
	}
	calls := fake.callsTo(http.MethodPost, upstreamRepo+"/pulls")
	if len(calls) != 1 {
		t.Fatalf("%d create requests reached Gitea, want 1", len(calls))
	}
	want := []string{"allow_maintainer_edit", "base", "body", "head", "title"}
	if keys := sortedKeys(t, calls[0].Body); !slices.Equal(keys, want) {
		t.Errorf("forwarded fields = %v, want %v", keys, want)
	}
	if sent := jsonMap(t, calls[0].Body); sent["title"] != "WIP: Draft title" {
		t.Errorf("forwarded title = %v", sent["title"])
	}
	got := jsonMap(t, raw)
	head, _ := got["head"].(map[string]any)
	if got["title"] != "Draft title" || got["draft"] != true || got["mergeable_state"] != "draft" || head["label"] != "o:feat/x" {
		t.Errorf("response = %s", raw)
	}
}

func TestCreatePullRefusesEmptyPull(t *testing.T) {
	gateway, fake := startScripted(t, map[string]scriptedReply{
		comparePath: {status: 200, body: `{"total_commits": 0, "commits": []}`},
		createPath:  {status: 201, body: giteaPull},
	})
	status, _, raw := sendRequest(t, http.MethodPost, gateway+gatewayRepo+"/pulls", testAuth,
		strings.NewReader(`{"title":"t","head":"feat/x","base":"main"}`), nil)
	if status != http.StatusUnprocessableEntity || !strings.Contains(string(raw), "No commits between") {
		t.Errorf("status = %d, body = %s", status, raw)
	}
	if n := len(fake.callsTo(http.MethodPost, upstreamRepo+"/pulls")); n != 0 {
		t.Errorf("%d create requests reached Gitea", n)
	}
}

func TestCreatePullDoesNotFollowRedirects(t *testing.T) {
	gateway, fake := startScripted(t, map[string]scriptedReply{
		comparePath: {status: 200, body: hasCommits},
		createPath:  {status: 302, location: "/elsewhere"},
	})
	status, _, raw := sendRequest(t, http.MethodPost, gateway+gatewayRepo+"/pulls", testAuth,
		strings.NewReader(`{"title":"t","head":"feat/x","base":"main"}`), nil)
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, body = %s", status, raw)
	}
	for _, call := range fake.recorded() {
		if call.Path == "/elsewhere" {
			t.Error("gateway followed a redirect")
		}
	}
}

func TestPullErrorsDoNotLeakGiteaText(t *testing.T) {
	reply := func(code int) scriptedReply {
		body, _ := json.Marshal(map[string]string{"message": secretDetail})
		return scriptedReply{status: code, body: string(body)}
	}
	for _, tc := range []struct {
		name       string
		routes     map[string]scriptedReply
		wantStatus int
	}{
		{"duplicate", map[string]scriptedReply{comparePath: {status: 200, body: hasCommits}, createPath: reply(409)}, 422},
		{"validation", map[string]scriptedReply{comparePath: {status: 200, body: hasCommits}, createPath: reply(422)}, 422},
		{"forbidden", map[string]scriptedReply{comparePath: {status: 200, body: hasCommits}, createPath: reply(403)}, 403},
		{"create failure", map[string]scriptedReply{comparePath: {status: 200, body: hasCommits}, createPath: reply(500)}, 502},
		{"compare failure", map[string]scriptedReply{comparePath: reply(500)}, 502},
		{"missing branch", map[string]scriptedReply{
			comparePath:           reply(404),
			"GET " + upstreamRepo: {status: 200, body: `{"id":1,"name":"r"}`},
		}, 422},
		{"missing repo", map[string]scriptedReply{comparePath: reply(404)}, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway, _ := startScripted(t, tc.routes)
			status, _, raw := sendRequest(t, http.MethodPost, gateway+gatewayRepo+"/pulls", testAuth,
				strings.NewReader(`{"title":"t","head":"feat/x","base":"main"}`), nil)
			if status != tc.wantStatus {
				t.Errorf("status = %d, want %d, body = %s", status, tc.wantStatus, raw)
			}
			if strings.Contains(string(raw), secretDetail) {
				t.Errorf("Gitea's error text reached the client: %s", raw)
			}
		})
	}
}

func TestPullBadInputIsRejectedBeforeGitea(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, auth, body string
		want                           int
	}{
		{"no credentials", http.MethodPost, "/pulls", "", `{"title":"t","head":"h","base":"main"}`, 401},
		{"body is not JSON", http.MethodPost, "/pulls", testAuth, `{`, 400},
		{"empty title", http.MethodPost, "/pulls", testAuth, `{"title":"  ","head":"h","base":"main"}`, 422},
		{"head has dot segment", http.MethodPost, "/pulls", testAuth, `{"title":"t","head":"a/../b","base":"main"}`, 422},
		{"head has two colons", http.MethodPost, "/pulls", testAuth, `{"title":"t","head":"a:b:c","base":"main"}`, 422},
		{"head owner is invalid", http.MethodPost, "/pulls", testAuth, `{"title":"t","head":"..:b","base":"main"}`, 422},
		{"base has dot segment", http.MethodPost, "/pulls", testAuth, `{"title":"t","head":"h","base":".."}`, 422},
		{"reviewers without credentials", http.MethodPost, "/pulls/1/requested_reviewers", "", `{"reviewers":["a"]}`, 401},
		{"reviewer name is invalid", http.MethodPost, "/pulls/1/requested_reviewers", testAuth, `{"reviewers":["../a"]}`, 422},
		{"pull number is not numeric", http.MethodGet, "/pulls/abc", testAuth, "", 404},
		{"status sha is malformed", http.MethodGet, "/commits/not-a-sha/status", testAuth, "", 404},
		{"check run sha is malformed", http.MethodGet, "/commits/not-a-sha/check-runs", testAuth, "", 404},
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
}

// Real Gitea cannot be held in its "still checking" state on demand.
func TestMergeabilityNeverClaimsConflict(t *testing.T) {
	for _, tc := range []struct {
		name          string
		pull          gitea.Pull
		wantMergeable *bool
		wantState     string
	}{
		{"mergeable", gitea.Pull{State: "open", Mergeable: true}, new(true), "clean"},
		{"false may mean still checking", gitea.Pull{State: "open"}, nil, "unknown"},
		{"draft", gitea.Pull{State: "open", Draft: true, Mergeable: true}, nil, "draft"},
		{"closed", gitea.Pull{State: "closed", Mergeable: true}, nil, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mergeable, state := mergeability(&tc.pull)
			if state != tc.wantState || (mergeable == nil) != (tc.wantMergeable == nil) ||
				(mergeable != nil && *mergeable != *tc.wantMergeable) {
				t.Errorf("got (%v, %s), want (%v, %s)", mergeable, state, tc.wantMergeable, tc.wantState)
			}
		})
	}
}

func TestPullDiff(t *testing.T) {
	const diffPath = "GET " + upstreamRepo + "/pulls/3.diff"
	accept := map[string]string{"Accept": "application/vnd.github.v3.diff"}

	t.Run("exact bytes with safe headers", func(t *testing.T) {
		const diff = "diff --git a/x b/x\n+<script>alert(1)</script>\n"
		gateway, _ := startScripted(t, map[string]scriptedReply{diffPath: {status: 200, body: diff}})
		status, header, raw := sendRequest(t, http.MethodGet, gateway+gatewayRepo+"/pulls/3", testAuth, nil, accept)
		if status != http.StatusOK || string(raw) != diff {
			t.Errorf("status = %d, body = %q", status, raw)
		}
		if header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(header.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("unsafe headers: %v", header)
		}
	})

	t.Run("oversized diff is refused", func(t *testing.T) {
		gateway, _ := startScripted(t, map[string]scriptedReply{
			diffPath: {status: 200, body: strings.Repeat("a", maxDiffBytes+1)},
		})
		status, _, raw := sendRequest(t, http.MethodGet, gateway+gatewayRepo+"/pulls/3", testAuth, nil, accept)
		if status != http.StatusNotAcceptable || len(raw) > 1024 {
			t.Errorf("status = %d, body length = %d", status, len(raw))
		}
	})
}
