package resourcedb

import (
	"context"
	"fmt"

	resourcetable "github.com/64mb/terraform-provider-clickhouse/pkg/resources/table"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type CHDBService struct {
	CHConnection   *driver.Conn
	CHTableService *resourcetable.CHTableService
}

func (ts *CHDBService) GetDBResources(ctx context.Context, database string) (*CHDBResources, error) {
	var dbResources CHDBResources
	var err error

	dbResources.CHTables, err = ts.CHTableService.GetDBTables(ctx, database)
	if err != nil {
		return nil, fmt.Errorf("error getting tables from database: %v", err)
	}

	return &dbResources, nil
}
