//go:build conformance

package conformance

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const mcpImage = "ghcr.io/github/github-mcp-server:v2.2.0@sha256:040c4e99810076035eb1d9beb4b4a96fcf589c43cefed6ff51a46a553eb4e174"

func loadEnv(t *testing.T) map[string]string {
	t.Helper()
	file, err := os.Open(filepath.Join("..", "deploy", ".env"))
	if err != nil {
		t.Fatalf("open deploy/.env (run deploy/seed.sh first): %v", err)
	}
	defer file.Close()
	values := map[string]string{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if key, value, ok := strings.Cut(scanner.Text(), "="); ok {
			values[key] = value
		}
	}
	if values["FORGE_TEST_TOKEN"] == "" {
		t.Fatal("FORGE_TEST_TOKEN is missing (run deploy/seed.sh)")
	}
	if values["GATEWAY_PORT"] == "" {
		values["GATEWAY_PORT"] = "8080"
	}
	return values
}

func connect(t *testing.T, ctx context.Context, env map[string]string, token string) *mcp.ClientSession {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "run", "-i", "--rm", "--network", "host",
		"-e", "GITHUB_PERSONAL_ACCESS_TOKEN",
		"-e", "GITHUB_HOST=http://127.0.0.1:"+env["GATEWAY_PORT"],
		"-e", "GITHUB_TOOLS=get_me",
		mcpImage, "stdio")
	cmd.Env = append(os.Environ(), "GITHUB_PERSONAL_ACCESS_TOKEN="+token)
	client := mcp.NewClient(&mcp.Implementation{Name: "conformance", Version: "1"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		t.Fatalf("connect to MCP server: %v", err)
	}
	t.Cleanup(func() { session.Close() })
	return session
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func toMap(t *testing.T, value any) map[string]any {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func resultText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var parts []string
	for _, content := range result.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			parts = append(parts, text.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func giteaUser(t *testing.T, ctx context.Context, token string) map[string]any {
	t.Helper()
	cmd := exec.CommandContext(ctx, "docker", "compose", "--project-directory", "../deploy",
		"-f", "../deploy/docker-compose.yml", "exec", "-T", "gitea",
		"curl", "-fsS", "-H", "@-", "http://localhost:3000/api/v1/user")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("Authorization: token %s\n", token))
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("read user directly from Gitea: %v", err)
	}
	var user map[string]any
	if err := json.Unmarshal(output, &user); err != nil {
		t.Fatal(err)
	}
	return user
}

func TestToolsListMatchesSnapshot(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 1 || tools.Tools[0].Name != "get_me" {
		names := make([]string, 0, len(tools.Tools))
		for _, tool := range tools.Tools {
			names = append(names, tool.Name)
		}
		t.Fatalf("exposed tools = %v, want only get_me", names)
	}

	data, err := os.ReadFile(filepath.Join("toolsnaps", "get_me.snap"))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	got := toMap(t, tools.Tools[0])
	for _, field := range []string{"name", "description", "inputSchema", "outputSchema"} {
		if !reflect.DeepEqual(got[field], snapshot[field]) {
			t.Errorf("%s differs from snapshot\n got: %v\nwant: %v", field, got[field], snapshot[field])
		}
	}
}

func TestGetMeReturnsGiteaUser(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_me", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if result.IsError {
		t.Fatalf("get_me failed: %s", text)
	}
	var me struct {
		Login      string `json:"login"`
		ID         int64  `json:"id"`
		ProfileURL string `json:"profile_url"`
		AvatarURL  string `json:"avatar_url"`
		Details    struct {
			Name              string `json:"name"`
			Bio               string `json:"bio"`
			Location          string `json:"location"`
			Blog              string `json:"blog"`
			Email             string `json:"email"`
			PublicRepos       int    `json:"public_repos"`
			TotalPrivateRepos int    `json:"total_private_repos"`
			OwnedPrivateRepos int    `json:"owned_private_repos"`
			CreatedAt         string `json:"created_at"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(text), &me); err != nil {
		t.Fatalf("decode %q: %v", text, err)
	}

	want := giteaUser(t, ctx, env["FORGE_TEST_TOKEN"])
	if me.Login != want["login"] || float64(me.ID) != want["id"] || me.ProfileURL != want["html_url"] ||
		me.AvatarURL != want["avatar_url"] || me.Details.Email != want["email"] {
		t.Errorf("identity mismatch: got %+v, Gitea %v", me, want)
	}
	if want["full_name"] == "" || want["description"] == "" || want["location"] == "" || want["website"] == "" {
		t.Fatalf("seed user profile is not populated: %v", want)
	}
	if me.Details.Name != want["full_name"] || me.Details.Bio != want["description"] ||
		me.Details.Location != want["location"] || me.Details.Blog != want["website"] {
		t.Errorf("profile mismatch: got %+v, Gitea %v", me.Details, want)
	}
	wantCreated, err := time.Parse(time.RFC3339, want["created"].(string))
	if err != nil {
		t.Fatal(err)
	}
	gotCreated, err := time.Parse(time.RFC3339, me.Details.CreatedAt)
	if err != nil || !gotCreated.Equal(wantCreated) {
		t.Errorf("created_at = %q, want %v (%v)", me.Details.CreatedAt, wantCreated, err)
	}
	if me.Details.PublicRepos != 1 || me.Details.TotalPrivateRepos != 1 || me.Details.OwnedPrivateRepos != 1 {
		t.Errorf("repo counts = %d public, %d total private, %d owned private; want 1 each",
			me.Details.PublicRepos, me.Details.TotalPrivateRepos, me.Details.OwnedPrivateRepos)
	}
}

func TestGetMeInvalidToken(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, "invalid-token")

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_me", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if !result.IsError || !strings.Contains(text, "401") || !strings.Contains(text, "Bad credentials") {
		t.Fatalf("IsError = %v, text = %q; want a 401 Bad credentials error", result.IsError, text)
	}
}
