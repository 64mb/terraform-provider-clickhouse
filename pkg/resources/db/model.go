package resourcedb

import (
	resourcetable "github.com/64mb/terraform-provider-clickhouse/pkg/resources/table"
)

type CHDBResources struct {
	CHTables []resourcetable.CHTable
}
