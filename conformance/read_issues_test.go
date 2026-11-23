//go:build conformance

package conformance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const pullRequestNumber = 4

func issueArgs(repo, method string, number int) map[string]any {
	return map[string]any{"owner": fixtureOwner, "repo": repo, "method": method, "issue_number": number}
}

type listedIssues struct {
	Issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		State  string `json:"state"`
	} `json:"issues"`
	TotalCount int `json:"totalCount"`
	PageInfo   struct {
		HasNextPage     bool   `json:"hasNextPage"`
		HasPreviousPage bool   `json:"hasPreviousPage"`
		EndCursor       string `json:"endCursor"`
	} `json:"pageInfo"`
}

func (l listedIssues) numbers() []int {
	numbers := make([]int, 0, len(l.Issues))
	for _, issue := range l.Issues {
		numbers = append(numbers, issue.Number)
	}
	return numbers
}

func listIssues(t *testing.T, ctx context.Context, session *mcp.ClientSession, extra map[string]any) listedIssues {
	t.Helper()
	args := map[string]any{"owner": fixtureOwner, "repo": publicRepo}
	for key, value := range extra {
		args[key] = value
	}
	result, text := callTool(t, ctx, session, "list_issues", args)
	if result.IsError {
		t.Fatalf("list_issues(%v) failed: %s", extra, text)
	}
	var out listedIssues
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return out
}

func TestIssueReadGet(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	result, text := callTool(t, ctx, session, "issue_read", issueArgs(publicRepo, "get", 1))
	if result.IsError {
		t.Fatalf("issue_read failed: %s", text)
	}
	var wrapper struct {
		Issue struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			Body   string `json:"body"`
			State  string `json:"state"`
			User   struct {
				Login string `json:"login"`
			} `json:"user"`
			Comments int    `json:"comments"`
			HTMLURL  string `json:"html_url"`
		} `json:"issue"`
	}
	if err := json.Unmarshal([]byte(text), &wrapper); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	got := wrapper.Issue
	var want struct {
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
	}
	giteaGet(t, ctx, env["FORGE_TEST_TOKEN"], "/repos/"+fixtureOwner+"/"+publicRepo+"/issues/1", &want)
	if got.Number != 1 || got.Title != want.Title || got.Body != want.Body || got.HTMLURL != want.HTMLURL {
		t.Errorf("issue = %+v, Gitea = %+v", got, want)
	}
	if got.State != "open" || got.User.Login != fixtureOwner || got.Comments != 3 {
		t.Errorf("issue details = %+v", got)
	}
	for _, label := range []string{"bug", "enhancement"} {
		if !strings.Contains(text, label) {
			t.Errorf("label %s missing: %s", label, text)
		}
	}

	result, text = callTool(t, ctx, session, "issue_read", issueArgs(publicRepo, "get", 2))
	if result.IsError || !strings.Contains(text, `"state":"closed"`) {
		t.Errorf("closed issue: IsError = %v, text = %s", result.IsError, text)
	}
}

func TestIssueReadComments(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	read := func(page, perPage int) []string {
		t.Helper()
		args := issueArgs(publicRepo, "get_comments", 1)
		args["page"], args["perPage"] = page, perPage
		result, text := callTool(t, ctx, session, "issue_read", args)
		if result.IsError {
			t.Fatalf("get_comments failed: %s", text)
		}
		var wrapper struct {
			Comments []struct {
				Body string `json:"body"`
				User struct {
					Login string `json:"login"`
				} `json:"user"`
			} `json:"comments"`
		}
		if err := json.Unmarshal([]byte(text), &wrapper); err != nil {
			t.Fatalf("decode %q: %v", text, err)
		}
		bodies := make([]string, 0, len(wrapper.Comments))
		for _, comment := range wrapper.Comments {
			if comment.User.Login != fixtureOwner {
				t.Errorf("comment author = %q", comment.User.Login)
			}
			bodies = append(bodies, comment.Body)
		}
		return bodies
	}

	if got, want := read(1, 100), []string{"Comment one", "Comment two", "Comment three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("all comments = %v, want %v", got, want)
	}
	if got, want := read(2, 2), []string{"Comment three"}; !reflect.DeepEqual(got, want) {
		t.Errorf("page 2 = %v, want %v", got, want)
	}
	if got := read(3, 2); len(got) != 0 {
		t.Errorf("page past the end = %v", got)
	}
}

func TestIssueReadLabels(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	result, text := callTool(t, ctx, session, "issue_read", issueArgs(publicRepo, "get_labels", 1))
	if result.IsError {
		t.Fatalf("get_labels failed: %s", text)
	}
	var got struct {
		Labels []struct {
			ID          string `json:"id"`
			Name        string `json:"name"`
			Color       string `json:"color"`
			Description string `json:"description"`
		} `json:"labels"`
		TotalCount int `json:"totalCount"`
	}
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if got.TotalCount != 2 || len(got.Labels) != 2 {
		t.Fatalf("labels = %+v", got)
	}
	colors := map[string]string{}
	for _, label := range got.Labels {
		if label.ID == "" || label.Description == "" {
			t.Errorf("incomplete label: %+v", label)
		}
		colors[label.Name] = label.Color
	}
	if want := map[string]string{"bug": "d73a4a", "enhancement": "a2eeef"}; !reflect.DeepEqual(colors, want) {
		t.Errorf("label colors = %v, want %v", colors, want)
	}
}

func TestListIssuesStates(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  []int
	}{
		{"both", nil, []int{6, 5, 3, 2, 1}},
		{"open", map[string]any{"state": "OPEN"}, []int{6, 5, 3, 1}},
		{"closed", map[string]any{"state": "CLOSED"}, []int{2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := listIssues(t, ctx, session, tc.extra)
			if !reflect.DeepEqual(got.numbers(), tc.want) || got.TotalCount != len(tc.want) {
				t.Errorf("numbers = %v, total = %d, want %v", got.numbers(), got.TotalCount, tc.want)
			}
			for _, number := range got.numbers() {
				if number == pullRequestNumber {
					t.Error("pull request appeared in issue list")
				}
			}
		})
	}
}

func TestListIssuesCursorPagination(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	var all []int
	extra := map[string]any{"perPage": 2}
	for page := 0; ; page++ {
		if page > 5 {
			t.Fatal("pagination did not finish")
		}
		got := listIssues(t, ctx, session, extra)
		all = append(all, got.numbers()...)
		if got.TotalCount != 5 {
			t.Errorf("total = %d, want 5", got.TotalCount)
		}
		if got.PageInfo.HasPreviousPage != (page > 0) {
			t.Errorf("page %d hasPreviousPage = %v", page, got.PageInfo.HasPreviousPage)
		}
		if !got.PageInfo.HasNextPage {
			break
		}
		extra = map[string]any{"perPage": 2, "after": got.PageInfo.EndCursor}
	}
	if want := []int{6, 5, 3, 2, 1}; !reflect.DeepEqual(all, want) {
		t.Errorf("paged numbers = %v, want %v", all, want)
	}
}

func TestIssueDeferredOptionsReturnErrors(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	for _, tc := range []struct {
		name  string
		extra map[string]any
		want  string
	}{
		{"labels", map[string]any{"labels": []string{"bug"}}, "labels"},
		{"since", map[string]any{"since": "2026-01-01T00:00:00Z"}, "since"},
		{"order field", map[string]any{"orderBy": "UPDATED_AT", "direction": "DESC"}, "orderBy"},
		{"order direction", map[string]any{"orderBy": "CREATED_AT", "direction": "ASC"}, "orderBy"},
		{"field filters", map[string]any{"field_filters": []map[string]any{{"field_name": "Priority", "value": "P1"}}}, "issue fields"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"owner": fixtureOwner, "repo": publicRepo}
			for key, value := range tc.extra {
				args[key] = value
			}
			result, text := callTool(t, ctx, session, "list_issues", args)
			if !result.IsError || !strings.Contains(text, tc.want) {
				t.Errorf("IsError = %v, text = %q; want an error mentioning %q", result.IsError, text, tc.want)
			}
		})
	}

	result, text := callTool(t, ctx, session, "issue_read", issueArgs(publicRepo, "get_sub_issues", 1))
	if !result.IsError || !strings.Contains(strings.ToLower(text), "sub-issues") {
		t.Errorf("get_sub_issues: IsError = %v, text = %q", result.IsError, text)
	}
	result, text = callTool(t, ctx, session, "issue_read", issueArgs(publicRepo, "get_parent", 1))
	if !result.IsError || !strings.Contains(text, "parent") {
		t.Errorf("get_parent: IsError = %v, text = %q", result.IsError, text)
	}
}

func TestIssueErrors(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	t.Run("missing issue", func(t *testing.T) {
		for _, method := range []string{"get", "get_comments", "get_labels"} {
			result, text := callTool(t, ctx, session, "issue_read", issueArgs(publicRepo, method, 9999))
			if !result.IsError {
				t.Errorf("%s: IsError = %v, text = %q", method, result.IsError, text)
			}
		}
	})
	t.Run("labels on a pull request number", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "issue_read", issueArgs(publicRepo, "get_labels", pullRequestNumber))
		if !result.IsError || !strings.Contains(text, "Could not resolve to an Issue") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("missing repo", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "list_issues", map[string]any{"owner": fixtureOwner, "repo": "no-such-repo"})
		if !result.IsError || !strings.Contains(text, "Could not resolve to a Repository") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("invalid cursor", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "list_issues", map[string]any{
			"owner": fixtureOwner, "repo": publicRepo, "after": "not-a-cursor",
		})
		if !result.IsError || !strings.Contains(text, "cursor") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("invalid token", func(t *testing.T) {
		invalid := connect(t, ctx, env, "invalid-token")
		result, text := callTool(t, ctx, invalid, "list_issues", map[string]any{"owner": fixtureOwner, "repo": publicRepo})
		if !result.IsError || !strings.Contains(text, "401") {
			t.Errorf("list_issues: IsError = %v, text = %q", result.IsError, text)
		}
		result, text = callTool(t, ctx, invalid, "issue_read", issueArgs(publicRepo, "get", 1))
		if !result.IsError || !strings.Contains(text, "401") {
			t.Errorf("issue_read: IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("token without issue scope", func(t *testing.T) {
		narrow := connect(t, ctx, env, env["FORGE_TEST_NARROW_TOKEN"])
		result, text := callTool(t, ctx, narrow, "issue_read", issueArgs(publicRepo, "get", 1))
		if !result.IsError || !strings.Contains(text, "403") {
			t.Errorf("issue_read: IsError = %v, text = %q", result.IsError, text)
		}
		result, text = callTool(t, ctx, narrow, "list_issues", map[string]any{"owner": fixtureOwner, "repo": publicRepo})
		if !result.IsError || strings.Contains(text, "First open issue") {
			t.Errorf("list_issues: IsError = %v, text = %q", result.IsError, text)
		}
	})
}

func postGraphQL(t *testing.T, ctx context.Context, env map[string]string, token, query string) (int, string) {
	t.Helper()
	payload, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"http://127.0.0.1:"+env["GATEWAY_PORT"]+"/api/graphql", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(body)
}

func TestGraphQLLimits(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	token := env["FORGE_TEST_TOKEN"]

	if status, body := postGraphQL(t, ctx, env, "", `{ repository(owner:"forge-test", name:"seed-public") { isPrivate } }`); status != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, body = %s", status, body)
	}

	var aliases strings.Builder
	aliases.WriteString(`{ repository(owner:"forge-test", name:"seed-public") {`)
	for i := range 400 {
		aliases.WriteString(" a")
		aliases.WriteString(strings.Repeat("x", i%3))
		aliases.WriteString(string(rune('a' + i%26)))
		aliases.WriteString(": isPrivate")
	}
	aliases.WriteString(" } }")
	if _, body := postGraphQL(t, ctx, env, token, aliases.String()); !strings.Contains(strings.ToLower(body), "selection") {
		t.Errorf("oversized query was not rejected by the selection limit: %.300s", body)
	}

	deep := `{ repository(owner:"forge-test", name:"seed-public") { issues(first:1) { nodes { labels(first:1) { nodes { name { a { b { c { d { e } } } } } } } } } } }`
	if _, body := postGraphQL(t, ctx, env, token, deep); !strings.Contains(strings.ToLower(body), "depth") {
		t.Errorf("too-deep query was not rejected by the depth limit: %.300s", body)
	}

	// With introspection disabled, the library returns empty data rather than an error.
	if _, body := postGraphQL(t, ctx, env, token, `{ __schema { types { name } } }`); strings.Contains(body, "__schema") || strings.Contains(body, "IssueState") {
		t.Errorf("introspection exposed the schema: %s", body)
	}
}

func TestPrivateIssuesHiddenFromNonCollaborator(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_OTHER_TOKEN"])

	result, text := callTool(t, ctx, session, "issue_read", issueArgs(privateRepo, "get", 1))
	if !result.IsError || strings.Contains(text, "Private issue") {
		t.Errorf("get: IsError = %v, text = %q", result.IsError, text)
	}
	result, text = callTool(t, ctx, session, "issue_read", issueArgs(privateRepo, "get_comments", 1))
	if !result.IsError {
		t.Errorf("get_comments: text = %q", text)
	}
	result, text = callTool(t, ctx, session, "issue_read", issueArgs(privateRepo, "get_labels", 1))
	if !result.IsError || !strings.Contains(text, "Could not resolve to a Repository") {
		t.Errorf("get_labels: IsError = %v, text = %q", result.IsError, text)
	}
	result, text = callTool(t, ctx, session, "list_issues", map[string]any{"owner": fixtureOwner, "repo": privateRepo})
	if !result.IsError || !strings.Contains(text, "Could not resolve to a Repository") || strings.Contains(text, "Private issue") {
		t.Errorf("list_issues: IsError = %v, text = %q", result.IsError, text)
	}

	owner := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])
	result, text = callTool(t, ctx, owner, "list_issues", map[string]any{"owner": fixtureOwner, "repo": privateRepo})
	if result.IsError || !strings.Contains(text, "Private issue") {
		t.Errorf("owner list_issues: IsError = %v, text = %q", result.IsError, text)
	}
}
