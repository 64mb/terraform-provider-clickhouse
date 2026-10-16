terraform {
  required_providers {
    clickhouse = {
      version = "2.0.0"
      source  = "hashicorp.com/64mb/clickhouse"
    }
  }
}


data "clickhouse_dbs" "this" {}

output "all_dbs" {
  value = data.clickhouse_dbs.this.dbs
}
