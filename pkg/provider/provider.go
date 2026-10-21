package provider

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/64mb/terraform-provider-clickhouse/pkg/datasources"
	"github.com/64mb/terraform-provider-clickhouse/pkg/resources/db"
	"github.com/64mb/terraform-provider-clickhouse/pkg/resources/role"
	"github.com/64mb/terraform-provider-clickhouse/pkg/resources/table"
	"github.com/64mb/terraform-provider-clickhouse/pkg/resources/user"
	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/hashicorp/terraform-plugin-sdk/v2/diag"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/validation"
	"github.com/joho/godotenv"
)

func init() {
	// Set descriptions to support markdown syntax, this will be used in document generation
	// and the language server.
	schema.DescriptionKind = schema.StringMarkdown

	// Customize the content of descriptions when output. For example you can add defaults on
	// to the exported descriptions if present.
	// schema.SchemaDescriptionBuilder = func(s *schema.Schema) string {
	// 	desc := s.Description
	// 	if s.Default != nil {
	// 		desc += fmt.Sprintf(" Defaults to `%v`.", s.Default)
	// 	}
	// 	return strings.TrimSpace(desc)
	// }
}

func New(version string) func() *schema.Provider {
	return func() *schema.Provider {
		p := &schema.Provider{
			Schema: map[string]*schema.Schema{
				"default_cluster": &schema.Schema{
					Description: "Default cluster, if provided will be used when no cluster is provided",
					Type:        schema.TypeString,
					Optional:    true,
					Default:     "",
				},
				"username": {
					Description: "Clickhouse username with admin privileges",
					Type:        schema.TypeString,
					Optional:    true,
					DefaultFunc: func() (any, error) {
						return getEnvVar("TF_CLICKHOUSE_USERNAME")
					},
				},
				"password": {
					Description: "Clickhouse user password with admin privileges",
					Type:        schema.TypeString,
					Optional:    true,
					Sensitive:   true,
					DefaultFunc: func() (any, error) {
						if password, _ := getEnvVar("TF_CLICKHOUSE_PASSWORD"); password != nil {
							return password, nil
						}
						return "", nil
					},
				},
				"host": {
					Description:  "Clickhouse server url",
					Type:         schema.TypeString,
					Required:     true,
					Sensitive:    true,
					ValidateFunc: validation.StringIsNotWhiteSpace,
					DefaultFunc: func() (any, error) {
						return getEnvVar("TF_CLICKHOUSE_HOST")
					},
				},
				"port": {
					Description:  "Clickhouse server native protocol port (TCP)",
					Type:         schema.TypeInt,
					Required:     true,
					ValidateFunc: validation.IntBetween(1, 65535),
					DefaultFunc: func() (any, error) {
						return getEnvVar("TF_CLICKHOUSE_PORT")
					},
				},
				"secure": {
					Description: "Clickhouse secure connection",
					Type:        schema.TypeBool,
					Optional:    true,
					Default:     false,
				},
			},
			DataSourcesMap: map[string]*schema.Resource{
				"clickhouse_dbs": datasources.DataSourceDbs(),
			},
			ResourcesMap: map[string]*schema.Resource{
				"clickhouse_db":    resourcedb.ResourceDb(),
				"clickhouse_table": resourcetable.ResourceTable(),
				"clickhouse_role":  resourcerole.ResourceRole(),
				"clickhouse_user":  resourceuser.ResourceUser(),
			},
			ConfigureContextFunc: configure(),
		}

		return p
	}
}

func getEnvVar(envVarName string) (any, error) {

	godotenv.Load(".env")
	if v := os.Getenv(envVarName); v != "" {
		return v, nil
	}
	return nil, errors.New(fmt.Sprintf("Env var %v not present", envVarName))

}

func configure() func(context.Context, *schema.ResourceData) (any, diag.Diagnostics) {
	return func(ctx context.Context, d *schema.ResourceData) (any, diag.Diagnostics) {
		host := d.Get("host").(string)
		port := d.Get("port").(int)
		username := d.Get("username").(string)
		defaultCluster := d.Get("default_cluster").(string)
		password := d.Get("password").(string)
		secure := d.Get("secure").(bool)

		unknown := unknownConfiguration(d)
		if !containsAttribute(unknown, "host") && strings.TrimSpace(host) == "" {
			return nil, diag.Errorf("clickhouse host must not be empty")
		}
		if !containsAttribute(unknown, "port") && (port < 1 || port > 65535) {
			return nil, diag.Errorf("clickhouse port must be between 1 and 65535")
		}

		var tlsConfig *tls.Config
		if secure {
			tlsConfig = &tls.Config{
				InsecureSkipVerify: false,
			}
		}
		options := &clickhouse.Options{
			Addr: []string{net.JoinHostPort(host, strconv.Itoa(port))},
			Auth: clickhouse.Auth{
				Username: username,
				Password: password,
			},
			Settings: clickhouse.Settings{
				"max_execution_time": 30,
			},
			TLS: tlsConfig,
		}
		if len(unknown) != 0 {
			// unknown configuration must never fall back to driver defaults during a read
			options.DialContext = func(context.Context, string) (net.Conn, error) {
				return nil, fmt.Errorf("clickhouse provider configuration is not yet known: %s, make the endpoint and credentials available before reading existing resources or data sources", strings.Join(unknown, ", "))
			}
		}
		// [clickhouse.Open] creates a pool without dialing, operations connect when needed
		conn, err := clickhouse.Open(options)

		if err != nil {
			return nil, diag.FromErr(fmt.Errorf("error connecting to clickhouse: %v", err))
		}

		return &common.ApiClient{ClickhouseConnection: &conn, DefaultCluster: defaultCluster}, nil
	}
}

func unknownConfiguration(d *schema.ResourceData) []string {
	var unknown []string
	// this SDK does not retain provider raw config, [GetOkExists] preserves unknown primitive values
	for _, name := range []string{"host", "port", "secure"} {
		if _, known := d.GetOkExists(name); !known {
			unknown = append(unknown, name)
		}
	}
	return unknown
}

func containsAttribute(attributes []string, name string) bool {
	for _, attribute := range attributes {
		if attribute == name {
			return true
		}
	}
	return false
}
