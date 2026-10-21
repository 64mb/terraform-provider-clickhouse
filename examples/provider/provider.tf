terraform {
  required_providers {
    clickhouse = {
      version = "~> 1.0.2"
      source  = "64mb/clickhouse"
    }
  }
}


provider "clickhouse" {
  port     = 9000
  host     = "127.0.0.1"
  username = "default"
  password = ""
}
