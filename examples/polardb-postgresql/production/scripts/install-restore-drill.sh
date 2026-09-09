#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="${NAMESPACE:?set NAMESPACE to the source Cluster namespace}"
CLUSTER="${CLUSTER:?set CLUSTER to the source Cluster name}"
TOOLS_IMAGE="${TOOLS_IMAGE:?set TOOLS_IMAGE to a digest-pinned kubeblocks-tools image}"
SCHEDULE="${SCHEDULE:-30 3 * * 0}"
TIMEOUT_SECONDS="${TIMEOUT_SECONDS:-900}"
SERVICE_ACCOUNT="${SERVICE_ACCOUNT:-polardb-pg-restore-drill}"

die() {
  printf 'ERROR: %s\n' "$*" >&2
  exit 1
}

is_dns_label() {
  [[ "$1" =~ ^[a-z0-9]([-a-z0-9]*[a-z0-9])?$ ]]
}

is_dns_label "${NAMESPACE}" || die "NAMESPACE must be a lowercase DNS label"
is_dns_label "${CLUSTER}" || die "CLUSTER must be a lowercase DNS label"
is_dns_label "${SERVICE_ACCOUNT}" || die "SERVICE_ACCOUNT must be a lowercase DNS label"
[[ "${SCHEDULE}" != *$'\n'* && "${SCHEDULE}" != *'"'* ]] ||
  die "SCHEDULE must be a single cron expression without double quotes"

case "${TOOLS_IMAGE}" in
  *@sha256:*) ;;
  *) die "TOOLS_IMAGE must be pinned by digest" ;;
esac

backup_policy="${CLUSTER}-postgresql-backup-policy"
schedule_name="${CLUSTER}-postgresql-backup-schedule"
test "$(kubectl get backuppolicy -n "${NAMESPACE}" "${backup_policy}" -o jsonpath='{.status.phase}' 2>/dev/null)" = Available ||
  die "BackupPolicy ${backup_policy} is not Available"
test "$(kubectl get backupschedule -n "${NAMESPACE}" "${schedule_name}" -o jsonpath='{.status.phase}' 2>/dev/null)" = Available ||
  die "BackupSchedule ${schedule_name} is not Available"
test "$(kubectl get backupschedule -n "${NAMESPACE}" "${schedule_name}" -o jsonpath='{.spec.schedules[0].enabled}' 2>/dev/null)" = true ||
  die "BackupSchedule ${schedule_name} is not enabled"

kubectl apply -f - <<YAML
apiVersion: v1
kind: ServiceAccount
metadata:
  name: ${SERVICE_ACCOUNT}
  namespace: ${NAMESPACE}
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: ${SERVICE_ACCOUNT}
  namespace: ${NAMESPACE}
rules:
- apiGroups: ["apps.kubeblocks.io"]
  resources: ["clusters", "opsrequests"]
  verbs: ["get", "list", "watch", "create", "delete"]
- apiGroups: ["dataprotection.kubeblocks.io"]
  resources: ["backups"]
  verbs: ["get", "list", "watch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: ${SERVICE_ACCOUNT}
  namespace: ${NAMESPACE}
subjects:
- kind: ServiceAccount
  name: ${SERVICE_ACCOUNT}
  namespace: ${NAMESPACE}
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: ${SERVICE_ACCOUNT}
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: polardb-pg-restore-drill
  namespace: ${NAMESPACE}
data:
  run.sh: |-
    #!/bin/sh
    set -eu
    backup="\$(kubectl get backup -n '${NAMESPACE}' -l app.kubernetes.io/instance='${CLUSTER}' --sort-by=.status.completionTimestamp \\
      -o go-template='{{range .items}}{{if eq .status.phase "Completed"}}{{.metadata.name}}{{"\\n"}}{{end}}{{end}}' | tail -n 1)"
    test -n "\${backup}" || { echo "no completed backup found" >&2; exit 1; }
    restore="restore-drill-\$(date +%s)"
    ops="restore-drill-\$(date +%s)"
    kubectl apply -f - <<RESTORE
    apiVersion: apps.kubeblocks.io/v1alpha1
    kind: OpsRequest
    metadata:
      name: \${ops}
      namespace: ${NAMESPACE}
    spec:
      clusterName: \${restore}
      type: Restore
      restore:
        backupName: \${backup}
        backupNamespace: ${NAMESPACE}
        volumeRestorePolicy: Parallel
        deferPostReadyUntilClusterRunning: true
    RESTORE
    deadline=\$((\$(date +%s) + ${TIMEOUT_SECONDS}))
    while test "\$(date +%s)" -lt "\${deadline}"; do
      phase="\$(kubectl get opsrequest -n '${NAMESPACE}' "\${ops}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
      test "\${phase}" = Succeed && break
      test "\${phase}" != Failed || { kubectl get opsrequest -n '${NAMESPACE}' "\${ops}" -o yaml; exit 1; }
      sleep 5
    done
    test "\${phase}" = Succeed || { echo "restore OpsRequest timed out" >&2; exit 1; }
    test "\$(kubectl get cluster -n '${NAMESPACE}' "\${restore}" -o jsonpath='{.status.phase}')" = Running
    kubectl delete cluster -n '${NAMESPACE}' "\${restore}" --wait=true
---
apiVersion: batch/v1
kind: CronJob
metadata:
  name: polardb-pg-restore-drill
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
      activeDeadlineSeconds: $((TIMEOUT_SECONDS + 300))
      backoffLimit: 0
      template:
        metadata:
          labels:
            app.kubernetes.io/component: restore-drill
            app.kubernetes.io/instance: ${CLUSTER}
        spec:
          restartPolicy: Never
          serviceAccountName: ${SERVICE_ACCOUNT}
          containers:
          - name: restore-drill
            image: ${TOOLS_IMAGE}
            imagePullPolicy: IfNotPresent
            command: ["/bin/sh", "/scripts/run.sh"]
            volumeMounts:
            - name: scripts
              mountPath: /scripts
              readOnly: true
          volumes:
          - name: scripts
            configMap:
              name: polardb-pg-restore-drill
              defaultMode: 0555
YAML

printf 'Installed weekly restore drill for %s/%s at %s (UTC).\n' "${NAMESPACE}" "${CLUSTER}" "${SCHEDULE}"
