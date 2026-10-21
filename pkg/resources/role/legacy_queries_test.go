package resourcerole

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLegacyGrantAndRevokeScopes(t *testing.T) {
	catalog := privilegeCatalog{}
	for _, name := range []string{"REMOTE", "S3", "AZURE", "HDFS"} {
		catalog[name] = privilegeDefinition{Name: name, Level: "GLOBAL"}
	}
	for _, name := range []string{"SELECT", "INSERT"} {
		catalog[name] = privilegeDefinition{Name: name, Level: "COLUMN"}
	}
	for _, tc := range []struct {
		name       string
		database   string
		privileges []string
		grants     []string
		revokes    []string
	}{
		{"remote", "*", []string{"REMOTE"}, []string{"GRANT REMOTE ON *.* TO `role`"}, []string{"REVOKE REMOTE ON *.* FROM `role`"}},
		{"s3", "*", []string{"S3"}, []string{"GRANT S3 ON *.* TO `role`"}, []string{"REVOKE S3 ON *.* FROM `role`"}},
		{"multiple_global", "*", []string{"REMOTE", "S3"}, []string{"GRANT REMOTE,S3 ON *.* TO `role`"}, []string{"REVOKE REMOTE,S3 ON *.* FROM `role`"}},
		{"database", "mydb", []string{"SELECT", "INSERT"}, []string{"GRANT SELECT,INSERT ON `mydb`.* TO `role`"}, []string{"REVOKE INSERT,SELECT ON `mydb`.* FROM `role`"}},
		{"system", "system", []string{"SELECT"}, []string{"GRANT CURRENT GRANTS (SELECT ON `system`.*) TO `role`"}, []string{"REVOKE SELECT ON `system`.* FROM `role`"}},
		{"wildcard", "*", []string{"SELECT"}, []string{"GRANT CURRENT GRANTS (SELECT ON *.*) TO `role`"}, []string{"REVOKE SELECT ON *.* FROM `role`"}},
		{"mixed", "*", []string{"REMOTE", "SELECT"}, []string{"GRANT REMOTE ON *.* TO `role`", "GRANT CURRENT GRANTS (SELECT ON *.*) TO `role`"}, []string{"REVOKE REMOTE,SELECT ON *.* FROM `role`"}},
		{"all_legacy_expandable", "*", []string{"REMOTE", "S3", "AZURE", "HDFS"}, []string{"GRANT REMOTE,S3,AZURE,HDFS ON *.* TO `role`"}, []string{"REVOKE AZURE,HDFS,REMOTE,S3 ON *.* FROM `role`"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			queries := grantQueries(catalog, "role", tc.privileges, tc.database)
			require.Equal(t, tc.grants, queries)
			var grants []CHGrant
			for _, query := range queries {
				query = strings.Replace(query, "GRANT CURRENT GRANTS (", "GRANT ", 1)
				query = strings.Replace(query, ") TO ", " TO ", 1)
				parsed, err := parseGrantStatement(query, "role")
				require.NoError(t, err)
				grants = append(grants, parsed...)
			}
			require.Equal(t, tc.revokes, revokeQueries(catalog, "role", grants, nil, tc.database))
			require.Empty(t, revokeQueries(catalog, "role", grants, tc.privileges, tc.database))
		})
	}
}
