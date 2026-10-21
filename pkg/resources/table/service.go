package resourcetable

import (
	"context"
	"fmt"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"

	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

type CHTableService struct {
	CHConnection *driver.Conn
}

func (ts *CHTableService) GetDBTables(ctx context.Context, database string) ([]CHTable, error) {
	query := "SELECT database, name FROM system.tables WHERE database = ?"
	rows, err := (*ts.CHConnection).Query(ctx, query, database)

	if err != nil {
		return nil, fmt.Errorf("reading tables from Clickhouse: %v", err)
	}

	defer rows.Close()
	var tables []CHTable
	for rows.Next() {
		var table CHTable
		err := rows.ScanStruct(&table)
		if err != nil {
			return nil, fmt.Errorf("scanning Clickhouse table row: %w", err)
		}
		tables = append(tables, table)
	}

	return tables, rows.Err()
}

func (ts *CHTableService) GetTable(ctx context.Context, database string, table string) (*CHTable, error) {
	query := "SELECT database, name, toString(uuid) AS uuid, engine_full, engine, comment, sorting_key, primary_key, partition_key, sampling_key, create_table_query FROM system.tables WHERE database = ? AND name = ?"
	row := (*ts.CHConnection).QueryRow(ctx, query, database, table)

	if row.Err() != nil {
		return nil, fmt.Errorf("reading table from Clickhouse: %w", row.Err())
	}

	var chTable CHTable
	err := row.ScanStruct(&chTable)
	if err != nil {
		return nil, fmt.Errorf("scanning Clickhouse table row: %w", err)
	}

	chTable.Columns, err = ts.getTableColumns(ctx, database, table)
	if err != nil {
		return nil, fmt.Errorf("getting columns for Clickhouse table: %v", err)
	}

	return &chTable, nil
}

func (ts *CHTableService) getTableColumns(ctx context.Context, database string, table string) ([]CHColumn, error) {
	query := "SELECT database, table, name, type, default_kind, default_expression, comment, compression_codec FROM system.columns WHERE database = ? AND table = ? ORDER BY position"
	rows, err := (*ts.CHConnection).Query(ctx, query, database, table)

	if err != nil {
		return nil, fmt.Errorf("reading columns from Clickhouse: %v", err)
	}

	defer rows.Close()
	var chColumns []CHColumn
	for rows.Next() {
		var column CHColumn
		err := rows.ScanStruct(&column)
		if err != nil {
			return nil, fmt.Errorf("scanning Clickhouse column row: %v", err)
		}
		chColumns = append(chColumns, column)
	}
	return chColumns, rows.Err()
}

func (ts *CHTableService) CreateTable(ctx context.Context, tableResource TableResource) error {
	if err := validateTable(tableResource); err != nil {
		return err
	}
	query := buildCreateOnClusterSentence(tableResource)
	err := (*ts.CHConnection).Exec(common.ClusterDDLContext(ctx, tableResource.Cluster, nil), query)
	if err != nil {
		return fmt.Errorf("creating Clickhouse table: %v", err)
	}
	return nil
}

func (ts *CHTableService) DeleteTable(ctx context.Context, tableResource TableResource) error {
	query := "DROP TABLE " + qualified(tableResource) + " " + clusterClause(tableResource.Cluster)
	err := (*ts.CHConnection).Exec(common.ClusterDDLContext(ctx, tableResource.Cluster, nil), query)
	if err != nil {
		return fmt.Errorf("deleting Clickhouse table: %v", err)
	}
	return nil
}
