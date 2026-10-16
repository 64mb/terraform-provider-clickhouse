terraform {
  required_providers {
    clickhouse = {
      source = "64mb/clickhouse"
    }
  }
}

resource "clickhouse_table" "events" {
  database = "default"
  name     = "events"
  engine   = "MergeTree"
  order_by = ["toStartOfHour(event_time)", "id"]

  partition_by {
    by = "toYYYYMM(event_time)"
  }

  column {
    name = "event_time"
    type = "DateTime64(9, 'UTC')"
  }
  column {
    name = "id"
    type = "UUID"
  }
  column {
    name               = "category"
    type               = "LowCardinality(String)"
    default_kind       = "DEFAULT"
    default_expression = "'unknown'"
    comment            = "event category"
  }
  column {
    name = "attributes"
    type = "Map(String, Array(Nullable(String)))"
  }

  settings = {
    index_granularity = "8192"
  }
  comment = "Application events"
}
