//go:build conformance

package conformance

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	writerOwner  = "forge-writer"
	writePublic  = "write-public"
	writePrivate = "write-private"
)

// uniqueName keeps reruns on the same stack from colliding.
func uniqueName(t *testing.T, prefix string) string {
	t.Helper()
	suffix := make([]byte, 5)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	return prefix + "-" + hex.EncodeToString(suffix)
}

func escapePath(path string) string {
	segments := strings.Split(path, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	return strings.Join(segments, "/")
}

func writerBranchSHA(t *testing.T, ctx context.Context, env map[string]string, repo, branch string) string {
	t.Helper()
	var body struct {
		Commit struct {
			ID string `json:"id"`
		} `json:"commit"`
	}
	giteaGet(t, ctx, env["FORGE_TEST_WRITER_TOKEN"], "/repos/"+writerOwner+"/"+repo+"/branches/"+escapePath(branch), &body)
	if body.Commit.ID == "" {
		t.Fatalf("no commit for branch %s", branch)
	}
	return body.Commit.ID
}

type storedFile struct {
	SHA     string `json:"sha"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Target  string `json:"target"`
}

func readStoredFile(t *testing.T, ctx context.Context, env map[string]string, repo, branch, path string) (storedFile, string) {
	t.Helper()
	var file storedFile
	giteaGet(t, ctx, env["FORGE_TEST_WRITER_TOKEN"],
		"/repos/"+writerOwner+"/"+repo+"/contents/"+escapePath(path)+"?ref="+url.QueryEscape(branch), &file)
	decoded, err := base64.StdEncoding.DecodeString(file.Content)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return file, string(decoded)
}

func branchExists(t *testing.T, ctx context.Context, env map[string]string, repo, branch string) bool {
	t.Helper()
	status, _ := gatewayGet(t, ctx, "http://127.0.0.1:"+env["GATEWAY_PORT"]+"/api/v3/repos/"+writerOwner+"/"+repo+
		"/git/ref/heads/"+escapePath(branch), env["FORGE_TEST_WRITER_TOKEN"])
	return status == http.StatusOK
}

func makeBranch(t *testing.T, ctx context.Context, session *mcp.ClientSession, repo, prefix string) string {
	t.Helper()
	branch := uniqueName(t, prefix)
	result, text := callTool(t, ctx, session, "create_branch", map[string]any{"owner": writerOwner, "repo": repo, "branch": branch})
	if result.IsError {
		t.Fatalf("create_branch failed: %s", text)
	}
	return branch
}

func writeArgs(repo, branch, path, content, sha string) map[string]any {
	args := map[string]any{
		"owner": writerOwner, "repo": repo, "branch": branch, "path": path,
		"content": content, "message": "Write " + path,
	}
	if sha != "" {
		args["sha"] = sha
	}
	return args
}

func wantToolError(t *testing.T, result *mcp.CallToolResult, text string, fragments ...string) {
	t.Helper()
	if !result.IsError {
		t.Fatalf("expected an error, got: %s", text)
	}
	for _, fragment := range fragments {
		if !strings.Contains(text, fragment) {
			t.Errorf("error %q is missing %q", text, fragment)
		}
	}
}

func TestCreateBranch(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_WRITER_TOKEN"])
	mainSHA := writerBranchSHA(t, ctx, env, writePublic, "main")
	featureSHA := writerBranchSHA(t, ctx, env, writePublic, "feature")
	if featureSHA == writerBranchSHA(t, ctx, env, writePublic, "feature-2") {
		t.Fatal("fixture branches feature and feature-2 must differ")
	}

	for _, tc := range []struct{ name, branch, from, want string }{
		{"default source", uniqueName(t, "default"), "", mainSHA},
		{"explicit source", uniqueName(t, "explicit"), "main", mainSHA},
		{"source is a prefix of another branch", uniqueName(t, "prefix"), "feature", featureSHA},
		{"slash in name", "agent/" + uniqueName(t, "work"), "feature", featureSHA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := map[string]any{"owner": writerOwner, "repo": writePublic, "branch": tc.branch}
			if tc.from != "" {
				args["from_branch"] = tc.from
			}
			result, text := callTool(t, ctx, session, "create_branch", args)
			if result.IsError {
				t.Fatalf("create_branch failed: %s", text)
			}
			var ref struct {
				Ref    string `json:"ref"`
				Object struct {
					SHA string `json:"sha"`
				} `json:"object"`
			}
			if err := json.Unmarshal([]byte(text), &ref); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			if ref.Ref != "refs/heads/"+tc.branch || ref.Object.SHA != tc.want {
				t.Errorf("response = %+v, want sha %s", ref, tc.want)
			}
			if got := writerBranchSHA(t, ctx, env, writePublic, tc.branch); got != tc.want {
				t.Errorf("stored sha = %s, want %s", got, tc.want)
			}
		})
	}

	t.Run("partial source name does not match", func(t *testing.T) {
		branch := uniqueName(t, "partial")
		result, text := callTool(t, ctx, session, "create_branch", map[string]any{
			"owner": writerOwner, "repo": writePublic, "branch": branch, "from_branch": "featur",
		})
		wantToolError(t, result, text, "404")
		if branchExists(t, ctx, env, writePublic, branch) {
			t.Error("branch was created from a partial name")
		}
	})

	t.Run("duplicate branch", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_branch", map[string]any{
			"owner": writerOwner, "repo": writePublic, "branch": "feature",
		})
		wantToolError(t, result, text, "Reference already exists")
		if got := writerBranchSHA(t, ctx, env, writePublic, "feature"); got != featureSHA {
			t.Errorf("existing branch moved to %s", got)
		}
	})

	t.Run("missing source", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_branch", map[string]any{
			"owner": writerOwner, "repo": writePublic, "branch": uniqueName(t, "x"), "from_branch": "no-such-branch",
		})
		wantToolError(t, result, text, "404")
	})

	t.Run("missing repository", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_branch", map[string]any{
			"owner": writerOwner, "repo": "no-such-repo", "branch": uniqueName(t, "x"),
		})
		wantToolError(t, result, text, "404")
	})

	t.Run("unknown source commit", func(t *testing.T) {
		status, body := gatewayWrite(t, ctx, env, http.MethodPost, writePublic, "/git/refs",
			`{"ref":"refs/heads/`+uniqueName(t, "x")+`","sha":"2222222222222222222222222222222222222222"}`)
		if status != http.StatusUnprocessableEntity || !strings.Contains(body, "Object does not exist") {
			t.Errorf("status = %d, body = %s", status, body)
		}
	})
}

func gatewayWrite(t *testing.T, ctx context.Context, env map[string]string, method, repo, path, body string) (int, string) {
	t.Helper()
	target := "http://127.0.0.1:" + env["GATEWAY_PORT"] + "/api/v3/repos/" + writerOwner + "/" + repo + path
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+env["FORGE_TEST_WRITER_TOKEN"])
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

func TestCreateOrUpdateFile(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_WRITER_TOKEN"])
	branch := makeBranch(t, ctx, session, writePublic, "files")

	for _, tc := range []struct{ name, path, content string }{
		{"new file", "new.txt", "hello\n"},
		{"nested path", "a/b/c/deep.txt", "deep\n"},
		{"unicode", "docs/été notes.md", "contenu été ✓\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := writerBranchSHA(t, ctx, env, writePublic, branch)
			result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, tc.path, tc.content, ""))
			if result.IsError {
				t.Fatalf("write failed: %s", text)
			}
			var out struct {
				Content struct {
					Path string `json:"path"`
					SHA  string `json:"sha"`
				} `json:"content"`
				Commit struct {
					SHA     string `json:"sha"`
					Message string `json:"message"`
					Author  struct {
						Name string `json:"name"`
					} `json:"author"`
				} `json:"commit"`
			}
			if err := json.Unmarshal([]byte(text), &out); err != nil {
				t.Fatalf("decode %q: %v", text, err)
			}
			file, stored := readStoredFile(t, ctx, env, writePublic, branch, tc.path)
			if stored != tc.content {
				t.Errorf("stored content = %q, want %q", stored, tc.content)
			}
			if out.Content.Path != tc.path || out.Content.SHA != file.SHA {
				t.Errorf("response content = %+v, stored sha %s", out.Content, file.SHA)
			}
			after := writerBranchSHA(t, ctx, env, writePublic, branch)
			if after == before || out.Commit.SHA != after {
				t.Errorf("commit = %s, branch moved %s -> %s", out.Commit.SHA, before, after)
			}
			if !strings.HasPrefix(out.Commit.Message, "Write "+tc.path) || out.Commit.Author.Name != writerOwner {
				t.Errorf("commit metadata = %+v", out.Commit)
			}
		})
	}

	t.Run("update with correct sha", func(t *testing.T) {
		file, _ := readStoredFile(t, ctx, env, writePublic, branch, "new.txt")
		result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, "new.txt", "updated\n", file.SHA))
		if result.IsError {
			t.Fatalf("update failed: %s", text)
		}
		if _, stored := readStoredFile(t, ctx, env, writePublic, branch, "new.txt"); stored != "updated\n" {
			t.Errorf("stored content = %q", stored)
		}
	})

	t.Run("stale sha", func(t *testing.T) {
		const stale = "1111111111111111111111111111111111111111"
		result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, "new.txt", "lost\n", stale))
		wantToolError(t, result, text, "SHA mismatch")

		// This guards the Gitea error wording that the gateway maps to 409.
		status, body := gatewayWrite(t, ctx, env, http.MethodPut, writePublic, "/contents/new.txt",
			`{"message":"m","content":"bG9zdAo=","branch":"`+branch+`","sha":"`+stale+`"}`)
		if status != http.StatusConflict {
			t.Errorf("status = %d, body = %s", status, body)
		}
		if _, stored := readStoredFile(t, ctx, env, writePublic, branch, "new.txt"); stored != "updated\n" {
			t.Errorf("stale write changed the file to %q", stored)
		}
	})

	t.Run("existing file without sha", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, "new.txt", "lost\n", ""))
		wantToolError(t, result, text, "File already exists")

		status, body := gatewayWrite(t, ctx, env, http.MethodPut, writePublic, "/contents/new.txt",
			`{"message":"m","content":"bG9zdAo=","branch":"`+branch+`"}`)
		if status != http.StatusUnprocessableEntity {
			t.Errorf("status = %d, body = %s", status, body)
		}
	})

	t.Run("directory path", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, "docs", "x", ""))
		wantToolError(t, result, text, "is a directory")
	})

	t.Run("missing branch", func(t *testing.T) {
		result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, "no-such-branch", "x.txt", "x", ""))
		wantToolError(t, result, text, "404")
		if branchExists(t, ctx, env, writePublic, "no-such-branch") {
			t.Error("a write created the missing branch")
		}
	})
}

func TestCreateOrUpdateFileSymlinks(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_WRITER_TOKEN"])
	branch := makeBranch(t, ctx, session, writePublic, "links")
	const link = "link-to-readme"
	file, _ := readStoredFile(t, ctx, env, writePublic, branch, link)
	if file.Type != "symlink" || file.Target != "README.md" {
		t.Fatalf("fixture = %+v", file)
	}

	t.Run("blocked by default", func(t *testing.T) {
		before := writerBranchSHA(t, ctx, env, writePublic, branch)
		result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, link, "docs/guide.md", file.SHA))
		wantToolError(t, result, text, "symlink_write_requires_opt_in")
		if after := writerBranchSHA(t, ctx, env, writePublic, branch); after != before {
			t.Errorf("blocked write moved the branch to %s", after)
		}
	})

	t.Run("regular file passes the symlink check", func(t *testing.T) {
		guide, _ := readStoredFile(t, ctx, env, writePublic, branch, "docs/guide.md")
		result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, "docs/guide.md", "guide v2\n", guide.SHA))
		if result.IsError {
			t.Fatalf("update failed: %s", text)
		}
	})

	// Known gap: Gitea cannot retarget a symlink through its API. The write must
	// fail clearly and leave the link untouched.
	t.Run("allow_symlink_write is refused safely", func(t *testing.T) {
		before := writerBranchSHA(t, ctx, env, writePublic, branch)
		args := writeArgs(writePublic, branch, link, "docs/guide.md", file.SHA)
		args["allow_symlink_write"] = true
		result, text := callTool(t, ctx, session, "create_or_update_file", args)
		wantToolError(t, result, text, "not supported")
		if after := writerBranchSHA(t, ctx, env, writePublic, branch); after != before {
			t.Errorf("refused write moved the branch to %s", after)
		}
		unchanged, _ := readStoredFile(t, ctx, env, writePublic, branch, link)
		if unchanged.Type != "symlink" || unchanged.Target != "README.md" {
			t.Errorf("link changed to %+v", unchanged)
		}
	})
}

func TestWriteAccessControl(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	owner := connect(t, ctx, env, env["FORGE_TEST_WRITER_TOKEN"])
	branch := makeBranch(t, ctx, owner, writePublic, "access")
	privateBranch := makeBranch(t, ctx, owner, writePrivate, "access")

	for _, tc := range []struct {
		name, token, repo, branch, status string
	}{
		{"read-only token", "FORGE_TEST_WRITER_READONLY_TOKEN", writePublic, branch, "403"},
		{"outsider on public repo", "FORGE_TEST_OUTSIDER_WRITE_TOKEN", writePublic, branch, "403"},
		{"outsider on private repo", "FORGE_TEST_OUTSIDER_WRITE_TOKEN", writePrivate, privateBranch, "404"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			session := connect(t, ctx, env, env[tc.token])
			before := writerBranchSHA(t, ctx, env, tc.repo, tc.branch)

			result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(tc.repo, tc.branch, "denied.txt", "x", ""))
			wantToolError(t, result, text, tc.status)
			if after := writerBranchSHA(t, ctx, env, tc.repo, tc.branch); after != before {
				t.Errorf("denied write moved the branch to %s", after)
			}

			denied := uniqueName(t, "denied")
			result, text = callTool(t, ctx, session, "create_branch", map[string]any{
				"owner": writerOwner, "repo": tc.repo, "branch": denied, "from_branch": tc.branch,
			})
			wantToolError(t, result, text, tc.status)
			if branchExists(t, ctx, env, tc.repo, denied) {
				t.Error("denied caller created a branch")
			}
		})
	}

	t.Run("protected branch", func(t *testing.T) {
		before := writerBranchSHA(t, ctx, env, writePublic, "locked")
		result, text := callTool(t, ctx, owner, "create_or_update_file", writeArgs(writePublic, "locked", uniqueName(t, "denied")+".txt", "x", ""))
		wantToolError(t, result, text, "403")
		if after := writerBranchSHA(t, ctx, env, writePublic, "locked"); after != before {
			t.Errorf("protected branch moved to %s", after)
		}
	})
}

func TestBranchAndCommitSurviveGitClone(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_WRITER_TOKEN"])
	branch := makeBranch(t, ctx, session, writePublic, "agent/clone")
	const path, content = "src/generated é.txt", "line one\nligne deux ✓\n"

	result, text := callTool(t, ctx, session, "create_or_update_file", writeArgs(writePublic, branch, path, content, ""))
	if result.IsError {
		t.Fatalf("write failed: %s", text)
	}

	// The token travels over stdin so it never appears in a process list.
	script := `read -r header; dir="$(mktemp -d)"; trap 'rm -rf "$dir"' EXIT
git -c http.extraHeader="$header" clone -q --depth 1 --branch "$1" "$2" "$dir/repo" && cat "$dir/repo/$3"`
	cmd := exec.CommandContext(ctx, "docker", "compose", "--project-directory", "../deploy",
		"-f", "../deploy/docker-compose.yml", "exec", "-T", "-u", "git", "gitea", "sh", "-c", script, "clone",
		branch, "http://localhost:3000/"+writerOwner+"/"+writePublic+".git", path)
	cmd.Stdin = strings.NewReader("Authorization: token " + env["FORGE_TEST_WRITER_TOKEN"] + "\n")
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("git clone: %v", err)
	}
	if string(output) != content {
		t.Errorf("cloned bytes = %q, want %q", output, content)
	}
}
