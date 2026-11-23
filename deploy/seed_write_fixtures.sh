#!/bin/sh
# Runs inside the Gitea container. Fixed identities and dates keep every commit
# SHA stable, so pushing again on a reseed changes nothing, even on the
# protected branch. Write tests only ever commit to branches they create.
set -eu

export GIT_TERMINAL_PROMPT=0
export GIT_AUTHOR_NAME="Forge Writer" GIT_AUTHOR_EMAIL="forge-writer@example.invalid"
export GIT_COMMITTER_NAME="Forge Writer" GIT_COMMITTER_EMAIL="forge-writer@example.invalid"
export GIT_AUTHOR_DATE="2026-01-01T00:00:00Z" GIT_COMMITTER_DATE="2026-01-01T00:00:00Z"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
cd "$work"

git init -q -b main .
mkdir docs
printf 'writer fixture\n' > README.md
printf 'guide\n' > docs/guide.md
ln -s README.md link-to-readme
git add -A
git commit -q -m "Add write fixtures"
git branch locked

# "feature" is a prefix of "feature-2", and the two point at different commits.
git checkout -q -b feature
printf 'feature\n' > README.md
git add README.md
git commit -q -m "Feature branch"
git checkout -q -b feature-2 main
printf 'feature 2\n' > README.md
git add README.md
git commit -q -m "Feature 2 branch"
git checkout -q main

for repo in write-public write-private; do
  git -c credential.helper='!f() { echo "username=$SEED_USER"; echo "password=$SEED_PASSWORD"; }; f' \
    push -q "http://localhost:3000/$SEED_USER/$repo.git" 'refs/heads/*:refs/heads/*'
done
