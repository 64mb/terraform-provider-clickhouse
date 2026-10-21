terraform {
  required_providers {
    clickhouse = {
      version = "~> 1.1"
      source  = "registry.opentofu.org/64mb/clickhouse"
    }
  }
}

provider "clickhouse" {
  port = 9000
}

resource "clickhouse_db" "awesome_database" {
  name    = "awesome_database"
  comment = "This is an awesome database"
}

resource "clickhouse_role" "awesome_role" {
  name       = "awesome_role"
  database   = clickhouse_db.awesome_database.name
  privileges = ["INSERT"]
}


