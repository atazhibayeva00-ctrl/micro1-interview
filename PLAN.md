# GitHub-Compatible Forge: Plan and Context

This file is the source of truth for project plan and context. Read it first when resuming work.

**Current state:** Slices 1 and 2 are merged into `main`. Slice 3 is built and verified locally on branch `slice-3-write-code`, with one known gap for symlink updates. Slice 4 is next and needs its plan written and approved.

## Running locally
```bash
cp -n /dev/null deploy/.env   # then set the values below
# COMPOSE_PROJECT_NAME=forge-slice-N
# GATEWAY_PORT=<unique port>
# GITEA_PORT=<unique port>
./deploy/verify.sh            # deletes this project's Gitea test data, then runs all checks
```

CI runs the same script on every push and pull request. Every slice must keep it passing.

## Parallel worktrees
- Start each new slice from a branch containing all slices it depends on.
- Use a unique `COMPOSE_PROJECT_NAME`, `GATEWAY_PORT`, and `GITEA_PORT` in that worktree's gitignored `deploy/.env`. Slice 1 uses `forge-slice-1`, 8080, and 3000. Slice 2 uses `forge-slice-2`, 8082, and 3002. Slice 3 uses `forge-slice-3`, 8100, and 3020.
- Each session edits only its own slice's sections of this file and its own ledger rows, to keep merges simple.

## Goal
Build a self-hosted GitHub-like platform whose MCP interface matches the official `github/github-mcp-server`. The primary users are agents and API clients. Work proceeds in vertical slices, one feature at a time.

## Architecture
```
MCP client
   |
github-mcp-server v2.2.0 (unmodified, GITHUB_TOOLS=<finished tools>)
   |  /api/v3  /api/graphql  /api/uploads  /raw
Compat gateway (Go, this repo)
   |-- REST / GraphQL translation --> Gitea v28.4.0 (official release, unforked)
   |-- Extension services ----------> Postgres (added only when a slice needs it)
```

### Standing constraints
- No new web UI. Gateway-only features are available through API and MCP.
- Rules that must hold for direct `git push` are enforced at the Git layer and tested with real pushes.
- Gateway-only resources authorize against Gitea repository permissions on every request.
- Writes spanning Gitea and Postgres are idempotent and reconcilable.
- No speculative abstractions. Extract shared code when a second slice needs it.

## Working agreement
Every slice follows this loop:
1. Read the pinned MCP tool's schema and exact API calls, and check Gitea's behavior.
2. Write the slice plan here, mark it `awaiting approval`, and **stop for the user's approval**.
3. Write failing MCP-level tests for success and relevant error cases.
4. Implement only the gateway routes and mappers the slice needs.
5. Run this slice's tests and all earlier slices' tests.
6. Add the tools to the `GITHUB_TOOLS` allowlist.
7. Update [the ledger](conformance/ledger.yaml) and this file, then propose the next slice plan.

## Decision log
| Decision | Reason |
|---|---|
| Use Gitea as the backend | MIT licensed, Go, actively maintained, and has the broadest fit for core forge features. |
| Run Gitea unforked behind a gateway | Agents and APIs are the primary users, so cheap upgrades and a replaceable backend outweigh deeper UI integration. |
| Run the official MCP server unmodified | Tool names and schemas match by construction, and parity upgrades become a version bump plus conformance run. |
| Build in vertical slices | Each feature is usable and verified before the next begins. |
| User approves each slice plan | The user controls scope and order at every step. |
| Copilot and GitHub-proprietary tools are out of scope | They depend on GitHub-hosted services. |

## Roadmap
| # | Slice | Tools | Depends on | Status |
|---|---|---|---|---|
| 1 | Identity | `get_me` | none | done |
| 2 | Read code | `get_file_contents`, `list_branches` | 1 | done |
| 3 | Write code | `create_branch`, `create_or_update_file` (partial) | 2 | done, not yet merged |
| 4 | Read issues | `issue_read`, `list_issues` (first GraphQL) | 1 | planned |
| 5 | Write issues | `issue_write`, `add_issue_comment` | 4 | planned |
| 6 | Pull requests | `create_pull_request`, `pull_request_read` | 3, 4 | planned |
| 7 | Review and merge | `pull_request_review_write`, `merge_pull_request` | 6 | planned |

Later backlog: Actions, notifications, projects, gists, discussions, rulesets, security alerts.

## Current slice
None. Slice 4 needs a plan and user approval before any build work.

## Completed slices
### Slice 1: `get_me`
- **Delivered:** Compose stack, idempotent seed, gateway, `GET /api/v3/user`, GitHub-format errors, MCP conformance tests, and a ledger of 154 snapshot tools (1 passing, 3 out of scope, 150 pending).
- **Verified:** `deploy/verify.sh` passed from a clean stack: formatting, vet, 10 gateway test functions with the race detector, a seed rerun that kept credentials unchanged, and 6 conformance tests against the unmodified MCP server and real Gitea.
- **Not yet verified:** `.github/workflows/ci.yml` has not run on GitHub Actions or been linted.
- **Lesson:** Gitea builds URLs from the request host, so `PUBLIC_URL_DETECTION=never` and a fixed `ROOT_URL` are required to avoid leaking `gitea:3000`.

### Slice 2: `get_file_contents`, `list_branches`
- **Delivered:** Gateway routes for repository info, exact ref lookup, branches, contents, Git trees, and raw downloads, plus a required `PUBLIC_URL` setting, seeded code fixtures, and a second test user. The ledger now has 4 passing rows (3 tools and the `list_branches_output` schema snapshot), 3 out of scope, and 147 pending.
- **Verified:** `deploy/verify.sh` passed from a clean stack, including all 15 conformance tests for Slices 1 and 2 against the unmodified MCP server and real Gitea.
- **Not yet verified:** This branch has not run on GitHub Actions. Git LFS files are untested.
- **Lessons:** Gitea's ref lookup matches by prefix, its trees page at 1000 entries, and its default page limit of 50 is below GitHub's 100. The MCP server adds `x-mcp-header` to some schemas at runtime, so snapshot comparison ignores it.

### Slice 3: `create_branch`, `create_or_update_file`
- **Delivered:** Gateway routes for creating branches, writing files, and reading Git blobs, plus a `forge-writer` user with separate write fixtures. The ledger now has 5 passing rows, 1 partial, 3 out of scope, and 145 pending.
- **Verified:** `deploy/verify.sh` passed from a clean stack, with 6 new gateway test functions and all 20 conformance test functions against the unmodified MCP server and real Gitea. Coverage includes access control, a protected branch, and a real `git clone` of a committed file.
- **Not yet verified:** This branch has not run on GitHub Actions.
- **Lessons:** Gitea returns a bare 500 for an unknown source commit, reports a stale SHA as a 422 identified only by its message text, and ignores the raw media type on blobs. A conformance test guards the stale-SHA wording against Gitea upgrades.

## Open questions and known gaps
- `create_or_update_file` with `allow_symlink_write=true` returns a clear "not supported" error and changes nothing, because Gitea's API cannot retarget a symlink. Closing this needs a Git-level write path.
- File writes ignore client-supplied `author` and `committer`, so commits are attributed to the authenticated user.
- A branch literally named like a 40-character SHA could be ambiguous as a `create_branch` source in Gitea. This case is untested.
- Blobs too large for the gateway's 32 MiB Gitea response cap return 502.
- Symlinks return metadata and their target path, but not the target file's content.
- `git_url` is still null in contents responses, although the Git blobs route now exists.
- A `raw.<host>` subdomain must stay unconfigured on hosted deployments unless it is served correctly, because the MCP server probes it at startup and switches URLs if it responds.
- `get_me` has no Gitea source for `updated_at`, `company`, `hireable`, `twitter_username`, or gist counts. They return empty or zero values, and `updated_at` appears as the zero time.
- The gateway has no `node_id` mapping yet. Add it when a slice needs it.
