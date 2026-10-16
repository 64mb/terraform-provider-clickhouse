terraform {
  required_providers {
    clickhouse = {
      version = "2.0.0"
      source  = "hashicorp.com/64mb/clickhouse"
    }
  }
}


provider "clickhouse" {
  port           = 8123
  clickhouse_url = "127.0.0.1"
  username       = "default"
  password       = ""
}
