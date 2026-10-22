package provider

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/64mb/terraform-provider-clickhouse/pkg/common"
	"github.com/hashicorp/terraform-plugin-sdk/v2/helper/schema"
)

func TestProviderCACertSchema(t *testing.T) {
	p := New("test")()
	field, ok := p.Schema["ca_cert"]
	if !ok {
		t.Fatal("provider must expose ca_cert")
	}
	if field.Type != schema.TypeString || !field.Optional {
		t.Fatal("ca_cert must be an optional string")
	}
	t.Setenv("TF_CLICKHOUSE_CA_CERT", "")
	d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"username": "default", "host": "localhost", "port": 9000})
	if got := d.Get("ca_cert"); got != "" {
		t.Fatalf("empty default = %q", got)
	}
	t.Setenv("TF_CLICKHOUSE_CA_CERT", "environment CA")
	d = schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"username": "default", "host": "localhost", "port": 9000})
	if got := d.Get("ca_cert"); got != "environment CA" {
		t.Fatalf("environment default = %q", got)
	}
	d = schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"username": "default", "host": "localhost", "port": 9000, "ca_cert": "explicit CA"})
	if got := d.Get("ca_cert"); got != "explicit CA" {
		t.Fatalf("explicit configuration = %q", got)
	}
	d = schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"username": "default", "host": "localhost", "port": 9000, "ca_cert": ""})
	if got := d.Get("ca_cert"); got != "" {
		t.Fatalf("explicit empty configuration = %q", got)
	}
}

func TestProviderCACertConfigurationErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		secure bool
		ca     string
	}{
		{"requires secure", false, "certificate"},
		{"invalid PEM", true, "not a certificate"},
		{"invalid certificate", true, "-----BEGIN CERTIFICATE-----\nbm90IGEgY2VydGlmaWNhdGU=\n-----END CERTIFICATE-----"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := New("test")()
			d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"username": "default", "host": "localhost", "port": 9000, "secure": tc.secure, "ca_cert": tc.ca})
			client, diags := p.ConfigureContextFunc(context.Background(), d)
			if !diags.HasError() {
				t.Fatalf("expected configuration error, got client %T and diagnostics %v", client, diags)
			}
		})
	}
}

func TestProviderTLSDefaults(t *testing.T) {
	plain, err := providerTLSConfig(false, "")
	if err != nil || plain != nil {
		t.Fatalf("plaintext TLS config = %v, %v", plain, err)
	}
	secure, err := providerTLSConfig(true, "")
	if err != nil {
		t.Fatal(err)
	}
	if secure == nil || secure.RootCAs != nil || secure.InsecureSkipVerify {
		t.Fatalf("unexpected default TLS configuration: %+v", secure)
	}
}

func TestProviderTLSAppendsSystemRoots(t *testing.T) {
	ca, _ := providerTestCertificates(t)
	otherCA, _ := providerTestCertificates(t)
	systemRoots, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	expected := systemRoots.Clone()
	if !expected.AppendCertsFromPEM([]byte(ca + otherCA)) {
		t.Fatal("invalid test certificate bundle")
	}
	cfg, err := providerTLSConfig(true, ca+otherCA)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RootCAs == nil || !cfg.RootCAs.Equal(expected) {
		t.Fatal("custom CA bundle must extend all system roots")
	}
	fresh, err := x509.SystemCertPool()
	if err != nil {
		t.Fatal(err)
	}
	if !fresh.Equal(systemRoots) {
		t.Fatal("provider configuration must not mutate process-wide roots")
	}
}

func TestProviderTLSVerification(t *testing.T) {
	ca, server := providerTestCertificates(t)
	otherCA, _ := providerTestCertificates(t)
	for _, tc := range []struct {
		name, ca, hostname string
		wantError          bool
	}{
		{"trusted CA", ca, "localhost", false},
		{"untrusted CA", otherCA, "localhost", true},
		{"wrong hostname", ca, "wrong.example", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := providerTLSConfig(true, tc.ca)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.InsecureSkipVerify {
				t.Fatal("certificate verification must remain enabled")
			}
			cfg.ServerName = tc.hostname
			listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{server}})
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				_ = conn.(*tls.Conn).Handshake()
			}()
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 5 * time.Second}, "tcp", listener.Addr().String(), cfg)
			if conn != nil {
				conn.Close()
			}
			<-finished
			if (err != nil) != tc.wantError {
				t.Fatalf("TLS handshake error = %v, want error %v", err, tc.wantError)
			}
		})
	}
}

func TestProviderCACertConfigureIsLazy(t *testing.T) {
	ca, _ := providerTestCertificates(t)
	p := New("test")()
	d := schema.TestResourceDataRaw(t, p.Schema, map[string]interface{}{"host": "unresolvable.invalid", "port": 9440, "username": "default", "secure": true, "ca_cert": ca})
	client, diags := p.ConfigureContextFunc(context.Background(), d)
	if diags.HasError() || client == nil {
		t.Fatalf("configuration must not dial: client %T, diagnostics %v", client, diags)
	}
	defer (*client.(*common.ApiClient).ClickhouseConnection).Close()
}

func providerTestCertificates(t *testing.T) (string, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "provider test CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), DNSNames: []string{"localhost"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})), tls.Certificate{Certificate: [][]byte{serverDER}, PrivateKey: serverKey}
}
