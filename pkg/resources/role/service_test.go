package resourcerole

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseGrantStatement(t *testing.T) {
	tests := []struct {
		name           string
		statement      string
		roleName       string
		expectedGrants []CHGrant
		expectError    bool
		errorContains  string
	}{
		{
			name:      "global privilege - REMOTE",
			statement: "GRANT REMOTE ON *.* TO test_role",
			roleName:  "test_role",
			expectedGrants: []CHGrant{
				{RoleName: "test_role", AccessType: "REMOTE", Database: "*", Scope: "*.*"},
			},
			expectError: false,
		},
		{
			name:      "global privilege - S3",
			statement: "GRANT S3 ON *.* TO test_role",
			roleName:  "test_role",
			expectedGrants: []CHGrant{
				{RoleName: "test_role", AccessType: "S3", Database: "*", Scope: "*.*"},
			},
			expectError: false,
		},
		{
			name:      "database privilege - single",
			statement: "GRANT SELECT ON mydb.* TO test_role",
			roleName:  "test_role",
			expectedGrants: []CHGrant{
				{RoleName: "test_role", AccessType: "SELECT", Database: "mydb", Scope: "mydb.*"},
			},
			expectError: false,
		},
		{
			name:      "database privilege - multiple",
			statement: "GRANT SELECT, INSERT ON mydb.* TO test_role",
			roleName:  "test_role",
			expectedGrants: []CHGrant{
				{RoleName: "test_role", AccessType: "SELECT", Database: "mydb", Scope: "mydb.*"},
				{RoleName: "test_role", AccessType: "INSERT", Database: "mydb", Scope: "mydb.*"},
			},
			expectError: false,
		},
		{
			name:      "system database privilege",
			statement: "GRANT SELECT ON system.* TO test_role",
			roleName:  "test_role",
			expectedGrants: []CHGrant{
				{RoleName: "test_role", AccessType: "SELECT", Database: "system", Scope: "system.*"},
			},
			expectError: false,
		},
		{
			name:          "invalid statement - missing GRANT",
			statement:     "SELECT ON *.* TO test_role",
			roleName:      "test_role",
			expectError:   true,
			errorContains: "invalid grant statement",
		},
		{
			name:          "invalid statement - missing ON",
			statement:     "GRANT REMOTE *.* TO test_role",
			roleName:      "test_role",
			expectError:   true,
			errorContains: "missing ' ON '",
		},
		{
			name:          "invalid statement - missing TO",
			statement:     "GRANT REMOTE ON *.*",
			roleName:      "test_role",
			expectError:   true,
			errorContains: "missing ' TO '",
		},
		{
			name:          "table-level grant not supported",
			statement:     "GRANT SELECT ON mydb.mytable TO test_role",
			roleName:      "test_role",
			expectError:   true,
			errorContains: "table-level grants are not supported",
		},
		// REVOKE statement test cases - these should fail with "missing GRANT"
		{
			name:          "REVOKE statement - database level",
			statement:     "REVOKE SELECT ON system.system_tables FROM test_role",
			roleName:      "test_role",
			expectError:   true,
			errorContains: "missing GRANT",
		},
		{
			name:          "REVOKE statement - table level",
			statement:     "REVOKE SELECT ON system.system_tables_tracking_auburn_qf_97 FROM system_read",
			roleName:      "system_read",
			expectError:   true,
			errorContains: "missing GRANT",
		},
		{
			name:          "REVOKE statement - global",
			statement:     "REVOKE REMOTE ON *.* FROM test_role",
			roleName:      "test_role",
			expectError:   true,
			errorContains: "missing GRANT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			grants, err := parseGrantStatement(tt.statement, tt.roleName)

			if tt.expectError {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errorContains)
				assert.Nil(t, grants)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedGrants, grants)
			}
		})
	}
}

func TestParseGrantQuotedIdentifiers(t *testing.T) {
	grants, err := parseGrantStatement("GRANT SELECT ON `db TO other\\`name`.* TO `role ON other\\`name`", "role ON other`name")
	require.NoError(t, err)
	require.Equal(t, "db TO other`name", grants[0].Database)
	require.Equal(t, "role ON other`name", grants[0].RoleName)
	unicodeGrants, err := parseGrantStatement("grant SELECT on `db ı TO other`.* to `role ı ON other`", "role ı ON other")
	require.NoError(t, err)
	require.Equal(t, "db ı TO other", unicodeGrants[0].Database)
	grants, err = parseGrantStatement("GRANT CREATE USER ON * TO `role`", "role")
	require.NoError(t, err)
	require.Equal(t, "*", grants[0].Database)
	require.Equal(t, "*", grants[0].Scope)
	for _, statement := range []string{
		"GRANT SELECT ON db.* TO role WITH GRANT OPTION",
		"GRANT SELECT ON db.* TO role; DROP ROLE other",
		"GRANT SELECT(x) ON db.* TO role",
		"GRANT SELECT ON `unterminated.* TO role",
	} {
		_, err := parseGrantStatement(statement, "role")
		require.Error(t, err)
	}
}
