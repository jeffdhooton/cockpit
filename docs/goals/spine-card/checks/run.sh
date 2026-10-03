#!/bin/sh
set -eu
# Offline, with a writable cache even in an isolated spine worktree.
export GOPROXY=off GOSUMDB=off
export GOCACHE="${GOCACHE:-${TMPDIR:-/tmp}/cockpit-spine-card-go-cache-$(id -u)}"
case "$1" in
  source) test_name=TestSource ;;
  tile) test_name=TestTile ;;
  preview) test_name=TestPreview ;;
  attention) test_name=TestAttention ;;
  enter) test_name=TestEnter ;;
  doctor) test_name=TestDoctor ;;
  harness) test_name=TestHarness ;;
  build|vet|test|quality)
    # Preserve installed tools except the three integrations forbidden by
    # the goal. Existing real-tmux integration tests skip when it is absent.
    tools=$(mktemp -d)
    trap 'rm -rf "$tools"' EXIT HUP INT TERM
    old_ifs=$IFS
    IFS=:
    for dir in $PATH; do
      IFS=$old_ifs
      for file in "$dir"/*; do
        [ -f "$file" ] && [ -x "$file" ] || continue
        name=${file##*/}
        case "$name" in spine|tmux|gh) continue ;; esac
        [ -e "$tools/$name" ] || ln -s "$file" "$tools/$name"
      done
      IFS=:
    done
    IFS=$old_ifs
    export PATH="$tools"
    case "$1" in
      build) go build ./... ;;
      vet) go vet ./... ;;
      test) go test ./... ;;
      quality) go build ./... && go vet ./... && go test ./... ;;
    esac
    exit
    ;;
  *) echo "unknown check: $1" >&2; exit 2 ;;
esac
go test ./docs/goals/spine-card/checks/testdata -count=1 -timeout=120s -run "^${test_name}$" -v
