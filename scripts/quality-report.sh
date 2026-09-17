#!/usr/bin/env bash
set -uo pipefail

verify=0
race=0
output=""
while [[ $# -gt 0 ]]; do
  case "$1" in
    --verify) verify=1 ;;
    --race) race=1 ;;
    --output) shift; output="${1:-}" ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

cd "$(dirname "$0")/.."
export GOCACHE="${GOCACHE:-$PWD/.cache/go-build}"
mkdir -p "$GOCACHE"
packages=$(go list ./... 2>/dev/null | wc -l | tr -d ' ')
test_files=$(git ls-files '*_test.go' | wc -l | tr -d ' ')
go_tests=$(git ls-files '*_test.go' | xargs grep -hEc '^func Test[A-Za-z0-9_]+' 2>/dev/null | awk '{s+=$1} END {print s+0}')
benchmarks=$(git ls-files '*_test.go' | xargs grep -hEc '^func Benchmark[A-Za-z0-9_]+' 2>/dev/null | awk '{s+=$1} END {print s+0}')
python_tests=$(git ls-files '*.py' | xargs grep -hEc '^\s*(async\s+)?def test_' 2>/dev/null | awk '{s+=$1} END {print s+0}')
report=$(mktemp)
failed=0

{
  echo '# Prizm quality report'
  echo
  echo "Generated: $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  echo
  echo '| Metric | Verified value |'
  echo '|---|---:|'
  echo "| Go packages | $packages |"
  echo "| Go test files | $test_files |"
  echo "| Go tests | $go_tests |"
  echo "| Go benchmarks | $benchmarks |"
  echo "| Python tests | $python_tests |"
  echo
  echo '| Check | Status |'
  echo '|---|---|'
} > "$report"

run() {
  local name="$1"; shift
  if "$@" >/dev/null 2>&1; then
    echo "| $name | PASS |" >> "$report"
  else
    echo "| $name | FAIL |" >> "$report"
    failed=1
  fi
}

if [[ $verify -eq 1 ]]; then
  run 'gofmt' bash -c 'test -z "$(gofmt -l $(git ls-files "*.go"))"'
  run 'go vet ./...' go vet ./...
  run 'go build ./...' go build ./...
  run 'go test ./... -count=1' go test ./... -count=1
  run 'coverage gates' ./scripts/coverage-gate.sh --output coverage-gate.md
  [[ $race -eq 1 ]] && run 'go test -race ./... -count=1' go test -race ./... -count=1
else
  echo '| verification | SKIPPED (run with --verify) |' >> "$report"
fi

if [[ -z $(git status --porcelain --untracked-files=no) ]]; then
  echo '| tracked worktree clean | PASS |' >> "$report"
else
  echo '| tracked worktree clean | FAIL |' >> "$report"
fi

cat "$report"
[[ -n $output ]] && cp "$report" "$output"
rm -f "$report"
exit "$failed"
