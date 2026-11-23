//go:build conformance

package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	prRepo        = "pr-fixtures"
	prPrivateRepo = "pr-private"
	forkOwner     = "forge-other"
	fixturePR     = 1
	deletedHeadPR = 2
)

type prBranchView struct {
	Ref  string `json:"ref"`
	SHA  string `json:"sha"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

type prView struct {
	Number             int          `json:"number"`
	Title              string       `json:"title"`
	Body               string       `json:"body"`
	State              string       `json:"state"`
	Draft              bool         `json:"draft"`
	MergeableState     string       `json:"mergeable_state"`
	RequestedReviewers []string     `json:"requested_reviewers"`
	Head               prBranchView `json:"head"`
	Base               prBranchView `json:"base"`
	Additions          int          `json:"additions"`
	Deletions          int          `json:"deletions"`
	ChangedFiles       int          `json:"changed_files"`
	Commits            int          `json:"commits"`
	Comments           int          `json:"comments"`
}

// prDiff is how the MCP server wraps get_diff output.
type prDiff struct {
	Diff string `json:"diff"`
}

func prReadArgs(repo string, number int, method string) map[string]any {
	return map[string]any{"owner": writerOwner, "repo": repo, "pullNumber": number, "method": method}
}

func readPR[T any](t *testing.T, ctx context.Context, session *mcp.ClientSession, args map[string]any) (T, string) {
	t.Helper()
	var out T
	result, text := callTool(t, ctx, session, "pull_request_read", args)
	if result.IsError {
		t.Fatalf("pull_request_read(%v) failed: %s", args["method"], text)
	}
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return out, text
}

// branchWithChange creates a branch holding one new file, using only MCP tools.
func branchWithChange(t *testing.T, ctx context.Context, session *mcp.ClientSession, owner, prefix string) string {
	t.Helper()
	branch := uniqueName(t, prefix)
	result, text := callTool(t, ctx, session, "create_branch", map[string]any{"owner": owner, "repo": prRepo, "branch": branch})
	if result.IsError {
		t.Fatalf("create_branch failed: %s", text)
	}
	result, text = callTool(t, ctx, session, "create_or_update_file", map[string]any{
		"owner": owner, "repo": prRepo, "branch": branch,
		"path": "changes/" + branch + ".txt", "content": "change on " + branch + "\n", "message": "Add change",
	})
	if result.IsError {
		t.Fatalf("create_or_update_file failed: %s", text)
	}
	return branch
}

func openPR(t *testing.T, ctx context.Context, session *mcp.ClientSession, args map[string]any) int {
	t.Helper()
	result, text := callTool(t, ctx, session, "create_pull_request", args)
	if result.IsError {
		t.Fatalf("create_pull_request failed: %s", text)
	}
	var created struct {
		ID  string `json:"id"`
		URL string `json:"html_url"`
	}
	if err := json.Unmarshal([]byte(text), &created); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	number, err := strconv.Atoi(created.URL[strings.LastIndex(created.URL, "/")+1:])
	if err != nil || created.ID == "" {
		t.Fatalf("unexpected create response: %s", text)
	}
	return number
}

func prArgs(title, head string) map[string]any {
	return map[string]any{"owner": writerOwner, "repo": prRepo, "title": title, "head": head, "base": "main"}
}

func storedPR(t *testing.T, ctx context.Context, env map[string]string, number int) map[string]any {
	t.Helper()
	var out map[string]any
	giteaGet(t, ctx, env["FORGE_TEST_PR_TOKEN"], fmt.Sprintf("/repos/%s/%s/pulls/%d", writerOwner, prRepo, number), &out)
	return out
}

func giteaRaw(t *testing.T, ctx context.Context, token, path string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "compose", "--project-directory", "../deploy",
		"-f", "../deploy/docker-compose.yml", "exec", "-T", "gitea",
		"curl", "-fsS", "-H", "@-", "http://localhost:3000/api/v1"+path)
	cmd.Stdin = strings.NewReader("Authorization: token " + token + "\n")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("read %s directly from Gitea: %v", path, err)
	}
	return string(output)
}

func TestCreatePullRequest(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_PR_TOKEN"])

	t.Run("normal", func(t *testing.T) {
		branch := branchWithChange(t, ctx, session, writerOwner, "normal")
		args := prArgs("Normal PR", branch)
		args["body"] = "Body text"
		number := openPR(t, ctx, session, args)
		stored := storedPR(t, ctx, env, number)
		head, _ := stored["head"].(map[string]any)
		base, _ := stored["base"].(map[string]any)
		if stored["title"] != "Normal PR" || stored["body"] != "Body text" || stored["draft"] != false ||
			head["ref"] != branch || base["ref"] != "main" {
			t.Errorf("stored PR = %v", stored)
		}
	})

	t.Run("draft", func(t *testing.T) {
		args := prArgs("Draft PR", branchWithChange(t, ctx, session, writerOwner, "draft"))
		args["draft"] = true
		number := openPR(t, ctx, session, args)
		if stored := storedPR(t, ctx, env, number); stored["draft"] != true {
			t.Errorf("stored draft = %v", stored["draft"])
		}
		view, _ := readPR[prView](t, ctx, session, prReadArgs(prRepo, number, "get"))
		if !view.Draft || view.Title != "Draft PR" || view.MergeableState != "draft" {
			t.Errorf("view = %+v", view)
		}
	})

	t.Run("reviewers", func(t *testing.T) {
		args := prArgs("Reviewed PR", branchWithChange(t, ctx, session, writerOwner, "reviewers"))
		args["reviewers"] = []string{forkOwner}
		number := openPR(t, ctx, session, args)
		view, _ := readPR[prView](t, ctx, session, prReadArgs(prRepo, number, "get"))
		if len(view.RequestedReviewers) != 1 || view.RequestedReviewers[0] != forkOwner {
			t.Errorf("requested reviewers = %v", view.RequestedReviewers)
		}
	})

	t.Run("head from a fork", func(t *testing.T) {
		fork := connect(t, ctx, env, env["FORGE_TEST_FORK_TOKEN"])
		branch := branchWithChange(t, ctx, fork, forkOwner, "fork")
		number := openPR(t, ctx, fork, prArgs("Fork PR", forkOwner+":"+branch))
		view, _ := readPR[prView](t, ctx, session, prReadArgs(prRepo, number, "get"))
		if view.Head.Ref != branch || view.Head.Repo == nil || view.Head.Repo.FullName != forkOwner+"/"+prRepo {
			t.Errorf("head = %+v", view.Head)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_pull_request", prArgs("Duplicate", "feature/review"))
		wantToolError(t, result, text, "already exists")
	})

	t.Run("no commits between branches", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_pull_request", prArgs("Empty", "no-changes"))
		wantToolError(t, result, text, "No commits between")
		giteaPort := env["GITEA_PORT"]
		if giteaPort == "" {
			giteaPort = "3000" // The Compose default, used in CI.
		}
		status, _ := gatewayGet(t, ctx, fmt.Sprintf("http://127.0.0.1:%s/api/v1/repos/%s/%s/pulls/main/no-changes",
			giteaPort, writerOwner, prRepo), env["FORGE_TEST_PR_TOKEN"])
		if status != 404 {
			t.Errorf("an empty pull request was created (lookup status %d)", status)
		}
	})

	t.Run("missing branch", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_pull_request", prArgs("Missing", "no-such-branch"))
		wantToolError(t, result, text, "does not exist")
	})
}

func TestPullRequestReadGet(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_PR_TOKEN"])

	view, text := readPR[prView](t, ctx, session, prReadArgs(prRepo, fixturePR, "get"))
	stored := storedPR(t, ctx, env, fixturePR)
	head, _ := stored["head"].(map[string]any)
	base, _ := stored["base"].(map[string]any)
	if view.Number != fixturePR || view.Title != "Fixture review PR" || view.Body != "Fixture body" || view.State != "open" {
		t.Errorf("identity = %+v", view)
	}
	if view.Head.Ref != "feature/review" || view.Head.SHA != head["sha"] || view.Base.Ref != "main" || view.Base.SHA != base["sha"] {
		t.Errorf("branches = %+v / %+v", view.Head, view.Base)
	}
	if view.Additions != 4 || view.Deletions != 1 || view.ChangedFiles != 3 || view.Commits != 2 || view.Comments != 2 {
		t.Errorf("counts = %+v", view)
	}
	if view.MergeableState != "clean" {
		t.Errorf("mergeable_state = %s", view.MergeableState)
	}
	if len(view.RequestedReviewers) != 1 || view.RequestedReviewers[0] != forkOwner {
		t.Errorf("requested reviewers = %v", view.RequestedReviewers)
	}
	if strings.Contains(text, "gitea:3000") {
		t.Errorf("internal address leaked: %s", text)
	}

	t.Run("deleted head branch keeps its name", func(t *testing.T) {
		gone, _ := readPR[prView](t, ctx, session, prReadArgs(prRepo, deletedHeadPR, "get"))
		if gone.State != "closed" || gone.Head.Ref != "gone-head" || gone.Head.SHA == "" {
			t.Errorf("view = %+v", gone)
		}
		// The status path dereferences head.sha, so it must still work here.
		if _, _ = readPR[struct {
			State string `json:"state"`
		}](t, ctx, session, prReadArgs(prRepo, deletedHeadPR, "get_status")); t.Failed() {
			t.Error("get_status failed on a deleted head branch")
		}
	})
}

func TestPullRequestReadDiffFilesCommits(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_PR_TOKEN"])

	t.Run("diff is exact", func(t *testing.T) {
		out, _ := readPR[prDiff](t, ctx, session, prReadArgs(prRepo, fixturePR, "get_diff"))
		want := giteaRaw(t, ctx, env["FORGE_TEST_PR_TOKEN"], fmt.Sprintf("/repos/%s/%s/pulls/%d.diff", writerOwner, prRepo, fixturePR))
		if out.Diff != want {
			t.Errorf("diff differs from Gitea\n got: %q\nwant: %q", out.Diff, want)
		}
		if !strings.Contains(out.Diff, "+line two") {
			t.Errorf("diff is missing a change: %q", out.Diff)
		}
	})

	type file struct {
		Filename  string `json:"filename"`
		Status    string `json:"status"`
		Additions int    `json:"additions"`
		Deletions int    `json:"deletions"`
	}
	t.Run("files", func(t *testing.T) {
		files, _ := readPR[[]file](t, ctx, session, prReadArgs(prRepo, fixturePR, "get_files"))
		statuses := map[string]string{}
		for _, item := range files {
			statuses[item.Filename] = item.Status
		}
		if len(files) != 3 || statuses["feature.txt"] != "added" || statuses["README.md"] != "modified" ||
			statuses["remove-me.txt"] != "removed" {
			t.Errorf("files = %+v", files)
		}
	})

	t.Run("file pagination", func(t *testing.T) {
		seen := map[string]bool{}
		for page := 1; page <= 3; page++ {
			args := prReadArgs(prRepo, fixturePR, "get_files")
			args["page"], args["perPage"] = page, 1
			files, _ := readPR[[]file](t, ctx, session, args)
			if len(files) != 1 || seen[files[0].Filename] {
				t.Fatalf("page %d = %+v, seen %v", page, files, seen)
			}
			seen[files[0].Filename] = true
		}
	})

	t.Run("commits", func(t *testing.T) {
		commits, _ := readPR[[]struct {
			SHA     string `json:"sha"`
			Message string `json:"message"`
			Author  *struct {
				Name string `json:"name"`
			} `json:"author"`
		}](t, ctx, session, prReadArgs(prRepo, fixturePR, "get_commits"))
		messages := map[string]bool{}
		for _, commit := range commits {
			messages[strings.TrimSpace(commit.Message)] = true
			if commit.SHA == "" || commit.Author == nil || commit.Author.Name != "Forge Writer" {
				t.Errorf("commit = %+v", commit)
			}
		}
		if len(commits) != 2 || !messages["Add feature file"] || !messages["Update README and remove file"] {
			t.Errorf("messages = %v", messages)
		}
	})
}

func TestPullRequestReadCI(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_PR_TOKEN"])
	headSHA := storedPR(t, ctx, env, fixturePR)["head"].(map[string]any)["sha"]

	t.Run("status", func(t *testing.T) {
		status, _ := readPR[struct {
			State      string `json:"state"`
			SHA        string `json:"sha"`
			TotalCount int    `json:"total_count"`
			Statuses   []struct {
				State   string `json:"state"`
				Context string `json:"context"`
			} `json:"statuses"`
		}](t, ctx, session, prReadArgs(prRepo, fixturePR, "get_status"))
		states := map[string]string{}
		for _, item := range status.Statuses {
			states[item.Context] = item.State
		}
		if status.State != "failure" || status.SHA != headSHA || status.TotalCount != 3 ||
			states["ci/build"] != "success" || states["ci/test"] != "pending" || states["ci/lint"] != "failure" {
			t.Errorf("status = %+v", status)
		}
	})

	t.Run("check runs", func(t *testing.T) {
		runs, _ := readPR[struct {
			TotalCount int `json:"total_count"`
			CheckRuns  []struct {
				Name       string `json:"name"`
				Status     string `json:"status"`
				Conclusion string `json:"conclusion"`
			} `json:"check_runs"`
		}](t, ctx, session, prReadArgs(prRepo, fixturePR, "get_check_runs"))
		byName := map[string]string{}
		for _, run := range runs.CheckRuns {
			byName[run.Name] = run.Status + "/" + run.Conclusion
		}
		if runs.TotalCount != 3 || byName["ci/build"] != "completed/success" ||
			byName["ci/test"] != "in_progress/" || byName["ci/lint"] != "completed/failure" {
			t.Errorf("check runs = %+v", runs)
		}
	})
}

func TestPullRequestReadReviews(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_PR_TOKEN"])

	t.Run("reviews exclude pending requests", func(t *testing.T) {
		reviews, _ := readPR[[]struct {
			State string `json:"state"`
			Body  string `json:"body"`
		}](t, ctx, session, prReadArgs(prRepo, fixturePR, "get_reviews"))
		byBody := map[string]string{}
		for _, review := range reviews {
			byBody[review.Body] = review.State
		}
		if len(reviews) != 2 || byBody["Fixture review"] != "COMMENTED" || byBody["Looks good"] != "APPROVED" {
			t.Errorf("reviews = %+v", reviews)
		}
	})

	t.Run("general comments", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "pull_request_read", prReadArgs(prRepo, fixturePR, "get_comments"))
		if result.IsError {
			t.Fatalf("get_comments failed: %s", text)
		}
		for _, want := range []string{"First general comment", "Second general comment"} {
			if !strings.Contains(text, want) {
				t.Errorf("comments are missing %q: %s", want, text)
			}
		}
		if strings.Contains(text, "First thread") {
			t.Errorf("review comments appeared as general comments: %s", text)
		}
	})

	type comment struct {
		Body   string `json:"body"`
		Path   string `json:"path"`
		Line   *int   `json:"line"`
		Author string `json:"author"`
	}
	type thread struct {
		ID         string    `json:"id"`
		IsResolved bool      `json:"is_resolved"`
		Comments   []comment `json:"comments"`
		TotalCount int       `json:"total_count"`
	}
	type threads struct {
		ReviewThreads []thread `json:"review_threads"`
		TotalCount    int      `json:"totalCount"`
		PageInfo      struct {
			HasNextPage bool   `json:"hasNextPage"`
			EndCursor   string `json:"endCursor"`
		} `json:"pageInfo"`
	}

	t.Run("review threads", func(t *testing.T) {
		out, _ := readPR[threads](t, ctx, session, prReadArgs(prRepo, fixturePR, "get_review_comments"))
		if len(out.ReviewThreads) != 2 || out.TotalCount != 2 || out.PageInfo.HasNextPage {
			t.Fatalf("threads = %+v", out)
		}
		byPath := map[string]thread{}
		for _, item := range out.ReviewThreads {
			byPath[item.Comments[0].Path] = item
		}
		first, second := byPath["feature.txt"], byPath["README.md"]
		if !first.IsResolved || first.TotalCount != 2 || len(first.Comments) != 2 ||
			first.Comments[0].Body != "First thread" || first.Comments[0].Author != "forge-test" ||
			first.Comments[1].Body != "Reply in first thread" || first.Comments[1].Author != writerOwner ||
			first.Comments[0].Line == nil || *first.Comments[0].Line != 2 {
			t.Errorf("first thread = %+v", first)
		}
		if second.IsResolved || second.TotalCount != 1 || second.Comments[0].Body != "Second thread" {
			t.Errorf("second thread = %+v", second)
		}
	})

	t.Run("review thread pagination", func(t *testing.T) {
		args := prReadArgs(prRepo, fixturePR, "get_review_comments")
		args["perPage"] = 1
		page1, _ := readPR[threads](t, ctx, session, args)
		if len(page1.ReviewThreads) != 1 || !page1.PageInfo.HasNextPage || page1.PageInfo.EndCursor == "" {
			t.Fatalf("page 1 = %+v", page1)
		}
		args["after"] = page1.PageInfo.EndCursor
		page2, _ := readPR[threads](t, ctx, session, args)
		if len(page2.ReviewThreads) != 1 || page2.PageInfo.HasNextPage ||
			page2.ReviewThreads[0].ID == page1.ReviewThreads[0].ID {
			t.Errorf("page 2 = %+v", page2)
		}
	})
}

func TestPullRequestAccessAndErrors(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	owner := connect(t, ctx, env, env["FORGE_TEST_PR_TOKEN"])

	t.Run("read-only token cannot create", func(t *testing.T) {
		branch := branchWithChange(t, ctx, owner, writerOwner, "readonly")
		session := connect(t, ctx, env, env["FORGE_TEST_PR_READONLY_TOKEN"])
		result, text := callTool(t, ctx, session, "create_pull_request", prArgs("Denied", branch))
		wantToolError(t, result, text, "403")
	})

	t.Run("outsider cannot see a private pull request", func(t *testing.T) {
		session := connect(t, ctx, env, env["FORGE_TEST_PR_OUTSIDER_TOKEN"])
		for _, method := range []string{"get", "get_diff", "get_files", "get_commits", "get_status", "get_check_runs", "get_reviews", "get_comments"} {
			result, text := callTool(t, ctx, session, "pull_request_read", prReadArgs(prPrivateRepo, 1, method))
			wantToolError(t, result, text, "404")
			if strings.Contains(text, "Private PR") || strings.Contains(text, "private change") {
				t.Errorf("%s leaked private content: %s", method, text)
			}
		}
		result, text := callTool(t, ctx, session, "pull_request_read", prReadArgs(prPrivateRepo, 1, "get_review_comments"))
		wantToolError(t, result, text, "Could not resolve")
	})

	t.Run("owner can read a private pull request", func(t *testing.T) {
		view, _ := readPR[prView](t, ctx, owner, prReadArgs(prPrivateRepo, 1, "get"))
		if view.Title != "Private PR" {
			t.Errorf("view = %+v", view)
		}
	})

	t.Run("missing pull request", func(t *testing.T) {
		result, text := callTool(t, ctx, owner, "pull_request_read", prReadArgs(prRepo, 99999, "get"))
		wantToolError(t, result, text, "404")
		result, text = callTool(t, ctx, owner, "pull_request_read", prReadArgs(prRepo, 99999, "get_review_comments"))
		wantToolError(t, result, text, "Could not resolve to a PullRequest")
	})
}

// TestAgentPullRequestWorkflow is the full end-to-end path an agent follows.
func TestAgentPullRequestWorkflow(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_PR_TOKEN"])
	branch := uniqueName(t, "agent/workflow")
	path := "src/" + uniqueName(t, "generated") + ".go"
	const content = "package generated\n\n// Value is é ✓.\nconst Value = 42\n"

	result, text := callTool(t, ctx, session, "create_branch", map[string]any{"owner": writerOwner, "repo": prRepo, "branch": branch})
	if result.IsError {
		t.Fatalf("create_branch failed: %s", text)
	}
	result, text = callTool(t, ctx, session, "create_or_update_file", map[string]any{
		"owner": writerOwner, "repo": prRepo, "branch": branch, "path": path, "content": content, "message": "Add generated file",
	})
	if result.IsError {
		t.Fatalf("create_or_update_file failed: %s", text)
	}
	number := openPR(t, ctx, session, prArgs("Agent workflow", branch))

	view, _ := readPR[prView](t, ctx, session, prReadArgs(prRepo, number, "get"))
	if view.Title != "Agent workflow" || view.Head.Ref != branch || view.State != "open" || view.Commits != 1 ||
		view.ChangedFiles != 1 || view.Additions != 4 {
		t.Errorf("view = %+v", view)
	}
	files, _ := readPR[[]struct {
		Filename string `json:"filename"`
		Status   string `json:"status"`
	}](t, ctx, session, prReadArgs(prRepo, number, "get_files"))
	if len(files) != 1 || files[0].Filename != path || files[0].Status != "added" {
		t.Errorf("files = %+v", files)
	}
	diff, _ := readPR[prDiff](t, ctx, session, prReadArgs(prRepo, number, "get_diff"))
	if !strings.Contains(diff.Diff, "+const Value = 42") || !strings.Contains(diff.Diff, "é ✓") {
		t.Errorf("diff = %q", diff.Diff)
	}
}
