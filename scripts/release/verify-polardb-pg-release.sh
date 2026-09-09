#!/usr/bin/env bash
set -euo pipefail

artifact_dir="${1:?usage: verify-polardb-pg-release.sh <artifact-dir>}"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

checksum_check() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum -c SHA256SUMS
  else
    shasum -a 256 -c SHA256SUMS
  fi
}

command -v helm >/dev/null 2>&1 || die "helm is required"
command -v tar >/dev/null 2>&1 || die "tar is required"
[[ -f "${artifact_dir}/SHA256SUMS" ]] || die "SHA256SUMS is missing"
[[ -f "${artifact_dir}/release-manifest.yaml" ]] || die "release manifest is missing"

(
  cd "$artifact_dir"
  checksum_check
)

shopt -s nullglob
chart_artifacts=(
  "${artifact_dir}"/polardb-pg-[0-9]*.tgz
  "${artifact_dir}"/polardb-pg-stack-ops-[0-9]*.tgz
)
assets_artifacts=("${artifact_dir}"/polardb-pg-production-assets-*.tgz)
(( ${#chart_artifacts[@]} == 2 )) || die "expected exactly two Helm Chart artifacts"
(( ${#assets_artifacts[@]} == 1 )) || die "expected exactly one production assets artifact"

while IFS= read -r image; do
  [[ "$image" == '$('*')' ]] && continue
  [[ "$image" == *@sha256:[0-9a-f]* ]] || die "rendered image is not digest-pinned: ${image}"
done < <(
  for chart in "${chart_artifacts[@]}"; do
    chart_name="$(helm show chart "$chart" | awk '$1 == "name:" { print $2; exit }')"
    helm template "$chart_name" "$chart" --namespace kb-system |
      sed -nE 's/^[[:space:]]*image:[[:space:]]*"?([^"[:space:]]+).*$/\1/p'
  done | sort -u
)

tar -tzf "${assets_artifacts[0]}" | grep -F 'examples/polardb-pg/scripts/run-restore-drill.sh' >/dev/null
tar -tzf "${assets_artifacts[0]}" | grep -F 'examples/polardb-pg/production/monitoring/victoria-metrics-kube-state-metrics-values.example.yaml' >/dev/null
tar -tzf "${assets_artifacts[0]}" | grep -F 'examples/polardb-pg-stack-ops/ops-fence.yaml' >/dev/null
tar -tzf "${assets_artifacts[0]}" | grep -F 'docs/polardb-pg-production-operations-zh.md' >/dev/null

printf 'Release artifacts verified: %s\n' "$artifact_dir"
