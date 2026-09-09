#!/usr/bin/env bash
set -euo pipefail

COMPONENT_DEFINITION="${COMPONENT_DEFINITION:-polardb-postgresql-ha-v1}"
PGDATA_PATH="${PGDATA_PATH:-/home/postgres/pgdata/pgroot/data}"
MAX_SWITCHOVER_LAG_BYTES="${MAX_SWITCHOVER_LAG_BYTES:-0}"

src="$(mktemp)"
tmp="$(mktemp)"
cleanup() {
  rm -f "${src}" "${tmp}"
}
trap cleanup EXIT

kubectl get componentdefinition "${COMPONENT_DEFINITION}" -o json > "${src}"
python3 - "${PGDATA_PATH}" "${MAX_SWITCHOVER_LAG_BYTES}" "${src}" > "${tmp}" <<'PY'
import json
import sys

pgdata_path = sys.argv[1]
max_switchover_lag_bytes = int(sys.argv[2])
source_path = sys.argv[3]
if max_switchover_lag_bytes < 0:
    raise SystemExit("MAX_SWITCHOVER_LAG_BYTES must be greater than or equal to zero")
with open(source_path, "r", encoding="utf-8") as source:
    obj = json.load(source)

metadata = obj.setdefault("metadata", {})
for key in ["managedFields"]:
    metadata.pop(key, None)
metadata.pop("annotations", None)
obj.pop("status", None)

spec = obj.setdefault("spec", {})
runtime = spec.setdefault("runtime", {})
containers = runtime.setdefault("containers", [])
main = None
for container in containers:
    if container.get("name") == "postgresql":
        main = container
        break
if main is None:
    raise SystemExit("postgresql container not found in ComponentDefinition")

def upsert_env(container, name, value):
    envs = container.setdefault("env", [])
    for env in envs:
        if env.get("name") == name:
            env.clear()
            env["name"] = name
            env["value"] = value
            return
    envs.append({"name": name, "value": value})

upsert_env(main, "PGDATA", pgdata_path)
upsert_env(main, "KB_ENABLE_HA", "false")

image = main.get("image")
if not image:
    raise SystemExit("postgresql container image not found")

with_candidate = r'''set -euo pipefail
endpoint="http://${KB_REPLICATION_PRIMARY_POD_FQDN}:8008"
payload="$(printf '{"leader":"%s","candidate":"%s"}' "${KB_REPLICATION_PRIMARY_POD_NAME}" "${KB_SWITCHOVER_CANDIDATE_NAME}")"
curl -fsS -XPOST "${endpoint}/switchover" -d "${payload}"
'''

without_candidate = r'''set -euo pipefail
endpoint="http://${KB_REPLICATION_PRIMARY_POD_FQDN}:8008"
payload="$(
  PATRONI_ENDPOINT="${endpoint}" MAX_SWITCHOVER_LAG_BYTES="''' + '" + str(max_switchover_lag_bytes) + "' + r'''" python3 - <<'PY2'
import json
import os
import sys
import urllib.request

endpoint = os.environ["PATRONI_ENDPOINT"]
leader = os.environ["KB_REPLICATION_PRIMARY_POD_NAME"]
max_lag = int(os.environ["MAX_SWITCHOVER_LAG_BYTES"])
with urllib.request.urlopen(endpoint + "/cluster", timeout=5) as resp:
    cluster = json.load(resp)

candidates = []
for member in cluster.get("members", []):
    name = member.get("name")
    role = str(member.get("role", "")).lower()
    state = str(member.get("state", "")).lower()
    if not name or name == leader or state != "running":
        continue
    if role not in ("replica", "sync_standby", "standby_leader"):
        continue
    lag = member.get("lag", 0) or 0
    try:
        lag = int(lag)
    except (TypeError, ValueError):
        lag = 9223372036854775807
    if lag > max_lag:
        continue
    candidates.append((lag, name))

if not candidates:
    raise SystemExit(f"no healthy Patroni replica candidate within {max_lag} bytes of lag")

candidate = sorted(candidates)[0][1]
print(json.dumps({"leader": leader, "candidate": candidate}))
PY2
)"
curl -fsS -XPOST "${endpoint}/switchover" -d "${payload}"
'''

actions = spec.setdefault("lifecycleActions", {})
actions.setdefault("roleProbe", {"builtinHandler": "polardb-postgresql", "periodSeconds": 1, "timeoutSeconds": 1})
actions["memberJoin"] = {"builtinHandler": "polardb-postgresql"}
actions["memberLeave"] = {"builtinHandler": "polardb-postgresql"}
actions["readonly"] = {"builtinHandler": "polardb-postgresql"}
actions["readwrite"] = {"builtinHandler": "polardb-postgresql"}
actions["switchover"] = {
    "withCandidate": {
        "image": image,
        "timeoutSeconds": 0,
        "exec": {"command": ["/bin/bash", "-c"], "args": [with_candidate]},
    },
    "withoutCandidate": {
        "image": image,
        "timeoutSeconds": 0,
        "exec": {"command": ["/bin/bash", "-c"], "args": [without_candidate]},
    },
}

json.dump(obj, sys.stdout, indent=2)
sys.stdout.write("\n")
PY

kubectl replace -f "${tmp}"
