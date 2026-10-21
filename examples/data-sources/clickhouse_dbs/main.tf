terraform {
  required_providers {
    clickhouse = {
      version = "~> 1.1"
      source  = "registry.opentofu.org/64mb/clickhouse"
    }
  }
}


data "clickhouse_dbs" "this" {}

output "all_dbs" {
  value = data.clickhouse_dbs.this.dbs
}
