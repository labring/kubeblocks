#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HELM="${HELM:-helm}"

manager_digest="sha256:1111111111111111111111111111111111111111111111111111111111111111"
tools_digest="sha256:2222222222222222222222222222222222222222222222222222222222222222"
datascript_digest="sha256:3333333333333333333333333333333333333333333333333333333333333333"
dataprotection_digest="sha256:4444444444444444444444444444444444444444444444444444444444444444"
datasafed_digest="sha256:5555555555555555555555555555555555555555555555555555555555555555"
charts_digest="sha256:6666666666666666666666666666666666666666666666666666666666666666"
spilo_digest="sha256:7777777777777777777777777777777777777777777777777777777777777777"
pgbouncer_digest="sha256:8888888888888888888888888888888888888888888888888888888888888888"
exporter_digest="sha256:9999999999999999999999999999999999999999999999999999999999999999"

rendered_control_plane="$(mktemp)"
rendered_addon="$(mktemp)"
trap 'rm -f "${rendered_control_plane}" "${rendered_addon}"' EXIT

bash -n \
  "${ROOT_DIR}/examples/polardb-postgresql/production/scripts/install-restore-drill.sh" \
  "${ROOT_DIR}/examples/polardb-postgresql/production/scripts/fence-redfish.sh"

FENCE_TARGET=system-1 \
REDFISH_ENDPOINT=https://bmc.example.test/redfish/v1/Systems \
FENCE_REASON=node-lost \
CONFIRM_FENCE=system-1 \
FENCE_DRY_RUN=true \
  bash "${ROOT_DIR}/examples/polardb-postgresql/production/scripts/fence-redfish.sh" |
  grep -F 'Would Redfish ForceOff system-1 for node-lost' >/dev/null

"${HELM}" template kubeblocks "${ROOT_DIR}/deploy/helm" \
  --set image.registry=ghcr.io/wallyxjh \
  --set image.repository=kubeblocks \
  --set image.digest="${manager_digest}" \
  --set image.tools.repository=kubeblocks-tools \
  --set image.tools.digest="${tools_digest}" \
  --set image.datascript.repository=kubeblocks-datascript \
  --set image.datascript.digest="${datascript_digest}" \
  --set dataProtection.image.registry=ghcr.io/wallyxjh \
  --set dataProtection.image.repository=kubeblocks-dataprotection \
  --set dataProtection.image.digest="${dataprotection_digest}" \
  --set dataProtection.image.datasafed.registry=registry.example.test \
  --set dataProtection.image.datasafed.repository=datasafed \
  --set dataProtection.image.datasafed.digest="${datasafed_digest}" \
  --set addonChartsImage.registry=ghcr.io/wallyxjh \
  --set addonChartsImage.repository=kubeblocks-addon-charts \
  --set addonChartsImage.digest="${charts_digest}" >"${rendered_control_plane}"

grep -F "ghcr.io/wallyxjh/kubeblocks@${manager_digest}" "${rendered_control_plane}" >/dev/null
grep -F "ghcr.io/wallyxjh/kubeblocks-tools@${tools_digest}" "${rendered_control_plane}" >/dev/null
grep -F "ghcr.io/wallyxjh/kubeblocks-datascript@${datascript_digest}" "${rendered_control_plane}" >/dev/null
grep -F "ghcr.io/wallyxjh/kubeblocks-dataprotection@${dataprotection_digest}" "${rendered_control_plane}" >/dev/null
grep -F "registry.example.test/datasafed@${datasafed_digest}" "${rendered_control_plane}" >/dev/null
grep -F "ghcr.io/wallyxjh/kubeblocks-addon-charts@${charts_digest}" "${rendered_control_plane}" >/dev/null

"${HELM}" template polardb-postgresql "${ROOT_DIR}/deploy/addons/polardb-postgresql" \
  --set image.digest="${spilo_digest}" \
  --set pgbouncer.image.digest="${pgbouncer_digest}" \
  --set metrics.image.digest="${exporter_digest}" \
  --set prometheusRule.enabled=true >"${rendered_addon}"

grep -F "docker.io/apecloud/spilo@${spilo_digest}" "${rendered_addon}" >/dev/null
grep -F "docker.io/apecloud/pgbouncer@${pgbouncer_digest}" "${rendered_addon}" >/dev/null
grep -F "docker.io/apecloud/postgres-exporter@${exporter_digest}" "${rendered_addon}" >/dev/null
grep -F 'image: $(POSTGRESQL_IMAGE)' "${rendered_addon}" >/dev/null
grep -F "mappingValue: \"docker.io/apecloud/spilo@${spilo_digest}\"" "${rendered_addon}" >/dev/null
grep -F 'component_definition="polardb-postgresql-ha-v1"' "${rendered_addon}" >/dev/null
grep -F 'or absent(pg_replication_is_master' "${rendered_addon}" >/dev/null

printf 'PolarDB PostgreSQL production manifest checks passed.\n'
