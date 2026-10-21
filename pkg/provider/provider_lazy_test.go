package provider

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/hashicorp/go-cty/cty"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/resource"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/terraform"
)

func unavailablePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestConfigureDefersConnection(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("secure=%t", secure), func(t *testing.T) {
			p := New("test")()
			data := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{
				"host": "127.0.0.1", "port": unavailablePort(t), "username": "default",
				"password": "", "secure": secure, "default_cluster": "test_cluster",
			})
			meta, diagnostics := p.ConfigureContextFunc(context.Background(), data)
			if diagnostics.HasError() {
				t.Fatalf("configuration must not connect: %v", diagnostics)
			}
			client, ok := meta.(*common.ApiClient)
			if !ok || client.ClickhouseConnection == nil {
				t.Fatalf("expected configured client, got %#v", meta)
			}
			defer (*client.ClickhouseConnection).Close()
			if client.DefaultCluster != "test_cluster" {
				t.Fatalf("cluster = %q", client.DefaultCluster)
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := (*client.ClickhouseConnection).Exec(ctx, "SELECT 1")
			var networkError *net.OpError
			if !errors.As(err, &networkError) || networkError.Op != "dial" {
				t.Fatalf("first query must report a connection error, got %v", err)
			}
		})
	}
}

const lazyResources = `
resource "clickhouse_db" "test" { name = "lazy_plan_db" }
resource "clickhouse_role" "test" {
 name = "lazy_plan_role"
 database = "default"
}
resource "clickhouse_user" "test" {
 name = "lazy_plan_user"
 password = "test"
}
resource "clickhouse_table" "test" {
 database = "default"
 name = "lazy_plan_table"
 engine = "MergeTree"
 order_by = ["id"]
 column {
  name = "id"
  type = "UInt64"
 }
}
`

func TestAccProviderPlanUnavailableEndpoint(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC is not set")
	}
	resource.Test(t, resource.TestCase{
		ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`provider "clickhouse" {
 host = "127.0.0.1"
 port = %d
 username = "default"
 password = ""
}
%s`, unavailablePort(t), lazyResources),
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

func bootstrapProvider() *schema.Provider {
	return &schema.Provider{ResourcesMap: map[string]*schema.Resource{
		"bootstrap_endpoint": {
			Schema: map[string]*schema.Schema{
				"host": {Type: schema.TypeString, Computed: true},
				"port": {Type: schema.TypeInt, Computed: true},
			},
			CreateContext: func(_ context.Context, _ *schema.ResourceData, _ interface{}) diag.Diagnostics {
				return diag.Errorf("bootstrap endpoint must not be created during a plan")
			},
			ReadContext: func(context.Context, *schema.ResourceData, interface{}) diag.Diagnostics { return nil },
			DeleteContext: func(_ context.Context, d *schema.ResourceData, _ interface{}) diag.Diagnostics {
				d.SetId("")
				return nil
			},
		},
	}}
}

func TestAccProviderPlanUnknownEndpoint(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC is not set")
	}
	factories := map[string]func() (*schema.Provider, error){
		"clickhouse": ProviderFactories["clickhouse"],
		"bootstrap":  func() (*schema.Provider, error) { return bootstrapProvider(), nil },
	}
	resource.Test(t, resource.TestCase{
		ProviderFactories: factories,
		Steps: []resource.TestStep{{
			Config: `resource "bootstrap_endpoint" "test" {}
provider "clickhouse" {
 host = bootstrap_endpoint.test.host
 port = bootstrap_endpoint.test.port
 username = "default"
 password = ""
}
` + lazyResources,
			PlanOnly:           true,
			ExpectNonEmptyPlan: true,
		}},
	})
}

func TestAccProviderBootstrapApply(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC is not set")
	}
	if testing.Short() {
		t.Skip("local ClickHouse acceptance test")
	}
	var listener net.Listener
	var listenerMu sync.Mutex
	stop := func() {
		listenerMu.Lock()
		defer listenerMu.Unlock()
		if listener != nil {
			_ = listener.Close()
			listener = nil
		}
	}
	t.Cleanup(stop)
	port := unavailablePort(t)
	bootstrap := func() (*schema.Provider, error) {
		p := bootstrapProvider()
		p.ResourcesMap["bootstrap_endpoint"].CreateContext = func(_ context.Context, d *schema.ResourceData, _ interface{}) diag.Diagnostics {
			endpoint, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
			if err != nil {
				return diag.FromErr(err)
			}
			listenerMu.Lock()
			listener = endpoint
			listenerMu.Unlock()
			go func() {
				for {
					downstream, err := endpoint.Accept()
					if err != nil {
						return
					}
					go func() {
						defer downstream.Close()
						upstream, err := net.DialTimeout("tcp", "127.0.0.1:19000", time.Second)
						if err != nil {
							return
						}
						defer upstream.Close()
						done := make(chan struct{})
						go func() { _, _ = io.Copy(upstream, downstream); _ = upstream.Close(); close(done) }()
						_, _ = io.Copy(downstream, upstream)
						_ = downstream.Close()
						<-done
					}()
				}
			}()
			d.SetId("local-clickhouse")
			if err := d.Set("host", "127.0.0.1"); err != nil {
				return diag.FromErr(err)
			}
			return diag.FromErr(d.Set("port", port))
		}
		p.ResourcesMap["bootstrap_endpoint"].DeleteContext = func(_ context.Context, d *schema.ResourceData, _ interface{}) diag.Diagnostics {
			stop()
			d.SetId("")
			return nil
		}
		return p, nil
	}
	resource.Test(t, resource.TestCase{
		PreCheck: func() {
			conn, err := net.DialTimeout("tcp", "127.0.0.1:19000", time.Second)
			if err != nil {
				t.Fatalf("start the local ClickHouse fixture on 127.0.0.1:19000: %v", err)
			}
			_ = conn.Close()
		},
		ProviderFactories: map[string]func() (*schema.Provider, error){
			"clickhouse": ProviderFactories["clickhouse"], "bootstrap": bootstrap,
		},
		Steps: []resource.TestStep{{
			Config: `resource "bootstrap_endpoint" "test" {}
provider "clickhouse" {
 host = bootstrap_endpoint.test.host
 port = bootstrap_endpoint.test.port
 username = "default"
 password = ""
}
` + lazyResources,
			Check: resource.ComposeTestCheckFunc(
				resource.TestCheckResourceAttr("clickhouse_db.test", "name", "lazy_plan_db"),
				resource.TestCheckResourceAttr("clickhouse_role.test", "name", "lazy_plan_role"),
				resource.TestCheckResourceAttr("clickhouse_user.test", "name", "lazy_plan_user"),
				resource.TestCheckResourceAttr("clickhouse_table.test", "name", "lazy_plan_table"),
			),
		}},
	})
}

func TestConfigureUnknownValues(t *testing.T) {
	for _, attribute := range []string{"host", "port", "secure"} {
		t.Run(attribute, func(t *testing.T) {
			p := New("test")()
			values := map[string]cty.Value{
				"host": cty.StringVal("127.0.0.1"), "port": cty.NumberIntVal(9000),
				"username": cty.StringVal("default"), "password": cty.StringVal(""),
				"secure": cty.BoolVal(false), "default_cluster": cty.StringVal(""),
			}
			values[attribute] = cty.UnknownVal(values[attribute].Type())
			config := terraform.NewResourceConfigShimmed(cty.ObjectVal(values), (&schema.Resource{Schema: p.Schema}).CoreConfigSchema())
			diagnostics := p.Configure(context.Background(), config)
			if diagnostics.HasError() {
				t.Fatalf("unknown configuration must allow planning: %v", diagnostics)
			}
			client, ok := p.Meta().(*common.ApiClient)
			if !ok || client.ClickhouseConnection == nil {
				t.Fatalf("expected deferred client, got %#v", p.Meta())
			}
			defer (*client.ClickhouseConnection).Close()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			err := (*client.ClickhouseConnection).Exec(ctx, "SELECT 1")
			if err == nil || !strings.Contains(err.Error(), "configuration is not yet known") || !strings.Contains(err.Error(), attribute) {
				t.Fatalf("expected unresolved [%s] diagnostic without dialing a fallback endpoint, got %v", attribute, err)
			}
		})
	}
}

func TestProviderEndpointValidation(t *testing.T) {
	tests := []struct {
		name      string
		host      string
		port      int
		wantError bool
	}{
		{name: "empty host", host: "", port: 9000, wantError: true},
		{name: "whitespace host", host: " \t\n", port: 9000, wantError: true},
		{name: "zero port", host: "127.0.0.1", port: 0, wantError: true},
		{name: "negative port", host: "127.0.0.1", port: -1, wantError: true},
		{name: "port above maximum", host: "127.0.0.1", port: 65536, wantError: true},
		{name: "minimum port", host: "127.0.0.1", port: 1},
		{name: "native port", host: "127.0.0.1", port: 9000},
		{name: "secure native port", host: "127.0.0.1", port: 9440},
		{name: "maximum port", host: "127.0.0.1", port: 65535},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diagnostics := New("test")().Validate(terraform.NewResourceConfigRaw(map[string]interface{}{
				"host": tt.host, "port": tt.port, "username": "default", "password": "",
			}))
			if diagnostics.HasError() != tt.wantError {
				t.Fatalf("validation errors = %t, want %t: %v", diagnostics.HasError(), tt.wantError, diagnostics)
			}
		})
	}
}

func TestAccProviderPlanDataSourceRequiresConnection(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("TF_ACC is not set")
	}
	port := unavailablePort(t)
	resource.Test(t, resource.TestCase{
		ProviderFactories: ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`provider "clickhouse" {
 host = "127.0.0.1"
 port = %d
 username = "default"
 password = ""
}
data "clickhouse_dbs" "test" {}
output "databases" { value = data.clickhouse_dbs.test.dbs }
`, port),
			PlanOnly:    true,
			ExpectError: regexp.MustCompile(fmt.Sprintf(`dial tcp 127\.0\.0\.1:%d:`, port)),
		}},
	})
}
