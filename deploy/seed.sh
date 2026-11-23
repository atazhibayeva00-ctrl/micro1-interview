#!/usr/bin/env bash
# Idempotently seeds a test user, repositories, and token into deploy/.env.
set -euo pipefail
umask 077

DEPLOY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="$DEPLOY_DIR/.env"
SEED_USER="forge-test"
SEED_EMAIL="forge-test@example.invalid"

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

printf 'user = "%s:%s"\n' "$SEED_USER" "$PASSWORD" | compose exec -T gitea curl -fsS -K - \
  -o /dev/null -X PATCH -H 'Content-Type: application/json' \
  -d '{"full_name":"Forge Test","description":"Seed user for conformance tests","location":"Test City","website":"https://example.invalid"}' \
  http://localhost:3000/api/v1/user/settings

token_valid() {
  [[ -n "$1" ]] && printf 'Authorization: token %s\n' "$1" | \
    compose exec -T gitea curl -fsS -o /dev/null -H @- http://localhost:3000/api/v1/user 2>/dev/null
}

# ensure_token ENV_KEY NAME_PREFIX SCOPES keeps a working token or creates one.
ensure_token() {
  local token
  token="$(env_get "$1")"
  if ! token_valid "$token"; then
    token="$(gitea_cli admin user generate-access-token --username "$SEED_USER" \
      --token-name "$2-$(date +%s)" --scopes "$3" --raw | tr -d '\r\n')"
    env_set "$1" "$token"
  fi
}
ensure_token FORGE_TEST_TOKEN seed-full read:user,read:repository
ensure_token FORGE_TEST_NARROW_TOKEN seed-narrow read:user

echo "Seed complete for user $SEED_USER."
