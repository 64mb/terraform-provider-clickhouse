# ClickHouse Provider

Manage ClickHouse databases, tables, roles and users with **OpenTofu or Terraform**.
Maintained by [64mb](https://github.com/64mb/terraform-provider-clickhouse).

## 🚀 Install & use

Add the provider to your configuration. The explicit registry address selects the
[OpenTofu Registry](https://registry.opentofu.org/64mb/clickhouse), including when using Terraform.

```hcl
terraform {
  required_providers {
    clickhouse = {
      source  = "registry.opentofu.org/64mb/clickhouse"
      version = "~> 1.0"
    }
  }
}

provider "clickhouse" {}

resource "clickhouse_db" "analytics" {
  name = "analytics"
}

resource "clickhouse_table" "events" {
  database = clickhouse_db.analytics.name
  name     = "events"
  engine   = "MergeTree"
  order_by = ["event_time", "event_type"]

  column {
    name = "event_time"
    type = "DateTime"
  }

  column {
    name = "event_type"
    type = "LowCardinality(String)"
  }
}
```

Set connection details in your shell, then initialize and apply:

```sh
export TF_CLICKHOUSE_HOST="127.0.0.1"
export TF_CLICKHOUSE_PORT="9000"
export TF_CLICKHOUSE_USERNAME="default"
export TF_CLICKHOUSE_PASSWORD="your-password"

tofu init
tofu plan
tofu apply
```

Use `terraform` in place of `tofu` for the same workflow. Connect through the
**native TCP protocol**, usually port `9000`. For TLS, set `secure = true` in the
provider block and use your server's secure native port, commonly `9440`.
Connection settings can also be supplied directly in the
[provider configuration](docs/index.md). If you use provider mirrors, allow this
provider to install directly from `registry.opentofu.org`.

Provider configuration is lazy: you can plan new objects before the ClickHouse
endpoint exists, including when its address comes from another resource.
Refresh, import and data-source reads require a reachable server. See
[creating the cluster and its objects together](docs/index.md#creating-the-cluster-and-its-objects-together).

## 🧩 What you can manage

| Resource or data source | Purpose | Reference |
| --- | --- | --- |
| `clickhouse_db` | Create databases and manage comments | [Database](docs/resources/db.md) |
| `clickhouse_table` | Manage table definitions, import existing tables and apply supported ALTER operations | [Table](docs/resources/table.md) |
| `clickhouse_role` | Manage roles with database or global privileges | [Role](docs/resources/role.md) |
| `clickhouse_user` | Create users and assign roles | [User](docs/resources/user.md) |
| `clickhouse_dbs` · data source | List databases and their metadata | [Databases](docs/data-sources/dbs.md) |

For example, grant read access to the database created above:

```hcl
variable "reader_password" {
  type      = string
  sensitive = true
}

resource "clickhouse_role" "reader" {
  name       = "analytics_reader"
  database   = clickhouse_db.analytics.name
  privileges = ["SELECT"]
}

resource "clickhouse_user" "reader" {
  name     = "analytics_reader"
  password = var.reader_password
  roles    = [clickhouse_role.reader.name]
}

data "clickhouse_dbs" "all" {}
```

User passwords are stored in state. Protect your state backend and supply the
password through your normal secrets workflow.

### Existing tables & schema changes

Import an existing table with its database and table name, optionally prefixed
by an explicit cluster target:

```sh
tofu import clickhouse_table.events 'analytics:events'
tofu import clickhouse_table.events 'my_cluster:analytics:events'
```

Use one import command matching your deployment, then align the resource
configuration with the imported state before applying changes.

Supported ALTER operations update tables in place and preserve their UUIDs.
Explicit column drops remove their data. Unsupported changes return an error
without falling back to DROP/CREATE. An explicit offline
rebuild is available for a limited set of standalone MergeTree migrations and
retains the original table. Read the [table guide](docs/resources/table.md) for
supported types, ALTER rules, import and the maintenance/recovery contract.

### Clustered deployments

Set a default cluster for databases and tables, or override it with their
resource-level `cluster` argument:

```hcl
provider "clickhouse" {
  default_cluster = "my_cluster"
}
```

Replace the earlier provider block with this one. Roles, users and grants run
on the connected node without `ON CLUSTER`. Database and table DDL waits for
all target hosts and surfaces remote errors. Distributed DDL is not atomic:
a timeout can leave partial changes or queued work that executes later.
See the [cluster DDL guide](docs/resources/table.md#cluster-ddl-completion) and
[table examples](examples/resources/clickhouse_table).

## 🛠 Contributing & development

Use **Go 1.25+** and [Task](https://taskfile.dev). Start with the local checks:

```sh
task build
go vet ./...
task test
```

For live table tests, start a disposable ClickHouse instance:

```sh
docker run -d --name clickhouse-provider-test -p 127.0.0.1:19000:9000 \
  -e CLICKHOUSE_SKIP_USER_SETUP=1 clickhouse/clickhouse-server:25.3

task test-table-integration
task test-table-lifecycle

docker rm -f clickhouse-provider-test
```

Wait for the server to accept native connections before running the tests.
Lifecycle tests need Terraform on `PATH`. To use OpenTofu, set
`TF_ACC_TERRAFORM_PATH` to the absolute path of `tofu`.

CI tests ClickHouse **25.3, 25.8, 26.3 and 26.8** with both CLIs. A separate
cluster job tests two ClickHouse nodes and a dedicated Keeper, using `latest`
images. Follow the [fixture guide](tests/fixtures/README.md) for topology tests,
cluster failure cases and cleanup.

Keep changes focused, add regression coverage for fixes, and run `task doc` when
changing generated schemas. See the [publishing guide](internal-docs/publishing.md)
for release verification and registry publishing.
