package provider

import (
	"context"
	"fmt"
	"net"
	"os"
	"regexp"
	"strconv"
	"testing"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func TestAccProviderTLS(t *testing.T) {
	addr := os.Getenv("CLICKHOUSE_TLS_TEST_ADDR")
	caFile := os.Getenv("CLICKHOUSE_TLS_TEST_CA_FILE")
	if addr == "" || caFile == "" {
		t.Skip("set CLICKHOUSE_TLS_TEST_ADDR and CLICKHOUSE_TLS_TEST_CA_FILE for native TLS acceptance tests")
	}
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	caPEM, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	tlsConfig, err := providerTLSConfig(true, string(caPEM))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Username: "default"}, TLS: tlsConfig})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	providerConfig := func(ca string) string {
		return fmt.Sprintf(`provider "clickhouse" {
 host = %q
 port = %d
 username = "default"
 password = ""
 secure = true
 default_cluster = ""
 %s
}
`, host, port, ca)
	}
	for _, mode := range []string{"configuration", "environment"} {
		t.Run(mode, func(t *testing.T) {
			ca := fmt.Sprintf("ca_cert = file(%q)", caFile)
			t.Setenv("TF_CLICKHOUSE_CA_CERT", "invalid environment CA must be overridden")
			if mode == "environment" {
				t.Setenv("TF_CLICKHOUSE_CA_CERT", string(caPEM))
				ca = ""
			}
			name := fmt.Sprintf("terraform_tls_%d", time.Now().UnixNano())
			config := providerConfig(ca) + fmt.Sprintf(`resource "clickhouse_db" "tls" {
 name = %q
}
`, name)
			resource.Test(t, resource.TestCase{
				ProviderFactories: ProviderFactories,
				CheckDestroy: func(_ *terraform.State) error {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					var count uint64
					if err := conn.QueryRow(ctx, "SELECT count() FROM system.databases WHERE name = ?", name).Scan(&count); err != nil {
						return err
					}
					if count != 0 {
						return fmt.Errorf("TLS database %s remains after destroy", name)
					}
					return nil
				},
				Steps: []resource.TestStep{
					{Config: config, Check: resource.TestCheckResourceAttr("clickhouse_db.tls", "name", name)},
					{ResourceName: "clickhouse_db.tls", ImportState: true, ImportStateVerify: true},
					{Config: config, PlanOnly: true},
				},
			})
		})
	}
	otherCA, _ := providerTestCertificates(t)
	for _, tc := range []struct{ name, ca string }{{"no_ca", ""}, {"wrong_ca", otherCA}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TF_CLICKHOUSE_CA_CERT", string(caPEM))
			resource.Test(t, resource.TestCase{
				ProviderFactories: ProviderFactories,
				Steps: []resource.TestStep{{
					Config:      providerConfig(fmt.Sprintf("ca_cert = %q", tc.ca)) + `data "clickhouse_dbs" "tls" {}`,
					PlanOnly:    true,
					ExpectError: regexp.MustCompile(`certificate signed by unknown authority|certificate is not trusted`),
				}},
			})
		})
	}
}
