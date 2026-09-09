#!/usr/bin/env bash
set -euo pipefail

FENCE_TARGET="${FENCE_TARGET:?set FENCE_TARGET to the failed node BMC system ID}"
REDFISH_ENDPOINT="${REDFISH_ENDPOINT:?set REDFISH_ENDPOINT to the BMC Redfish Systems URL}"
REDFISH_USERNAME="${REDFISH_USERNAME:-}"
REDFISH_PASSWORD_FILE="${REDFISH_PASSWORD_FILE:-}"
CONFIRM_FENCE="${CONFIRM_FENCE:-}"
FENCE_REASON="${FENCE_REASON:?set FENCE_REASON to node-lost, network-partition, or storage-split}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-120}"
REDFISH_CA_BUNDLE="${REDFISH_CA_BUNDLE:-}"
ALLOW_INSECURE_TLS="${ALLOW_INSECURE_TLS:-false}"
FENCE_DRY_RUN="${FENCE_DRY_RUN:-false}"
CURL_CONNECT_TIMEOUT_SECONDS="${CURL_CONNECT_TIMEOUT_SECONDS:-10}"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

case "${FENCE_REASON}" in
  node-lost|network-partition|storage-split) ;;
  *) die "FENCE_REASON must be node-lost, network-partition, or storage-split" ;;
esac

case "${REDFISH_ENDPOINT}" in
  https://*) ;;
  *) die "REDFISH_ENDPOINT must use https" ;;
esac

test "${CONFIRM_FENCE}" = "${FENCE_TARGET}" ||
  die "set CONFIRM_FENCE exactly to FENCE_TARGET before power fencing"

if [ "${FENCE_DRY_RUN}" = "true" ]; then
  printf 'Would Redfish ForceOff %s for %s via %s.\n' \
    "${FENCE_TARGET}" "${FENCE_REASON}" "${REDFISH_ENDPOINT%/}/${FENCE_TARGET}"
  exit 0
fi

test -n "${REDFISH_USERNAME}" || die "set REDFISH_USERNAME from a protected secret"
test -n "${REDFISH_PASSWORD_FILE}" || die "set REDFISH_PASSWORD_FILE to a protected secret file"
test -r "${REDFISH_PASSWORD_FILE}" || die "REDFISH_PASSWORD_FILE is not readable"

password="$(cat "${REDFISH_PASSWORD_FILE}")"
system_url="${REDFISH_ENDPOINT%/}/${FENCE_TARGET}"
curl_args=(--fail --silent --show-error --connect-timeout "${CURL_CONNECT_TIMEOUT_SECONDS}" --user "${REDFISH_USERNAME}:${password}")

if [ -n "${REDFISH_CA_BUNDLE}" ]; then
  test -r "${REDFISH_CA_BUNDLE}" || die "REDFISH_CA_BUNDLE is not readable"
  curl_args+=(--cacert "${REDFISH_CA_BUNDLE}")
elif [ "${ALLOW_INSECURE_TLS}" = "true" ]; then
  curl_args+=(--insecure)
fi

curl "${curl_args[@]}" \
  --request POST \
  --header 'Content-Type: application/json' \
  --data '{"ResetType":"ForceOff"}' \
  "${system_url}/Actions/ComputerSystem.Reset"

deadline=$(( $(date +%s) + TIMEOUT_SECONDS ))
while [ "$(date +%s)" -lt "${deadline}" ]; do
  state="$(curl "${curl_args[@]}" "${system_url}" | jq -r '.PowerState // empty')"
  [ "${state}" = Off ] && {
    printf 'Fenced %s for %s.\n' "${FENCE_TARGET}" "${FENCE_REASON}"
    exit 0
  }
  sleep 5
done

die "Redfish did not report PowerState=Off before timeout"
