//go:build conformance

package conformance

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func gatewayUser(t *testing.T, ctx context.Context, env map[string]string, token string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		"http://127.0.0.1:"+env["GATEWAY_PORT"]+"/api/v3/user", nil)
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
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("gateway response is not JSON: %q", raw)
	}
	return resp.StatusCode, body
}

func compose(ctx context.Context, args ...string) ([]byte, error) {
	base := []string{"compose", "--project-directory", "../deploy", "-f", "../deploy/docker-compose.yml"}
	return exec.CommandContext(ctx, "docker", append(base, args...)...).CombinedOutput()
}

// The MCP server refuses to start without a token, so this checks the gateway.
func TestGatewayMissingToken(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	status, body := gatewayUser(t, ctx, env, "")
	if status != http.StatusUnauthorized || body["message"] != "Requires authentication" {
		t.Fatalf("status = %d, body = %v", status, body)
	}
}

func TestGetMeNarrowTokenOmitsRepoCounts(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	token := env["FORGE_TEST_NARROW_TOKEN"]
	if token == "" {
		t.Fatal("FORGE_TEST_NARROW_TOKEN is missing (run deploy/seed.sh)")
	}

	status, body := gatewayUser(t, ctx, env, token)
	if status != http.StatusOK || body["login"] != "forge-test" {
		t.Fatalf("status = %d, body = %v", status, body)
	}
	for _, key := range []string{"public_repos", "total_private_repos", "owned_private_repos"} {
		if _, present := body[key]; present {
			t.Errorf("%s exposed to a token without repository scope: %v", key, body[key])
		}
	}

	session := connect(t, ctx, env, token)
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_me", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	if text := resultText(t, result); result.IsError || !strings.Contains(text, `"login":"forge-test"`) {
		t.Fatalf("IsError = %v, text = %q", result.IsError, text)
	}
}

// This stops Gitea, so it must not run in parallel with other conformance tests.
func TestGetMeGiteaDown(t *testing.T) {
	ctx := testContext(t)
	env := loadEnv(t)
	session := connect(t, ctx, env, env["FORGE_TEST_TOKEN"])

	t.Cleanup(func() {
		restoreCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if output, err := compose(restoreCtx, "up", "-d", "--wait"); err != nil {
			t.Errorf("restore stack: %v\n%s", err, output)
		}
	})
	if output, err := compose(ctx, "stop", "gitea"); err != nil {
		t.Fatalf("stop Gitea: %v\n%s", err, output)
	}

	status, body := gatewayUser(t, ctx, env, env["FORGE_TEST_TOKEN"])
	if status != http.StatusBadGateway || body["message"] != "Bad Gateway" {
		t.Errorf("gateway status = %d, body = %v", status, body)
	}

	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_me", Arguments: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	text := resultText(t, result)
	if !result.IsError || !strings.Contains(text, "502") {
		t.Errorf("IsError = %v, text = %q; want a 502 error", result.IsError, text)
	}
	if strings.Contains(text, "gitea:3000") {
		t.Errorf("internal address leaked: %q", text)
	}
}
