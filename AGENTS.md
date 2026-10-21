# Repository guidance

## Project

Terraform provider for ClickHouse, implemented in Go with Terraform Plugin SDK v2
and `clickhouse-go/v2`. Use the native TCP protocol on port 9000, not HTTP on 8123.
The provider manages databases, tables, roles, users, and the databases data source.

- `pkg/provider/`: provider configuration and shared client setup
- `pkg/resources/{db,table,role,user}/`: schemas, lifecycle callbacks, services, and models
- `pkg/common/`: shared client, cluster DDL, SQL helpers, and metadata utilities
- `pkg/datasources/`: data sources
- `docs/` and `examples/`: user documentation and Terraform configurations
- `tests/fixtures/`: local ClickHouse and ClickHouse Keeper test environments

## Development rules

- Keep changes focused on the requested behavior and preserve unrelated user edits
- Read the relevant resource, service, tests, and documentation before changing behavior
- Reproduce a bug with a failing regression test before implementing its fix
- Use `gofmt` and follow the surrounding Go style
- Wrap errors with useful operation context and `%w` when preserving the cause
- Bind query values and escape SQL identifiers rather than interpolating raw input
- Keep resource IDs and state stable across refresh, import, and ordinary updates
- Preserve existing data and table UUIDs during supported ALTER operations
- Never fall back to DROP/CREATE when an ALTER fails
- Keep destructive migrations behind the documented explicit maintenance contract
- Update resource documentation and examples when behavior or configuration changes
- Use development sub-agents with model `gpt-6.1-sol` and reasoning effort `low` when independent work can proceed without overlapping edits
- Before completing code changes, have a separate sub-agent review the final diff with model `gpt-6.1-sol` and reasoning effort `xhigh`
- Address actionable review findings and verify the resulting changes before reporting completion

## ClickHouse behavior

- Provider configuration creates a lazy connection pool shared through `common.ApiClient`
- Configuring the provider must not require a running ClickHouse server
- Existing-resource refresh and data-source reads require an accessible server
- `default_cluster` and resource-level `cluster` apply to databases and tables
- Roles, users, and grants currently execute without `ON CLUSTER`
- Use the shared cluster helpers for escaping, legacy quoted macros, and DDL completion
- Cluster DDL must surface remote failures even under asynchronous or `never_throw` profiles
- Distributed DDL is not atomic and may execute later after a timeout
- Check all target nodes before deleting a clustered database with potentially remaining tables
- Refresh reads the connected node and does not establish cluster-wide schema consistency
- Cluster metadata is stored in database and table comments as JSON
- Use `SHOW GRANTS` for roles to preserve original expandable privileges
- Roles currently manage one database scope and do not track partial REVOKE statements
- Replicated and Distributed topology changes require an external migration where documented

## Verification

Run the checks relevant to the change and report what actually ran, including skips.
Do not treat a skipped integration or acceptance test as a successful live check.

```sh
go build ./...
go vet ./...
go test -race -count=1 -skip '^(TestAcc|TestIntegration)' ./...
```

Use `resource.Test` and `TF_ACC=1` for new Terraform acceptance tests. Configure
`TF_CLICKHOUSE_HOST`, `TF_CLICKHOUSE_PORT`, `TF_CLICKHOUSE_USERNAME`, and
`TF_CLICKHOUSE_PASSWORD` when a test relies on provider environment defaults.
Some older tests use `resource.UnitTest` and run without `TF_ACC`.

For cluster changes, follow `tests/fixtures/README.md`. The two-node Compose fixture
uses `clickhouse/clickhouse-server:latest` and a separate
`clickhouse/clickhouse-keeper:latest`. Verify both nodes directly, including data,
UUIDs, state, import, unchanged plans, deletion, and relevant failure paths.
Use unique object names and remove test containers and temporary data after testing.

## Pull requests

These repository-specific rules override the general Russian PR-description format.

- Write descriptions in English as short, clear, concrete bullet points
- State what changed and why it matters, with one change or important verification result per bullet
- Prefer action verbs such as `Add`, `Fix`, `Preserve`, `Wait`, and `Verify`
- Omit introductions, process narration, boilerplate, and unnecessary implementation details
- Do not end bullets with periods or use semicolons or em dashes
- Include meaningful verification results and material limitations
- Before editing a PR description, read its current text and preserve manual edits
- If asked to restore a description, restore the exact requested revision from edit history
- Write titles in English with a lowercase first word after any prefix
- When a ticket exists, use `TICKET-000: fix tests`; do not invent ticket IDs
- Preserve manual edits to PR titles

## Code comments

- Start comments with a lowercase letter
- Do not end comments with a period or use semicolons
- Put identifiers and literal values mentioned in prose in square brackets, such as `[table]`
- Preserve identifier casing and required syntax in directives, examples, and special-purpose comments
