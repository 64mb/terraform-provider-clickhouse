# Terraform Provider: Clickhouse (Terraform Plugin SDK)

Manage ClickHouse databases, tables, roles and users with Terraform or OpenTofu. This fork is maintained at [64mb/terraform-provider-clickhouse](https://github.com/64mb/terraform-provider-clickhouse).

Tables support complex types, existing-table import, supported ALTER operations and an explicit offline rebuild mode with retained originals. See the [table resource guide](docs/resources/table.md) for the exact compatibility and recovery contracts and [publishing guide](internal-docs/publishing.md) for signed releases and OpenTofu registration.

## Local verification

Use a disposable ClickHouse server, never a production connection:

```sh
docker run -d --name clickhouse-provider-test -p 127.0.0.1:19000:9000 \
  -e CLICKHOUSE_SKIP_USER_SETUP=1 clickhouse/clickhouse-server:25.3
go test -race -count=1 -skip '^(TestAcc|TestIntegration)' ./...
CLICKHOUSE_TEST_ADDR=127.0.0.1:19000 go test ./pkg/resources/table -run '^TestIntegration' -v
TF_ACC=1 go test ./pkg/resources/table -run '^TestAccTable' -v
# remove the disposable fixture after testing
docker rm -f clickhouse-provider-test
```

The table lifecycle tests use only `127.0.0.1:19000`. Existing legacy acceptance tests use the `TF_CLICKHOUSE_*` environment variables. CI also runs the table lifecycle with Terraform and OpenTofu. To run against OpenTofu locally, install `tofu`, set `TF_ACC_TERRAFORM_PATH` to its absolute path, and run the same SDK acceptance tests.

## Requirements

-	[Terraform](https://www.terraform.io/downloads.html) >= 0.13.x
-	[Go](https://golang.org/doc/install) >= 1.25 for development and CI

## Building The Provider

1. Clone the repository
1. Enter the repository directory
1. Build the provider using the Go `install` command: 
```sh
$ go install
```

## Adding Dependencies

This provider uses [Go modules](https://github.com/golang/go/wiki/Modules).
Please see the Go documentation for the most up to date information about using Go modules.

To add a new dependency `github.com/author/dependency` to your Terraform provider:

```bash
go get github.com/author/dependency
go mod tidy
```

Then commit the changes to `go.mod` and `go.sum`.

## Using the provider


Definining provider. The port should be the Clickhouse native protocol port (9000 by default, and 9440 for Clickhouse Cloud)

```hcl
provider "clickhouse" {
  port           = 9000           # Clickhouse native protocol port
  host           = "127.0.0.1"
  username       = "default"
  password       = ""
}
```

In order to definte url, username and password in a safety way it is possible to define them using env vars:

```config
TF_CLICKHOUSE_USERNAME=default
TF_CLICKHOUSE_PASSWORD=""
TF_CLICKHOUSE_HOST="127.0.0.1"
TF_CLICKHOUSE_PORT=9000
```

```hcl
resource "clickhouse_db" "test_db_clusterd" {
  name = "database_test_clustered"
  comment = "This is a test database"
}
```

### Clustered server

Configuring provider

```hcl
provider "clickhouse" {
  port           = 9000
  host           = "127.0.0.1"
  username       = "default"
  password       = ""
  default_cluster ="cluster"
}
```

Creating a Database

```hcl
resource "clickhouse_db" "test_db_clusterd" {
  name = "database_test_clustered"
  comment = "This is a test database"
  cluster = "cluster"
}
```

### Clustered server using Altinity Clickhouse Operator

I is possible to use macros defined for cluster, databases, installation names in Altinity operator when creating resources.

```hcl
provider "clickhouse" {
  port           = 9000
  host           = "127.0.0.1"
  username       = "default"
  password       = ""
  default_cluster ="'{cluster}'"
}
```

```hcl
resource "clickhouse_db" "test_db_cluster" {
  name = "database_test_clustered"
  comment = "This is a test database"
  cluster = "'{cluster}'"
}
```

Creating tables

```hcl
resource "clickhouse_table" "replicated_table" {
  database      = clickhouse_db.test_db_clustered.name
  name    = "replicated_table"
  cluster       = clickhouse_db.test_db_clustered.cluster
  engine        = "ReplicatedMergeTree"
  engine_params = ["'/clickhouse/{installation}/clickhouse_db.test_db_clustered.cluster/tables/{shard}/{database}/{table}'", "'{replica}'"]
  order_by      = ["event_date", "event_type"]
  columns {
    name = "event_date"
    type = "Date"
  }
  columns {
    name = "event_type"
    type = "Int32"
  }
  columns {
    name = "article_id"
    type = "Int32"
  }
  columns {
    name = "title"
    type = "String"
  }
  partition_by {
    by = "event_type"
  }
  partition_by {
    by                 = "event_date"
    partition_function = "toYYYYMM"
  }
}


resource "clickhouse_table" "distributed_table" {
  database      = clickhouse_db.test_db_clustered.name
  name    = "distributed_table"
  cluster       = clickhouse_db.test_db_clustered.cluster
  engine        = "Distributed"
  engine_params = [clickhouse_db.test_db_clustered.cluster, clickhouse_db.test_db_clustered.name, clickhouse_table.replicated_table.name, "rand()"]
}
```

Creating roles

```hcl
resource "clickhouse_role" "my_database_rw" {
  name       = "my_database_rw"
  database   = clickhouse_db.test_db_cluster.name
  privileges = ["SELECT", "INSERT"]
}
```

Creating users

```hcl
resource "clickhouse_user" "my_database_rw_user" {
  name     = "my_database_rw_user"
  password = "awesome_user_password"
  roles    = [clickhouse_role.my_database_rw.name]
}
```

## Developing the Provider

If you wish to work on the provider, you'll first need [Go](http://www.golang.org) installed on your machine (see [Requirements](#requirements) above).

To compile the provider, run `go install`. This will build the provider and put the provider binary in the `$GOPATH/bin` directory.

To generate or update documentation, run `go generate`.

In order to run the full suite of Acceptance tests, run `make testacc`.

_Note:_ Acceptance tests create real resources, and often cost money to run.

```sh
$ make testacc
```
