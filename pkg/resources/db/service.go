package resourcedb

import (
	"context"
	"fmt"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"

	resourcetable "github.com/64mb/terraform-provider-clickhouse/pkg/resources/table"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type CHDBService struct {
	CHConnection   *driver.Conn
	CHTableService *resourcetable.CHTableService
}

func (ts *CHDBService) GetDBResources(ctx context.Context, database string, cluster string) (*CHDBResources, error) {
	var dbResources CHDBResources
	var err error
	if cluster != "" {
		rows, err := (*ts.CHConnection).Query(ctx, "SELECT DISTINCT name FROM clusterAllReplicas(?, system.tables) WHERE database = ? SETTINGS skip_unavailable_shards = 0", common.ClusterName(cluster), database)
		if err != nil {
			return nil, fmt.Errorf("checking database tables across cluster: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var table resourcetable.CHTable
			if err := rows.Scan(&table.Name); err != nil {
				return nil, fmt.Errorf("reading cluster table: %w", err)
			}
			dbResources.CHTables = append(dbResources.CHTables, table)
		}
		return &dbResources, rows.Err()
	}

	dbResources.CHTables, err = ts.CHTableService.GetDBTables(ctx, database)
	if err != nil {
		return nil, fmt.Errorf("error getting tables from database: %v", err)
	}

	return &dbResources, nil
}
