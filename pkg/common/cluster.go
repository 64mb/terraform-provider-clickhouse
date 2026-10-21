package common

import (
	"context"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
)

// cluster DDL must wait for every host and surface remote errors regardless of the user profile
func ClusterDDLContext(ctx context.Context, cluster string, settings clickhouse.Settings) context.Context {
	merged := clickhouse.Settings{}
	for key, value := range settings {
		merged[key] = value
	}
	if cluster == "" {
		return clickhouse.Context(ctx, clickhouse.WithSettings(merged))
	}
	timeout := 180
	if deadline, ok := ctx.Deadline(); ok {
		remaining := int(time.Until(deadline) / time.Second)
		if remaining < 1 {
			remaining = 1
		}
		if remaining < timeout {
			timeout = remaining
		}
	}
	merged["distributed_ddl_output_mode"] = "none"
	merged["distributed_ddl_task_timeout"] = timeout
	return clickhouse.Context(ctx, clickhouse.WithSettings(merged))
}
