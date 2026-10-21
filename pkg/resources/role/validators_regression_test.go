package resourcerole

import (
	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestPrivilegeNamesFollowServerVocabulary(t *testing.T) {
	for _, privilege := range []string{"ALL", "OPTIMIZE", "SYSTEM DROP QUERY CACHE", "READ", "dictGet", "FUTURE VALID PRIVILEGE"} {
		t.Run(privilege, func(t *testing.T) {
			require.False(t, ValidatePrivileges("*", common.StringListToSet([]string{privilege})).HasError())
		})
	}
}

func TestPrivilegeNamesRejectSQLFragments(t *testing.T) {
	for _, privilege := range []string{"", " SELECT", "SELECT ", "SELECT, INSERT", "SELECT ON *.* TO other", "SELECT; DROP ROLE other", "SELECT -- comment", "SELECT/*comment*/", "SELECT(x)", "SELECT\nINSERT", "SELECT WITH GRANT OPTION"} {
		t.Run(privilege, func(t *testing.T) {
			require.True(t, ValidatePrivileges("*", common.StringListToSet([]string{privilege})).HasError())
		})
	}
}
