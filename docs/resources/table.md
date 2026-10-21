---
page_title: "clickhouse_table Resource - terraform-provider-clickhouse"
description: |-
  Manage ClickHouse tables, import existing definitions, alter supported properties and rebuild during explicit maintenance
---

# clickhouse_table

The provider reads existing definitions from `system.tables` and ordered `system.columns`. Updates use `ALTER TABLE`. It never replaces a table implicitly when an alteration is unsupported. ClickHouse validates engine-specific restrictions, settings, key compatibility and conversions. Failed alterations report an error; refresh and retry after resolving it. Multiple standalone ALTER commands are not a transaction, so earlier successful changes can remain after a later command fails.

## Schema

Required:

- `database`, `name`: object identifiers, safely quoted in generated SQL
- `engine`: `MergeTree`, `ReplacingMergeTree`, `SummingMergeTree`, `AggregatingMergeTree`, `CollapsingMergeTree`, `VersionedCollapsingMergeTree`, `GraphiteMergeTree`, their `Replicated` variants, or `Distributed`
- `column`: ordered blocks containing `name` and `type`

Optional:

- `engine_params`: list of engine arguments as SQL expressions, including SQL quotes for string literals, defaults to `[]`
- `cluster`: `ON CLUSTER` target, overrides the provider's `default_cluster` when set, otherwise creation uses that default
- `order_by`: ordered list of SQL sorting expressions, defaults to `tuple()` for MergeTree tables
- `primary_key`: ordered list of SQL expressions, defaults to the sorting key on creation
- `partition_by`: ordered blocks with `by` as an SQL expression and optional legacy `partition_function` (`toYYYYMM`, `toYYYYMMDD`, `toYYYYMMDDhhmmss`)
- `ttl`: table TTL SQL expression
- `sample_by`: sampling expression, imported and preserved; changing it requires external migration
- `settings`: map of MergeTree setting names to SQL values, for example `{ index_granularity = "8192", allow_nullable_key = "1" }`
- `comment`: user-facing table comment; provider metadata is stored in a JSON envelope, existing plain comments are accepted on import
- `allow_column_drop`: defaults to `false`, explicitly permits removing columns and their data
- `rebuild`: optional single maintenance block described below

Column options:

- `default_kind`: `DEFAULT`, `MATERIALIZED` or `ALIAS`, together with `default_expression`
- `default_expression`: SQL expression
- `comment`: plain text
- `codec`: codec expression list such as `Delta(8), ZSTD(1)`
- `ttl`: column TTL SQL expression

Computed:

- `id`: `cluster:database:table`
- `uuid`: ClickHouse object UUID
- `create_table_query`: complete server DDL for import inspection, including unmanaged metadata
- `replication_path_macros_enabled`: whether server metadata confirms built-in `{database}` and `{table}` expansion is enabled for a replicated engine
- `rebuild_backup_table`: retained original table after a successful rebuild

Names and comments are escaped. Expressions are deliberately SQL expressions, not HCL identifiers. Statements, SQL comments and unbalanced syntax are rejected. Type declarations use a separate recursive parser, never raw SQL.

## Types and refresh

Types include numeric types, `Bool`, `String`, `UUID`, `IPv4`, `IPv6`, `FixedString`, `Date`, `Date32`, `DateTime` with optional timezone, `DateTime64` with precision and optional timezone, `Decimal` and fixed-width Decimal variants, `Enum8/16`, `LowCardinality`, `Nullable`, `Array`, named or unnamed `Tuple`, `Map`, `Nested`, `AggregateFunction`, `SimpleAggregateFunction`, `Nothing` and basic `JSON`. Composite types may be nested. Server settings and ClickHouse version still determine which combinations are valid.

Whitespace, Decimal aliases, and default DateTime64 precision are normalized. Server formatting reconciles equivalent expressions such as `INTERVAL 30 DAY` and `toIntervalDay(30)` during refresh. String literals retain their contents. Configured `Nested` blocks are preserved when their expanded physical columns match; imports expose physical dotted Array columns, avoiding invented grouping. Grouped `Nested` assumes the ClickHouse default `flatten_nested = 1`; nested default expressions are rejected.

### Replication path macros

For `Replicated*MergeTree` engines, the first `engine_params` argument may contain
`{database}` and `{table}` in a SQL string literal:

```hcl
engine_params = ["'/clickhouse/tables/{shard}/{database}/{table}'", "'{replica}'"]
```

ClickHouse materializes these two placeholders in the stored definition. Refresh,
import planning and ordinary ALTER recognize a matching expanded path without
changing the table or requiring a maintenance rebuild. Real path changes and
changes to the remaining engine arguments still require an external migration
for replicated tables. `{shard}`, `{replica}`, `{uuid}` and custom macros remain
under ClickHouse's control and are not expanded by the provider.

This comparison requires `SELECT` on `system.macros` to confirm that builtin
expansion is enabled. An explicit server macro named `database`, `table` or `uuid`
disables builtin materialization. If metadata is inaccessible, the provider keeps
the ordinary exact-path comparison; use the explicit path from
`create_table_query` in your configuration. Other table operations do not acquire
a new permission requirement. The comparison accepts complete single-quoted SQL
literals, not expressions such as `concat(...)`.

## ALTER behavior

Supported changes include adding, dropping and reordering columns, supported type conversions, defaults, materialized/alias expressions, column/table comments, column codecs, column/table TTL and mutable settings. Type conversions wait for ClickHouse mutations. Changing defaults affects how missing values are computed; the provider does not implicitly run `MATERIALIZE COLUMN` or backfill physically stored values.

A sorting key can be extended with plain columns added without defaults in the same ALTER. Its existing primary key remains unchanged. Set `primary_key` explicitly when extending the sorting key so the configuration describes the retained key. Reordering the existing sorting key, using existing columns in a new sorting expression, changing the primary/partition key, engine/arguments or immutable `index_granularity` requires the maintenance rebuild below. Additional server restrictions return actionable ClickHouse errors without a destructive fallback. `Distributed` ALTER is rejected because topology changes need external coordination. Database/name/cluster changes require an external migration and import.

## Import

```sh
terraform import clickhouse_table.events 'analytics:events'
tofu import clickhouse_table.events 'analytics:events'
# an explicitly managed ON CLUSTER target can be included
terraform import clickhouse_table.events 'my_cluster:analytics:events'
```

Use matching `database` and `name` in the resource configuration. Import accepts existing plain comments and legacy provider comments, reads engine arguments, physical column order, defaults, expressions, keys, TTL and explicit settings. Inspect `terraform state show` / `tofu state show` and write configuration matching those values before planning modifications. The server-injected default `index_granularity = 8192` is omitted when it was not configured; the full DDL remains available in `create_table_query`. Materialized and alias columns must remain in their physical positions. SQL string engine arguments include quotes. Import does not copy data or alter the table.

An imported table may contain metadata outside the managed schema, such as projections or skipping indices. Ordinary ALTER preserves unmanaged definitions subject to ClickHouse's dependency checks. The rebuild mode rejects unsupported metadata instead of silently discarding it. Replicated engine parameters and topology must be represented exactly; import does not infer an ON CLUSTER target from an engine's replication path.

## Maintenance rebuild

```hcl
resource "clickhouse_table" "events" {
  database = "analytics"
  name     = "events"
  engine   = "MergeTree"
  order_by = ["category", "id"]

  column {
    name = "id"
    type = "UInt64"
  }
  column {
    name = "category"
    type = "LowCardinality(String)"
  }

  rebuild {
    enabled                  = true
    maintenance_acknowledged = true
    operation_id             = "events-sort-v2"
  }
}
```

Before setting `maintenance_acknowledged = true`, stop **all** writers externally, including direct application inserts, async producers/queues, ingestion tools and indirect writers. Drain in-flight inserts and mutations. Keep them stopped through apply, verification and every recovery attempt. The acknowledgement is an operator contract, not a server write lock. Process/queue/mutation checks and checksums detect some violations but cannot fence a writer that starts later. Concurrent DDL and simultaneous applies are also unsupported. This mode does not provide a universal online migration.

Only standalone `MergeTree` to `MergeTree` in an `Atomic` database is supported. Reducing/aggregating engines, Replicated/Distributed/ON CLUSTER topology, TTL tables, materialized/alias columns, codecs, constraints, sampling, projections and skipping indices are rejected. Dependency checks deliberately reject a server containing user views, dictionaries, Buffer or Distributed tables, even when they may be unrelated. This conservative boundary avoids silently changing UUID/name dependency bindings. Managed ClickHouse commonly uses replicated engines; those require a separate topology-aware migration.

The provider creates an owned staging table, explicitly copies common stored columns with target casts, and compares row count plus XOR and sum of row hashes before and after copying. New columns use target defaults. Removing any original column requires `allow_column_drop = true`. Verification is probabilistic, not a cryptographic proof of equality, and type conversions can be lossy according to ClickHouse cast semantics. Use enough disk space for both tables.

A single `EXCHANGE TABLES` atomically switches names. The old table is **never automatically deleted**, including on resource destruction. Validate the result, retain the backup as long as needed, and drop it manually only after confirming success. Readers may span the switch; stop/drain readers too when strict cutover visibility is required.

The operation ID selects a deterministic staging/backup name. A journal in table metadata binds it to the source UUID and target definition. Keep the same configuration and operation ID on retry. Before switching, an owned partial staging table is truncated and recopied. After a lost EXCHANGE response, UUID/journal checks recognize completion and prevent exchanging twice. Staging names owned by other operations are rejected. Refresh recovers the backup name from the journal. Keep the backup and journal intact through recovery. Use a new operation ID for another rebuild, or disable/remove the block before normal alterations. Do not run concurrent migrations of the same table.

ClickHouse's relevant contracts: [ALTER columns](https://clickhouse.com/docs/sql-reference/statements/alter/column), [sorting key restrictions](https://clickhouse.com/docs/sql-reference/statements/alter/order-by), [EXCHANGE](https://clickhouse.com/docs/sql-reference/statements/exchange), [Atomic databases](https://clickhouse.com/docs/engines/database-engines/atomic), [replication](https://clickhouse.com/docs/engines/table-engines/mergetree-family/replication)

### Cluster DDL completion

For `ON CLUSTER`, database and table DDL explicitly waits for all target hosts
and returns remote execution errors, even when the server profile uses
`never_throw` or asynchronous distributed DDL. The wait is limited to 180 seconds
and shortened to the remaining operation deadline. Existing `alter_sync` and
`mutations_sync` settings still apply to table updates.

Distributed DDL is not atomic: a failed apply may already have changed some nodes,
and timed-out tasks can execute later. Inspect the cluster DDL queue and reconcile
partial changes before retrying. Refresh reads the connected node and does not
audit schema consistency across the cluster. Database deletion checks every target
node for remaining tables and fails if a node cannot be checked.
