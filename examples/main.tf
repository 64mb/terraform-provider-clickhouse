terraform {
  required_providers {
    clickhouse = {
      version = "~> 1.1"
      source  = "registry.opentofu.org/64mb/clickhouse"
    }
  }
}

provider "clickhouse" {
  port     = 9000
  host     = "127.0.0.1"
  username = "default"
  password = ""
}

module "databases" {
  source = "./data-sources/clickhouse_dbs"
}


output "databases" {
  value = module.databases.all_dbs
}
