package resourcerole_test

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/provider"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestAccRolePrivilegeLifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") != "1" {
		t.Skip("set TF_ACC=1 to run Terraform acceptance tests")
	}
	for _, scope := range []string{"default", "*"} {
		t.Run(scope, func(t *testing.T) {
			name := fmt.Sprintf("role_lifecycle_%d", time.Now().UnixNano())
			config := func(name, database, privileges string) string {
				return fmt.Sprintf(`provider "clickhouse" {}
resource "clickhouse_role" "test" {
 name = %q
 database = %q
 privileges = %s
}`, name, database, privileges)
			}
			factories := map[string]func() (*schema.Provider, error){"clickhouse": func() (*schema.Provider, error) { return provider.New("test")(), nil }}
			initial := config(name, scope, `["ALL"]`)
			updated := config(name, scope, `["OPTIMIZE", "SELECT"]`)
			renamed := config(name+"_renamed", "default", `["ALTER", "OPTIMIZE"]`)
			sources := config(name, "*", `["REMOTE", "S3", "SELECT"]`)
			resource.Test(t, resource.TestCase{ProviderFactories: factories, Steps: []resource.TestStep{
				{Config: initial, Check: resource.TestCheckResourceAttr("clickhouse_role.test", "privileges.#", "1")},
				{Config: initial, PlanOnly: true},
				{Config: updated, Check: resource.TestCheckResourceAttr("clickhouse_role.test", "privileges.#", "2")},
				{Config: updated, PlanOnly: true},
				{Config: renamed},
				{Config: renamed, PlanOnly: true},
				{Config: sources},
				{Config: sources, PlanOnly: true},
				{Config: config(name, "*", `[]`), Check: resource.TestCheckResourceAttr("clickhouse_role.test", "privileges.#", "0")},
				{Config: config(name, "*", `[]`), PlanOnly: true},
			}})
		})
	}
}
