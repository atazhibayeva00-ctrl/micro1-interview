#!/usr/bin/env bash
# Idempotently seeds a test user, repositories, and token into deploy/.env.
set -euo pipefail
umask 077

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$DEPLOY_DIR/.env"
SEED_USER="forge-test"
SEED_EMAIL="forge-test@example.invalid"
OTHER_USER="forge-other"
WRITER_USER="forge-writer"

touch "$ENV_FILE"

env_get() { sed -n "s/^$1=//p" "$ENV_FILE" | tail -1; }
env_set() {
  local temp
  temp="$(mktemp "$ENV_FILE.XXXXXX")"
  grep -v "^$1=" "$ENV_FILE" > "$temp" || true
  printf '%s=%s\n' "$1" "$2" >> "$temp"
  mv "$temp" "$ENV_FILE"
}

compose() { docker compose --project-directory "$DEPLOY_DIR" -f "$DEPLOY_DIR/docker-compose.yml" "$@"; }
gitea_cli() { compose exec -T -u git gitea gitea "$@"; }

PASSWORD="$(env_get FORGE_TEST_PASSWORD)"
if [[ -z "$PASSWORD" ]]; then
  PASSWORD="$(openssl rand -hex 24)"
  env_set FORGE_TEST_PASSWORD "$PASSWORD"
fi

if ! gitea_cli admin user list | awk '{print $2}' | grep -qx "$SEED_USER"; then
  gitea_cli admin user create --username "$SEED_USER" --email "$SEED_EMAIL" \
    --password "$PASSWORD" --must-change-password=false >/dev/null
fi

create_repo() {
  local status
  status="$(printf 'user = "%s:%s"\n' "$SEED_USER" "$PASSWORD" | compose exec -T gitea curl -sS -K - \
    -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
    -d "{\"name\":\"$1\",\"private\":$2}" http://localhost:3000/api/v1/user/repos)"
  [[ "$status" == "201" || "$status" == "409" ]] || { echo "failed to create $1: HTTP $status" >&2; exit 1; }
}
create_repo seed-public false
create_repo seed-private true

SEED_USER="$SEED_USER" SEED_PASSWORD="$PASSWORD" compose exec -T -u git \
  -e SEED_USER -e SEED_PASSWORD gitea sh -s < "$DEPLOY_DIR/seed_fixtures.sh"

set_default_branch() {
  printf 'user = "%s:%s"\n' "$SEED_USER" "$PASSWORD" | compose exec -T gitea curl -fsS -K - \
    -o /dev/null -X PATCH -H 'Content-Type: application/json' \
    -d "{\"default_branch\":\"$2\"}" "http://localhost:3000/api/v1/repos/$SEED_USER/$1"
}
set_default_branch seed-public main
set_default_branch seed-private trunk

# A second user proves that private repositories stay hidden from non-collaborators.
OTHER_PASSWORD="$(env_get FORGE_TEST_OTHER_PASSWORD)"
if [[ -z "$OTHER_PASSWORD" ]]; then
  OTHER_PASSWORD="$(openssl rand -hex 24)"
  env_set FORGE_TEST_OTHER_PASSWORD "$OTHER_PASSWORD"
fi
if ! gitea_cli admin user list | awk '{print $2}' | grep -qx "$OTHER_USER"; then
  gitea_cli admin user create --username "$OTHER_USER" --email "$OTHER_USER@example.invalid" \
    --password "$OTHER_PASSWORD" --must-change-password=false >/dev/null
fi

# Write tests use their own user and repositories, so they cannot change the
# read fixtures or forge-test's repository counts.
WRITER_PASSWORD="$(env_get FORGE_TEST_WRITER_PASSWORD)"
if [[ -z "$WRITER_PASSWORD" ]]; then
  WRITER_PASSWORD="$(openssl rand -hex 24)"
  env_set FORGE_TEST_WRITER_PASSWORD "$WRITER_PASSWORD"
fi
if ! gitea_cli admin user list | awk '{print $2}' | grep -qx "$WRITER_USER"; then
  gitea_cli admin user create --username "$WRITER_USER" --email "$WRITER_USER@example.invalid" \
    --password "$WRITER_PASSWORD" --must-change-password=false >/dev/null
fi

writer_api() {
  printf 'user = "%s:%s"\n' "$WRITER_USER" "$WRITER_PASSWORD" | compose exec -T gitea curl -sS -K - \
    -o /dev/null -w '%{http_code}' -H 'Content-Type: application/json' "$@"
}
for spec in write-public:false write-private:true; do
  status="$(writer_api -X POST -d "{\"name\":\"${spec%%:*}\",\"private\":${spec##*:},\"default_branch\":\"main\"}" \
    http://localhost:3000/api/v1/user/repos)"
  [[ "$status" == "201" || "$status" == "409" ]] || { echo "failed to create ${spec%%:*}: HTTP $status" >&2; exit 1; }
done

SEED_USER="$WRITER_USER" SEED_PASSWORD="$WRITER_PASSWORD" compose exec -T -u git \
  -e SEED_USER -e SEED_PASSWORD gitea sh -s < "$DEPLOY_DIR/seed_write_fixtures.sh"

protection="http://localhost:3000/api/v1/repos/$WRITER_USER/write-public/branch_protections"
if [[ "$(writer_api "$protection/locked")" != "200" ]]; then
  status="$(writer_api -X POST -d '{"rule_name":"locked","enable_push":false}' "$protection")"
  [[ "$status" == "201" ]] || { echo "failed to protect branch: HTTP $status" >&2; exit 1; }
fi

printf 'user = "%s:%s"\n' "$SEED_USER" "$PASSWORD" | compose exec -T gitea curl -fsS -K - \
  -o /dev/null -X PATCH -H 'Content-Type: application/json' \
  -d '{"full_name":"Forge Test","description":"Seed user for conformance tests","location":"Test City","website":"https://example.invalid"}' \
  http://localhost:3000/api/v1/user/settings

# Pull request fixtures live in their own repositories, so other slices' counts
# and branches stay unchanged.
for spec in pr-fixtures:false pr-private:true; do
  status="$(writer_api -X POST -d "{\"name\":\"${spec%%:*}\",\"private\":${spec##*:},\"default_branch\":\"main\"}" \
    http://localhost:3000/api/v1/user/repos)"
  [[ "$status" == "201" || "$status" == "409" ]] || { echo "failed to create ${spec%%:*}: HTTP $status" >&2; exit 1; }
done

PULLS="http://localhost:3000/api/v1/repos/$WRITER_USER/pr-fixtures"
PUSH_GONE_HEAD=0
[[ "$(writer_api "$PULLS/pulls/1")" == "200" ]] || PUSH_GONE_HEAD=1
PUSH_GONE_HEAD="$PUSH_GONE_HEAD" SEED_USER="$WRITER_USER" SEED_PASSWORD="$WRITER_PASSWORD" compose exec -T -u git \
  -e PUSH_GONE_HEAD -e SEED_USER -e SEED_PASSWORD gitea sh -s < "$DEPLOY_DIR/seed_pull_fixtures.sh"

# as_user USER PASSWORD METHOD URL [JSON] prints the response and fails on an error.
as_user() {
  local args=(-fsS -K - -X "$3" -H 'Content-Type: application/json')
  [[ $# -ge 5 ]] && args+=(-d "$5")
  printf 'user = "%s:%s"\n' "$1" "$2" | compose exec -T gitea curl "${args[@]}" "$4"
}
as_writer() { as_user "$WRITER_USER" "$WRITER_PASSWORD" "$@"; }
as_reviewer() { as_user "$SEED_USER" "$PASSWORD" "$@"; }
json_field() { python3 -I -c 'import json,sys; print(eval(sys.argv[1], {"d": json.load(sys.stdin)}))' "$1"; }

as_writer PUT "$PULLS/collaborators/$SEED_USER" '{"permission":"write"}' >/dev/null
as_writer PUT "$PULLS/collaborators/$OTHER_USER" '{"permission":"read"}' >/dev/null
fork_status="$(printf 'user = "%s:%s"\n' "$OTHER_USER" "$OTHER_PASSWORD" | compose exec -T gitea curl -sS -K - \
  -o /dev/null -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d '{}' "$PULLS/forks")"
[[ "$fork_status" == "202" || "$fork_status" == "409" ]] || { echo "failed to fork: HTTP $fork_status" >&2; exit 1; }

# Reviews and comments are not idempotent, so they are created once with PR 1.
if [[ "$PUSH_GONE_HEAD" == "1" ]]; then
  as_writer POST "$PULLS/pulls" \
    '{"title":"Fixture review PR","body":"Fixture body","head":"feature/review","base":"main"}' >/dev/null
  head_sha="$(as_writer GET "$PULLS/pulls/1" | json_field 'd["head"]["sha"]')"
  as_writer POST "$PULLS/statuses/$head_sha" \
    '{"context":"ci/build","state":"success","description":"built","target_url":"https://example.invalid/build"}' >/dev/null
  as_writer POST "$PULLS/statuses/$head_sha" '{"context":"ci/test","state":"pending"}' >/dev/null
  as_writer POST "$PULLS/statuses/$head_sha" '{"context":"ci/lint","state":"failure"}' >/dev/null

  review_id="$(as_reviewer POST "$PULLS/pulls/1/reviews" '{"event":"COMMENT","body":"Fixture review","comments":[{"path":"feature.txt","new_position":2,"body":"First thread"},{"path":"README.md","new_position":2,"body":"Second thread"}]}' | json_field 'd["id"]')"
  root_id="$(as_writer GET "$PULLS/pulls/1/reviews/$review_id/comments" | json_field '[c for c in d if c["path"]=="feature.txt"][0]["id"]')"
  as_writer POST "$PULLS/pulls/1/comments/$root_id/replies" '{"body":"Reply in first thread"}' >/dev/null
  as_writer POST "$PULLS/pulls/comments/$root_id/resolve" >/dev/null
  as_reviewer POST "$PULLS/pulls/1/reviews" '{"event":"APPROVED","body":"Looks good"}' >/dev/null
  as_writer POST "$PULLS/pulls/1/requested_reviewers" "{\"reviewers\":[\"$OTHER_USER\"]}" >/dev/null
  as_writer POST "$PULLS/issues/1/comments" '{"body":"First general comment"}' >/dev/null
  as_reviewer POST "$PULLS/issues/1/comments" '{"body":"Second general comment"}' >/dev/null

  as_writer POST "$PULLS/pulls" '{"title":"Deleted head PR","head":"gone-head","base":"main"}' >/dev/null
  as_writer DELETE "$PULLS/branches/gone-head" >/dev/null
  as_writer POST "http://localhost:3000/api/v1/repos/$WRITER_USER/pr-private/pulls" \
    '{"title":"Private PR","head":"private-work","base":"main"}' >/dev/null
fi

token_valid() {
  [[ -n "$1" ]] && printf 'Authorization: token %s\n' "$1" | \
    compose exec -T gitea curl -fsS -o /dev/null -H @- http://localhost:3000/api/v1/user 2>/dev/null
}

# ensure_token ENV_KEY NAME_PREFIX SCOPES [USER] keeps a working token or creates one.
ensure_token() {
  local token
  token="$(env_get "$1")"
  if ! token_valid "$token"; then
    token="$(gitea_cli admin user generate-access-token --username "${4:-$SEED_USER}" \
      --token-name "$2-$(date +%s)" --scopes "$3" --raw | tr -d '\r\n')"
    env_set "$1" "$token"
  fi
}
ensure_token FORGE_TEST_TOKEN seed-full read:user,read:repository,read:issue
ensure_token FORGE_TEST_NARROW_TOKEN seed-narrow read:user
ensure_token FORGE_TEST_OTHER_TOKEN seed-other read:user,read:repository,read:issue "$OTHER_USER"
ensure_token FORGE_TEST_WRITER_TOKEN seed-writer read:user,write:repository "$WRITER_USER"
ensure_token FORGE_TEST_WRITER_READONLY_TOKEN seed-writer-ro read:user,read:repository "$WRITER_USER"
ensure_token FORGE_TEST_OUTSIDER_WRITE_TOKEN seed-outsider read:user,write:repository
ensure_token FORGE_TEST_PR_TOKEN seed-pr read:user,write:repository,read:issue "$WRITER_USER"
ensure_token FORGE_TEST_PR_READONLY_TOKEN seed-pr-ro read:user,read:repository,read:issue "$WRITER_USER"
ensure_token FORGE_TEST_FORK_TOKEN seed-fork read:user,write:repository,read:issue "$OTHER_USER"
ensure_token FORGE_TEST_PR_OUTSIDER_TOKEN seed-pr-outsider read:user,read:repository,read:issue

echo "Seed complete for user $SEED_USER."
