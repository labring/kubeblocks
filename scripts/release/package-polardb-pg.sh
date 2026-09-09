#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
release_version="${RELEASE_VERSION:-0.3.3}"
output_dir="${OUTPUT_DIR:-${repo_root}/dist/polardb-pg-v${release_version}}"
charts=(
  "deploy/addons/polardb-pg"
  "deploy/addons/polardb-pg-stack-ops"
)

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

checksum() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$@"
  else
    shasum -a 256 "$@"
  fi
}

cd "$repo_root"
command -v helm >/dev/null 2>&1 || die "helm is required"
[[ "$release_version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] ||
  die "RELEASE_VERSION must be a three-part SemVer without a v prefix"

rm -rf "$output_dir"
mkdir -p "$output_dir"

chart_artifacts=()
release_artifacts=()
images_file="${output_dir}/images.txt"
: >"$images_file"
chmod 0644 "$images_file"

for chart in "${charts[@]}"; do
  chart_version="$(awk '$1 == "version:" { print $2; exit }' "${chart}/Chart.yaml")"
  [[ "$chart_version" == "$release_version" ]] ||
    die "${chart}/Chart.yaml version ${chart_version} does not match RELEASE_VERSION ${release_version}"

  helm lint "$chart"
  rendered="$(mktemp)"
  helm template "$(basename "$chart")" "$chart" --namespace kb-system >"$rendered"
  while IFS= read -r image; do
    [[ -n "$image" ]] && printf '%s\n' "$image" >>"$images_file"
  done < <(sed -nE 's/^[[:space:]]*image:[[:space:]]*"?([^"[:space:]]+).*$/\1/p' "$rendered")
  rm -f "$rendered"

  artifact="$(helm package "$chart" --destination "$output_dir" | sed -nE 's/^Successfully packaged chart and saved it to: //p')"
  [[ -n "$artifact" && -f "$artifact" ]] || die "failed to package ${chart}"
  chart_artifact="$(basename "$artifact")"
  chart_artifacts+=("$chart_artifact")
  release_artifacts+=("$chart_artifact")
done

production_assets="polardb-pg-production-assets-${release_version}.tgz"
tar -C "$repo_root" -czf "${output_dir}/${production_assets}" \
  docs/polardb-pg-production-operations-zh.md \
  docs/polardb-pg-real-engine-zh.md \
  docs/polardb-pg-stack-ops-zh.md \
  examples/polardb-pg \
  examples/polardb-pg-stack-ops
release_artifacts+=("$production_assets")

sort -u "$images_file" -o "$images_file"
sed -i.bak '/^\$(/d' "$images_file"
rm -f "${images_file}.bak"
while IFS= read -r image; do
  [[ "$image" == *@sha256:[0-9a-f]* ]] || die "release image is not digest-pinned: ${image}"
done <"$images_file"

(
  cd "$output_dir"
  checksum "${release_artifacts[@]}" >SHA256SUMS
)

commit="$(git rev-parse HEAD)"
created_at="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
manifest="${output_dir}/release-manifest.yaml"
{
  printf 'apiVersion: release.kubeblocks.io/v1alpha1\n'
  printf 'kind: PolarDBPGRelease\n'
  printf 'metadata:\n  name: polardb-pg-v%s\n' "$release_version"
  printf 'spec:\n'
  printf '  createdAt: %s\n' "$created_at"
  printf '  sourceCommit: %s\n' "$commit"
  printf '  charts:\n'
  for artifact in "${chart_artifacts[@]}"; do
    digest="$(grep "  ${artifact}$" "${output_dir}/SHA256SUMS" | awk '{print $1}')"
    printf '    - file: %s\n      sha256: %s\n' "$artifact" "$digest"
  done
  assets_digest="$(grep "  ${production_assets}$" "${output_dir}/SHA256SUMS" | awk '{print $1}')"
  printf '  assets:\n    - file: %s\n      sha256: %s\n' \
    "$production_assets" "$assets_digest"
  printf '  images:\n'
  while IFS= read -r image; do
    printf '    - %s\n' "$image"
  done <"$images_file"
} >"$manifest"

(
  cd "$output_dir"
  checksum images.txt release-manifest.yaml >>SHA256SUMS
)

printf 'Created release artifacts in %s\n' "$output_dir"
printf 'Verify with: scripts/release/verify-polardb-pg-release.sh %s\n' "$output_dir"
