//go:build conformance

package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	fixtureOwner    = "forge-test"
	publicRepo      = "seed-public"
	privateRepo     = "seed-private"
	fixtureBranches = 63
	largeFileSize   = 1200000
)

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, name string, args map[string]any) (*mcp.CallToolResult, string) {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s(%v): %v", name, args, err)
	}
	return result, resultText(t, result)
}

func fileArgs(repo, path, ref string) map[string]any {
	args := map[string]any{"owner": fixtureOwner, "repo": repo, "path": path}
	if ref != "" {
		args["ref"] = ref
	}
	return args
}

func embeddedFile(t *testing.T, result *mcp.CallToolResult, text string) *mcp.ResourceContents {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool failed: %s", text)
	}
	for _, content := range result.Content {
		if resource, ok := content.(*mcp.EmbeddedResource); ok {
			return resource.Resource
		}
	}
	t.Fatalf("no embedded resource in result: %s", text)
	return nil
}

func giteaGet(t *testing.T, ctx context.Context, token, path string, out any) {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "compose", "--project-directory", "../deploy",
		"-f", "../deploy/docker-compose.yml", "exec", "-T", "gitea",
		"curl", "-fsS", "-H", "@-", "http://localhost:3000/api/v1"+path)
	cmd.Stdin = strings.NewReader(fmt.Sprintf("Authorization: token %s\n", token))
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("read %s directly from Gitea: %v", path, err)
	}
	if err := json.Unmarshal(output, out); err != nil {
		t.Fatal(err)
	}
}

func branchSHA(t *testing.T, ctx context.Context, env map[string]string, repo, branch string) string {
	t.Helper()
	var body struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	giteaGet(t, ctx, env["FORGE_TEST_TOKEN"], "/repos/"+fixtureOwner+"/"+repo+"/branches/"+branch, &body)
	if body.Commit.ID == "" {
		t.Fatalf("no commit for branch %s", branch)
	}
	return body.Commit.ID
}

func gatewayGet(t *testing.T, ctx context.Context, rawURL, token string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	return resp.StatusCode, body
}

type minimalBranch struct {
	Name      string `json:"name"`
	SHA       string `json:"sha"`
	Protected bool   `json:"protected"`
}

func listBranches(t *testing.T, ctx context.Context, session *mcp.ClientSession, page, perPage int) []minimalBranch {
	t.Helper()
	result, text := callTool(t, ctx, session, "list_branches", map[string]any{
		"owner": fixtureOwner, "repo": publicRepo, "page": page, "perPage": perPage,
	})
	if result.IsError {
		t.Fatalf("list_branches failed: %s", text)
	}
	var branches []minimalBranch
	if err := json.Unmarshal([]byte(text), &branches); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	return branches
}

func TestListBranchesReturnsRealBranches(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	branches := listBranches(t, ctx, session, 1, 100)
	if len(branches) != fixtureBranches {
		t.Fatalf("got %d branches, want %d", len(branches), fixtureBranches)
	}
	byName := map[string]minimalBranch{}
	for _, branch := range branches {
		byName[branch.Name] = branch
	}
	for _, name := range []string{"main", "feature/login", "feature/login-v2"} {
		got, ok := byName[name]
		if !ok {
			t.Errorf("branch %s is missing", name)
			continue
		}
		if want := branchSHA(t, ctx, env, publicRepo, name); got.SHA != want {
			t.Errorf("%s sha = %s, want %s", name, got.SHA, want)
		}
		if got.Protected {
			t.Errorf("%s reported as protected", name)
		}
	}
}

func TestListBranchesPagination(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	seen := map[string]bool{}
	for page := 1; page <= 2; page++ {
		branches := listBranches(t, ctx, session, page, 2)
		if len(branches) != 2 {
			t.Fatalf("page %d has %d branches, want 2", page, len(branches))
		}
		for _, branch := range branches {
			if seen[branch.Name] {
				t.Errorf("branch %s repeated across pages", branch.Name)
			}
			seen[branch.Name] = true
		}
	}
	if beyond := listBranches(t, ctx, session, 99, 100); len(beyond) != 0 {
		t.Errorf("page past the end returned %d branches", len(beyond))
	}

	base := "http://127.0.0.1:" + env["GATEWAY_PORT"] + "/api/v3/repos/" + fixtureOwner + "/" + publicRepo + "/branches"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"?per_page=2&page=2", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+env["FORGE_TEST_TOKEN"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	link := resp.Header.Get("Link")
	for _, want := range []string{`rel="next"`, `rel="prev"`, `rel="first"`, `rel="last"`, base} {
		if !strings.Contains(link, want) {
			t.Errorf("Link header %q is missing %s", link, want)
		}
	}
}

func TestGetFileContentsFiles(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	for _, tc := range []struct{ path, want string }{
		{"README.md", "hello forge\n"},
		{"empty.txt", ""},
		{"src/pkg/lib.go", "package pkg\n"},
		{"docs/notes été.md", "spaced unicode\n"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, tc.path, ""))
			if file := embeddedFile(t, result, text); file.Text != tc.want {
				t.Errorf("content = %q, want %q", file.Text, tc.want)
			}
		})
	}

	t.Run("binary", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, "logo.bin", ""))
		want := "\x00\x01\x02PNG\xff\xfe"
		if file := embeddedFile(t, result, text); string(file.Blob) != want {
			t.Errorf("blob = %q, want %q", file.Blob, want)
		}
	})
}

func TestGetFileContentsDirectories(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, "/", ""))
	if result.IsError {
		t.Fatalf("root listing failed: %s", text)
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(text), &entries); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	types := map[string]any{}
	for _, entry := range entries {
		types[entry["name"].(string)] = entry["type"]
	}
	if types["README.md"] != "file" || types["src"] != "dir" {
		t.Errorf("root entries = %v", types)
	}
	if strings.Contains(text, "gitea:3000") {
		t.Errorf("internal address leaked: %s", text)
	}

	args := fileArgs(publicRepo, "src", "")
	args["fields"] = []string{"name", "type"}
	result, text = callTool(t, ctx, session, "get_file_contents", args)
	if result.IsError {
		t.Fatalf("filtered listing failed: %s", text)
	}
	var filtered []map[string]any
	if err := json.Unmarshal([]byte(text), &filtered); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}
	if len(filtered) != 1 || len(filtered[0]) != 2 || filtered[0]["name"] != "pkg" || filtered[0]["type"] != "dir" {
		t.Errorf("filtered entries = %v", filtered)
	}

	result, text = callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, "many", ""))
	if result.IsError {
		t.Fatalf("large directory failed: %s", text)
	}
	var many []map[string]any
	if err := json.Unmarshal([]byte(text), &many); err != nil {
		t.Fatalf("decode large directory: %v", err)
	}
	if len(many) != 1101 {
		t.Errorf("large directory has %d entries, want 1101", len(many))
	}
}

func TestGetFileContentsRefs(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])
	loginSHA := branchSHA(t, ctx, env, publicRepo, "feature/login")

	for _, tc := range []struct{ name, ref, want string }{
		{"short branch", "main", "hello forge\n"},
		{"qualified branch", "refs/heads/main", "hello forge\n"},
		{"partial branch", "heads/main", "hello forge\n"},
		{"slash branch", "feature/login", "hello login\n"},
		{"longer sibling branch", "feature/login-v2", "hello login v2\n"},
		{"lightweight tag", "v1-light", "hello v1\n"},
		{"annotated tag", "v1-annotated", "hello v1\n"},
		{"qualified annotated tag", "refs/tags/v1-annotated", "hello v1\n"},
		{"full sha", loginSHA, "hello login\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, "README.md", tc.ref))
			if file := embeddedFile(t, result, text); file.Text != tc.want {
				t.Errorf("content = %q, want %q", file.Text, tc.want)
			}
		})
	}

	t.Run("branch prefix does not match", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, "README.md", "feature/log"))
		if !result.IsError || !strings.Contains(text, "could not resolve ref") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})

	t.Run("main falls back to default branch", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(privateRepo, "secret.txt", "main"))
		if file := embeddedFile(t, result, text); file.Text != "private secret\n" {
			t.Errorf("content = %q", file.Text)
		}
		if !strings.Contains(text, "refs/heads/trunk") {
			t.Errorf("fallback note missing: %q", text)
		}
	})
}

var downloadURL = regexp.MustCompile(`http://\S+`)

func TestGetFileContentsLargeFiles(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	// many/ has more than one tree page, so this also covers tree aggregation.
	for _, path := range []string{"large.txt", "many/large.txt"} {
		t.Run(path, func(t *testing.T) {
			result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, path, ""))
			if result.IsError {
				t.Fatalf("tool failed: %s", text)
			}
			var link *mcp.ResourceLink
			for _, content := range result.Content {
				if candidate, ok := content.(*mcp.ResourceLink); ok {
					link = candidate
				}
			}
			if link == nil || link.Size == nil || *link.Size != largeFileSize {
				t.Fatalf("resource link = %+v, text = %s", link, text)
			}
			address := downloadURL.FindString(text)
			if !strings.HasPrefix(address, "http://127.0.0.1:"+env["GATEWAY_PORT"]+"/raw/") {
				t.Fatalf("download URL %q does not use the gateway", address)
			}
			status, body := gatewayGet(t, ctx, address, env["FORGE_TEST_TOKEN"])
			if status != http.StatusOK || len(body) != largeFileSize {
				t.Errorf("download status = %d, size = %d", status, len(body))
			}
		})
	}
}

func TestGetFileContentsSpecialEntries(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	for _, tc := range []struct{ path, wantType, wantDetail string }{
		{"link-to-readme", "symlink", "README.md"},
		{"vendor/lib", "submodule", "1111111111111111111111111111111111111111"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(publicRepo, tc.path, ""))
			if result.IsError {
				t.Fatalf("tool failed: %s", text)
			}
			// The server nests this metadata as an escaped JSON string.
			metadata := strings.ReplaceAll(text, `\"`, `"`)
			if !strings.Contains(metadata, `"type":"`+tc.wantType+`"`) || !strings.Contains(metadata, tc.wantDetail) {
				t.Errorf("metadata = %s", text)
			}
		})
	}
}

func TestReadCodeErrors(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	owner := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	t.Run("missing path", func(t *testing.T) {
		result, text := callTool(t, ctx, owner, "get_file_contents", fileArgs(publicRepo, "no/such/file.txt", ""))
		if !result.IsError || !strings.Contains(text, "does not point to a file or directory") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("bad ref", func(t *testing.T) {
		result, text := callTool(t, ctx, owner, "get_file_contents", fileArgs(publicRepo, "README.md", "no-such-ref"))
		if !result.IsError || !strings.Contains(text, "could not resolve ref") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("unknown sha", func(t *testing.T) {
		result, text := callTool(t, ctx, owner, "get_file_contents",
			fileArgs(publicRepo, "README.md", "2222222222222222222222222222222222222222"))
		if !result.IsError || strings.Contains(text, "502") {
			t.Errorf("IsError = %v, text = %q; want a clean not-found error", result.IsError, text)
		}
	})
	t.Run("missing repo", func(t *testing.T) {
		result, text := callTool(t, ctx, owner, "list_branches", map[string]any{"owner": fixtureOwner, "repo": "no-such-repo"})
		if !result.IsError || !strings.Contains(text, "404") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("invalid token", func(t *testing.T) {
		session := connect(t, ctx, env, "invalid-token")
		result, text := callTool(t, ctx, session, "list_branches", map[string]any{"owner": fixtureOwner, "repo": publicRepo})
		if !result.IsError || !strings.Contains(text, "401") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
	t.Run("token without repository scope", func(t *testing.T) {
		session := connect(t, ctx, env, env["FORGE_TEST_NARROW_TOKEN"])
		result, text := callTool(t, ctx, session, "list_branches", map[string]any{"owner": fixtureOwner, "repo": publicRepo})
		if !result.IsError || !strings.Contains(text, "403") {
			t.Errorf("IsError = %v, text = %q", result.IsError, text)
		}
	})
}

func TestPrivateRepoHiddenFromNonCollaborator(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	other := env["FORGE_TEST_OTHER_TOKEN"]
	if other == "" {
		t.Fatal("FORGE_TEST_OTHER_TOKEN is missing (run deploy/seed.sh)")
	}
	session := connect(t, ctx, env, other)

	result, text := callTool(t, ctx, session, "get_file_contents", fileArgs(privateRepo, "secret.txt", "trunk"))
	if !result.IsError || strings.Contains(text, "private secret") {
		t.Errorf("get_file_contents: IsError = %v, text = %q", result.IsError, text)
	}
	result, text = callTool(t, ctx, session, "list_branches", map[string]any{"owner": fixtureOwner, "repo": privateRepo})
	if !result.IsError || !strings.Contains(text, "404") {
		t.Errorf("list_branches: IsError = %v, text = %q", result.IsError, text)
	}

	sha := branchSHA(t, ctx, env, privateRepo, "trunk")
	raw := "http://127.0.0.1:" + env["GATEWAY_PORT"] + "/raw/" + fixtureOwner + "/" + privateRepo + "/" + sha + "/secret.txt"
	for name, token := range map[string]string{"anonymous": "", "other user": other} {
		if status, body := gatewayGet(t, ctx, raw, token); status != http.StatusNotFound || strings.Contains(string(body), "private secret") {
			t.Errorf("%s raw read: status = %d, body = %q", name, status, body)
		}
	}
	if status, body := gatewayGet(t, ctx, raw, env["FORGE_TEST_TOKEN"]); status != http.StatusOK || string(body) != "private secret\n" {
		t.Errorf("owner raw read: status = %d, body = %q", status, body)
	}
}
