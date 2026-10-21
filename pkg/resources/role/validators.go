package resourcerole

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

var privilegeNamePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*( [A-Za-z][A-Za-z0-9_]*)*$`)

func validPrivilegeName(privilege string) bool {
	if !privilegeNamePattern.MatchString(privilege) {
		return false
	}
	for _, word := range strings.Fields(strings.ToUpper(privilege)) {
		switch word {
		case "ON", "TO", "FROM", "WITH", "GRANT", "REVOKE", "CURRENT", "OPTION":
			return false
		}
	}
	return true
}

func ValidatePrivileges(_ string, privileges *schema.Set) diag.Diagnostics {
	var diagnostics diag.Diagnostics
	for _, value := range privileges.List() {
		privilege := value.(string)
		if !validPrivilegeName(privilege) {
			diagnostics = append(diagnostics, diag.Diagnostic{Severity: diag.Error, Summary: "invalid privilege name", Detail: fmt.Sprintf("%q must be a privilege name without SQL clauses, punctuation or surrounding whitespace", privilege)})
		}
	}
	return diagnostics
}
