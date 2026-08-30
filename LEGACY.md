# Legacy compatibility lifecycle

| Legacy surface | Last supported runtime | Migration | Removal |
| --- | --- | --- | --- |
| `config.toml` `[[projects]]`, platforms and `[[hooks]]` | Contract 1.x | `amux database import-config --dry-run`, then `--apply` | Contract 2.0 runtime; parser retained only for import |
| Invocation/Orchestration `project` target and `X-AgentMux-Project` | Contract 1.x | Import the project and use the returned PostgreSQL Agent ID | Contract 2.0 |
| `amux client --sqlite-path` and `store.Open` | Contract 1.x | `amux database migrate-sqlite` | Contract 2.0 runtime; `OpenLegacySQLite` remains test/migration-only |
| SQLite observation/provider rows | Contract 1.x data | Offline SQLite→PostgreSQL migration with timestamped backup | Import code may be removed after two releases with no supported 1.x upgrade path |
| Legacy model picker and provider metadata adapters | Contract 2.x | Use Runtime Settings and route metadata | Review for removal in Contract 3.0 |

Compatibility code must name its migration path and planned removal version in
the same package. New runtime behavior must not depend on a legacy reader.
