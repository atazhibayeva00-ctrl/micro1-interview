#!/bin/sh
# Runs inside the Gitea container. Fixed identities and dates keep every commit
# SHA stable, so pushing again on a reseed changes nothing.
set -eu

export GIT_TERMINAL_PROMPT=0
export GIT_AUTHOR_NAME="Forge Writer" GIT_AUTHOR_EMAIL="forge-writer@example.invalid"
export GIT_COMMITTER_NAME="Forge Writer" GIT_COMMITTER_EMAIL="forge-writer@example.invalid"
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"

git init -q -b main .
printf 'pull request fixture\n' > README.md
printf 'remove me\n' > remove-me.txt
git add -A
git commit -q -m "Add pull request fixtures"
git branch no-changes

git checkout -q -b feature/review
printf 'line one\nline two\nline three\n' > feature.txt
git add feature.txt
git commit -q -m "Add feature file"
printf 'pull request fixture\nwith an update\n' > README.md
git rm -q remove-me.txt
git add README.md
git commit -q -m "Update README and remove file"

git checkout -q -b gone-head main
printf 'gone\n' > gone.txt
git add gone.txt
git commit -q -m "Add file on a branch that gets deleted"

git checkout -q -b private-work main
printf 'private change\n' > private.txt
git add private.txt
git commit -q -m "Add private change"
git checkout -q main

remote() {
  git -c credential.helper='!f() { echo "username=$SEED_USER"; echo "password=$SEED_PASSWORD"; }; f' \
    push -q "http://localhost:3000/$SEED_USER/$1.git" "$2"
}
remote pr-fixtures refs/heads/main:refs/heads/main
remote pr-fixtures refs/heads/no-changes:refs/heads/no-changes
remote pr-fixtures refs/heads/feature/review:refs/heads/feature/review
remote pr-private refs/heads/main:refs/heads/main
remote pr-private refs/heads/private-work:refs/heads/private-work

# gone-head is deleted after its pull request opens, so only push it once.
if [ "${PUSH_GONE_HEAD:-}" = "1" ]; then
  remote pr-fixtures refs/heads/gone-head:refs/heads/gone-head
fi
