# Examples

Each directory contains a standalone Terraform configuration. The root example
also uses the databases data source as a local module.

The examples use `registry.opentofu.org/64mb/clickhouse` and the native TCP port
9000. Terraform and OpenTofu can use the same configurations.

Set connection defaults before running an example:

```sh
export TF_CLICKHOUSE_HOST=127.0.0.1
export TF_CLICKHOUSE_PORT=9000
export TF_CLICKHOUSE_USERNAME=default
export TF_CLICKHOUSE_PASSWORD=''

cd examples/resources/clickhouse_table
terraform init
terraform validate
terraform plan
```

Use `tofu` in place of `terraform` for OpenTofu. Provider blocks inside an example
override the corresponding environment defaults.

- `provider/`: provider configuration, also used by the generated provider documentation
- `data-sources/clickhouse_dbs/`: database discovery
- `resources/clickhouse_db/`: standalone and clustered databases
- `resources/clickhouse_table/`: a MergeTree table
- `resources/clickhouse_role/`: database privileges
- `resources/clickhouse_user/`: a user with two roles

The clustered database example requires a configured ClickHouse cluster. Replace
its cluster macro with your cluster name if your server does not define that macro.
