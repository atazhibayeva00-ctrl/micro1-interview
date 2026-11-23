#!/bin/sh
# Runs inside the Gitea container. Fixed identities and dates keep every commit
# SHA stable, so pushing again on a reseed changes nothing.
set -eu

export GIT_TERMINAL_PROMPT=0
export GIT_AUTHOR_NAME="Forge Test" GIT_AUTHOR_EMAIL="forge-test@example.invalid"
export GIT_COMMITTER_NAME="Forge Test" GIT_COMMITTER_EMAIL="forge-test@example.invalid"
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

push() {
  git remote add origin "http://localhost:3000/$SEED_USER/$1.git"
  git -c credential.helper='!f() { echo "username=$SEED_USER"; echo "password=$SEED_PASSWORD"; }; f' \
    push -q --force origin 'refs/heads/*:refs/heads/*' 'refs/tags/*:refs/tags/*'
}

mkdir "$work/public" "$work/private"

cd "$work/public"
git init -q -b main .
mkdir -p src/pkg docs many
printf 'hello v1\n' > README.md
: > empty.txt
printf 'package pkg\n' > src/pkg/lib.go
printf 'spaced unicode\n' > 'docs/notes été.md'
printf '\000\001\002PNG\377\376' > logo.bin
head -c 1200000 /dev/zero | tr '\0' 'a' > large.txt
cp large.txt many/large.txt
i=1
while [ "$i" -le 1100 ]; do
  : > "many/f$i.txt"
  i=$((i + 1))
done
ln -s README.md link-to-readme
printf '[submodule "vendor/lib"]\n\tpath = vendor/lib\n\turl = https://example.invalid/lib.git\n' > .gitmodules
git add -A
git update-index --add --cacheinfo 160000,1111111111111111111111111111111111111111,vendor/lib
git commit -q -m "Add fixtures"
git tag v1-light
git tag -a v1-annotated -m "Annotated fixture tag"

# Stage only README.md: "commit -a" would delete the submodule entry, which has
# no checkout in this working tree.
printf 'hello forge\n' > README.md
git add README.md
git commit -q -m "Update README"

git checkout -q -b feature/login
printf 'hello login\n' > README.md
git add README.md
git commit -q -m "Login branch"
git checkout -q -b feature/login-v2 main
printf 'hello login v2\n' > README.md
git add README.md
git commit -q -m "Login v2 branch"
git checkout -q main

head="$(git rev-parse HEAD)"
i=1
while [ "$i" -le 60 ]; do
  git update-ref "refs/heads/bulk/$(printf '%03d' "$i")" "$head"
  i=$((i + 1))
done
push seed-public

cd "$work/private"
git init -q -b trunk .
printf 'private secret\n' > secret.txt
git add -A
git commit -q -m "Add private fixture"
push seed-private

# api METHOD REPO PATH [JSON] calls Gitea without putting credentials in argv.
api() {
  if [ "$#" -ge 4 ]; then
    printf 'user = "%s:%s"\n' "$SEED_USER" "$SEED_PASSWORD" | curl -fsS -K - -o /dev/null -X "$1" \
      -H 'Content-Type: application/json' -d "$4" "http://localhost:3000/api/v1/repos/$SEED_USER/$2$3"
  else
    printf 'user = "%s:%s"\n' "$SEED_USER" "$SEED_PASSWORD" | curl -fsS -K - -X "$1" \
      "http://localhost:3000/api/v1/repos/$SEED_USER/$2$3"
  fi
}

ensure_label() {
  if ! api GET "$1" /labels | grep -q "\"name\":\"$2\""; then
    api POST "$1" /labels "{\"name\":\"$2\",\"color\":\"$3\",\"description\":\"$4\"}"
  fi
}

issue_exists() { api GET "$1" "/issues/$2" >/dev/null 2>&1; }

# ensure_issue REPO NUMBER TITLE keeps issue numbers stable across reseeds.
ensure_issue() {
  if issue_exists "$1" "$2"; then
    return 1
  fi
  api POST "$1" /issues "{\"title\":\"$3\",\"body\":\"Body of issue $2\",\"assignees\":[\"$SEED_USER\"]}"
}

ensure_label seed-public bug d73a4a "Something is broken"
ensure_label seed-public enhancement a2eeef "New feature"

if ensure_issue seed-public 1 "First open issue"; then
  api POST seed-public /issues/1/labels '{"labels":["bug","enhancement"]}'
  for comment in one two three; do
    api POST seed-public /issues/1/comments "{\"body\":\"Comment $comment\"}"
  done
fi
if ensure_issue seed-public 2 "Closed issue"; then
  api POST seed-public /issues/2/labels '{"labels":["enhancement"]}'
  api PATCH seed-public /issues/2 '{"state":"closed"}'
fi
ensure_issue seed-public 3 "Third issue" || true
if ! issue_exists seed-public 4; then
  api POST seed-public /pulls '{"head":"feature/login","base":"main","title":"Fixture pull request"}'
fi
ensure_issue seed-public 5 "Fifth issue" || true
ensure_issue seed-public 6 "Sixth issue" || true
ensure_issue seed-private 1 "Private issue" || true
