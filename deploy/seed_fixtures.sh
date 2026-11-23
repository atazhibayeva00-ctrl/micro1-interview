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
