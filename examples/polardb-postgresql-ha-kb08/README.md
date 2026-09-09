# Patroni PostgreSQL HA for KubeBlocks 0.8

This directory is an executable sample for the `polardb-postgresql` addon.
It runs ordinary PostgreSQL/Spilo with Patroni. It is not the PolarDB-PG
shared-storage engine.

Apply `namespace.yaml` and `cluster.yaml` only after the addon and the
corresponding KubeBlocks 0.8 manager image have been deployed. The addon uses
the KB 0.8 `postgresql` probe handler and its `ComponentDefinition.spec.labels`
`patroni-managed` component label
causes the ported manager to set `KB_ENABLE_HA=false` in the Lorry sidecar.
The operation manifests are intentionally separate: do not apply rejoin,
rebuild, backup, and restore manifests at cluster creation time.

The expected component names are:

- Cluster: `pg-ha`
- Component: `postgresql`
- Primary service: `pg-ha-postgresql-postgresql`
- Headless service: `pg-ha-postgresql-headless`

`ops-rebuild.yaml` calls Patroni `/reinitialize` and discards the target
standby's local PostgreSQL data. Edit `TARGET_INSTANCE` and confirm the target
is not primary before applying it.
