#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:?set NAMESPACE to the source Cluster namespace}"
CLUSTER="${CLUSTER:?set CLUSTER to the source Cluster name}"
TOOLS_IMAGE="${TOOLS_IMAGE:-ghcr.io/wallyxjh/kubeblocks-tools@sha256:fc798eecb8b7f8871ab662baab63387ee1a2d0bbd64061a662e2c315c156ca38}"
SCHEDULE="${SCHEDULE:-30 3 * * 0}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-1800}"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

trim_name() {
  printf '%s' "$1" | cut -c1-63 | sed 's/-$//'
}

case "$TOOLS_IMAGE" in
  *@sha256:*) ;;
  *) die "TOOLS_IMAGE must be fixed by digest" ;;
esac

[[ "$SCHEDULE" != *$'\n'* && "$SCHEDULE" != *'"'* ]] ||
  die "SCHEDULE must be a single cron expression without quotes or newlines"

script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
drill_script="${script_dir}/run-restore-drill.sh"
[[ -r "$drill_script" ]] || die "cannot read ${drill_script}"

base_name="$(trim_name "polardb-pg-restore-drill-${CLUSTER}")"
service_account="${base_name}"
config_map="${base_name}"

kubectl create configmap "$config_map" -n "$NAMESPACE" \
  --from-file=run-restore-drill.sh="$drill_script" \
  --dry-run=client -o yaml | kubectl apply -f -

cat <<YAML | kubectl apply -f -
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${service_account}
  namespace: ${NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${service_account}
  namespace: ${NAMESPACE}
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list", "watch", "create"]
  - apiGroups: ["apps.kubeblocks.io"]
    resources: ["clusters", "opsrequests"]
    verbs: ["get", "list", "watch", "create", "delete"]
  - apiGroups: ["dataprotection.kubeblocks.io"]
    resources: ["backups", "backuppolicies"]
    verbs: ["get", "list", "watch", "create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${service_account}
  namespace: ${NAMESPACE}
subjects:
  - kind: ServiceAccount
    name: ${service_account}
    namespace: ${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${service_account}
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: ${base_name}
  namespace: ${NAMESPACE}
  labels:
    app.kubernetes.io/component: restore-drill
    app.kubernetes.io/instance: ${CLUSTER}
spec:
  schedule: "${SCHEDULE}"
  concurrencyPolicy: Forbid
  failedJobsHistoryLimit: 3
  successfulJobsHistoryLimit: 3
  jobTemplate:
    spec:
      backoffLimit: 0
      activeDeadlineSeconds: $((TIMEOUT_SECONDS + 600))
      template:
        metadata:
          labels:
            app.kubernetes.io/component: restore-drill
            app.kubernetes.io/instance: ${CLUSTER}
        spec:
          restartPolicy: Never
          serviceAccountName: ${service_account}
          containers:
            - name: restore-drill
              image: ${TOOLS_IMAGE}
              imagePullPolicy: IfNotPresent
              command: ["/bin/bash", "/scripts/run-restore-drill.sh"]
              env:
                - name: NAMESPACE
                  value: ${NAMESPACE}
                - name: CLUSTER
                  value: ${CLUSTER}
                - name: TIMEOUT_SECONDS
                  value: "${TIMEOUT_SECONDS}"
                - name: CLEANUP
                  value: "true"
              volumeMounts:
                - name: scripts
                  mountPath: /scripts
                  readOnly: true
          volumes:
            - name: scripts
              configMap:
                name: ${config_map}
                defaultMode: 0555
YAML

printf 'Installed weekly PolarDB-PG restore drill: namespace=%s cluster=%s schedule=%s UTC\n' \
  "$NAMESPACE" "$CLUSTER" "$SCHEDULE"
