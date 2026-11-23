#!/usr/bin/env bash
# Full verification from a clean stack. Used locally and in CI.
# WARNING: deletes this Compose project's Gitea test data.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ENV_FILE="$ROOT/deploy/.env"
cd "$ROOT"

compose() { docker compose --project-directory deploy -f deploy/docker-compose.yml "$@"; }
on_failure() { compose logs --no-color --tail=80 gateway gitea || true; }
trap 'status=$?; if [[ $status -ne 0 ]]; then on_failure; fi' EXIT

(umask 077; touch "$ENV_FILE")

unformatted="$(gofmt -l cmd internal conformance)"
if [[ -n "$unformatted" ]]; then
  echo "Run gofmt on: $unformatted" >&2
  exit 1
fi
go vet ./...
go vet -tags conformance ./...
go test -race -count=1 ./...

compose down -v --remove-orphans
sed -i '/^FORGE_TEST_/d' "$ENV_FILE"
compose up -d --build --wait

gateway_port="$(sed -n 's/^GATEWAY_PORT=//p' "$ENV_FILE" | tail -n 1)"
gateway_url="http://127.0.0.1:${gateway_port:-8080}"
ui_headers="$(curl -fsS -D - -o /dev/null "$gateway_url/ui/")"
if ! grep -qi '^content-security-policy: default-src .self.' <<<"$ui_headers"; then
  echo "The /ui/ page is missing its Content-Security-Policy header." >&2
  exit 1
fi
curl -fsS -o /dev/null "$gateway_url/ui/app.js"

./deploy/seed.sh
before="$(grep '^FORGE_TEST_' "$ENV_FILE" | sort | sha256sum)"
./deploy/seed.sh
after="$(grep '^FORGE_TEST_' "$ENV_FILE" | sort | sha256sum)"
if [[ "$before" != "$after" ]]; then
  echo "Seed is not idempotent: credentials changed on rerun." >&2
  exit 1
fi

go test -tags conformance -count=1 -p 1 -v ./conformance/
echo "Verification passed."
