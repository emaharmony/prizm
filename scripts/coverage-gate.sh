#!/usr/bin/env bash
set -euo pipefail

minimum_aggregate=55.0
minimum_critical=80.0
output=coverage-gate.md
while [[ $# -gt 0 ]]; do
  case "$1" in
    --minimum-aggregate) shift; minimum_aggregate="$1" ;;
    --minimum-critical) shift; minimum_critical="$1" ;;
    --output) shift; output="$1" ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
  shift
done

cd "$(dirname "$0")/.."
mkdir -p test-results/coverage

total_coverage() {
  go tool cover "-func=$1" | awk '/^total:/ {gsub(/%/, "", $3); print $3}'
}

passes() { awk -v actual="$1" -v required="$2" 'BEGIN {exit !(actual + 0 >= required + 0)}'; }

covdata_dir="$PWD/test-results/coverage/covdata"
mkdir -p "$covdata_dir"
find "$covdata_dir" -maxdepth 1 -type f -delete

go test -coverpkg=./... ./... -count=1 -args "-test.gocoverdir=$covdata_dir"
coverage_binary="$PWD/test-results/coverage/prizm-cover"
go build -cover -coverpkg=./... -o "$coverage_binary" ./cmd/prizm-cli

smoke() { GOCOVERDIR="$covdata_dir" "$coverage_binary" "$@" >/dev/null; }
smoke --help
smoke version
smoke config --config prizm.yaml.example --json
smoke doctor --config prizm.yaml.example --json
smoke preview --workflow examples/workflows/gated-loop.yaml
smoke context show --workspace-root prizm-workspace.example --context soul
smoke workflow list
smoke workflow show demo.echo_tool
smoke workflow run demo.echo_tool --run-dir test-results/coverage/cli-runs
smoke agent list
smoke agent show planner
smoke tool list
smoke validation list
smoke validation run echo_test --project . --run-dir test-results/coverage/validation-runs
smoke policy list
smoke adapter list
smoke adapter show echo
smoke adapter health echo
smoke projection list
smoke skills --json
smoke mcp --config prizm.yaml.example --json
smoke scan --json
smoke search --query coverage --provider mock
smoke approval list
smoke status --config prizm.yaml.example

sample_dir=test-results/coverage/sample-data/run_01JR0SAMPLE0
mkdir -p "$sample_dir"
cp examples/runs/sample-run/* "$sample_dir/"
smoke cost -data test-results/coverage/sample-data run_01JR0SAMPLE0
smoke trace -data test-results/coverage/sample-data run_01JR0SAMPLE0

go tool covdata textfmt -i="$covdata_dir" -o=coverage.out
aggregate=$(total_coverage coverage.out)
failed=0
passes "$aggregate" "$minimum_aggregate" || failed=1

{
  echo '# Coverage gate'
  echo
  if passes "$aggregate" "$minimum_aggregate"; then status=PASS; else status=FAIL; fi
  echo "Aggregate: **${aggregate}%** (required: ${minimum_aggregate}%) — ${status}"
  echo
  echo '| Surface | Coverage | Required | Status |'
  echo '|---|---:|---:|---|'
} > "$output"

for package in safety policy approval mutation validation guard; do
  profile="test-results/coverage/${package}.out"
  go test "./internal/${package}" -coverprofile="$profile" -count=1 >/dev/null
  coverage=$(total_coverage "$profile")
  if passes "$coverage" "$minimum_critical"; then status=PASS; else status=FAIL; failed=1; fi
  echo "| internal/${package} | ${coverage}% | ${minimum_critical}% | ${status} |" >> "$output"
done

tool_profile=test-results/coverage/tool.out
go test ./internal/tool -coverprofile="$tool_profile" -count=1 >/dev/null
read -r covered statements < <(awk '
  /\/(executor|policy|registry|paths|path_resolve)\.go:/ {
    total += $2; if ($3 > 0) hit += $2
  }
  END {print hit + 0, total + 0}
' "$tool_profile")
[[ $statements -gt 0 ]] || { echo 'tool authority coverage matched no statements' >&2; exit 1; }
tool_authority=$(awk -v hit="$covered" -v total="$statements" 'BEGIN {printf "%.1f", 100 * hit / total}')
if passes "$tool_authority" "$minimum_critical"; then status=PASS; else status=FAIL; failed=1; fi
echo "| internal/tool authority files | ${tool_authority}% | ${minimum_critical}% | ${status} |" >> "$output"

cat "$output"
exit "$failed"
