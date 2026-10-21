package resourcerole

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestGrantQueriesUseServerScopeMetadata(t *testing.T) {
	catalog := privilegeCatalog{"NEW GLOBAL RIGHT": {Name: "NEW GLOBAL RIGHT", Level: "GLOBAL"}, "SELECT": {Name: "SELECT", Level: "COLUMN"}}
	require.Equal(t, []string{"GRANT NEW GLOBAL RIGHT ON *.* TO `role`", "GRANT CURRENT GRANTS (SELECT ON *.*) TO `role`"}, grantQueries(catalog, "role", []string{"NEW GLOBAL RIGHT", "SELECT"}, "*"))
	require.ErrorContains(t, catalog.validateScope("analytics", []string{"NEW GLOBAL RIGHT"}), "only allowed")
	require.Equal(t, []string{"GRANT SELECT ON `db\\`quoted`.* TO `role\\`quoted`"}, grantQueries(catalog, "role`quoted", []string{"SELECT"}, "db`quoted"))
}

func TestReplacePrivilegesInSingleStatement(t *testing.T) {
	require.Equal(t, "GRANT CURRENT GRANTS (SELECT,OPTIMIZE ON *.*) TO `role` WITH REPLACE OPTION", replacementGrantQuery("role", []string{"SELECT", "OPTIMIZE"}, "*"))
	require.Equal(t, "GRANT SELECT ON `db`.* TO `role` WITH REPLACE OPTION", replacementGrantQuery("role", []string{"SELECT"}, "db"))
}

func TestUnchangedAggregateRightsStayIncremental(t *testing.T) {
	catalog := privilegeCatalog{"ALL": {Name: "ALL"}, "READ": {Name: "READ", Level: "SOURCE"}, "S3": {Name: "S3", Level: "GLOBAL"}}
	for _, name := range []string{"ALL", "READ", "S3"} {
		require.False(t, catalog.replacementRequired([]string{name, "SELECT"}, []string{name, "SELECT", "INSERT"}))
	}
	require.True(t, catalog.replacementRequired([]string{"ALL"}, []string{"SELECT"}))
}
