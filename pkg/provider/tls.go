package provider

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
)

func providerTLSConfig(secure bool, caCert string) (*tls.Config, error) {
	if caCert != "" && !secure {
		return nil, fmt.Errorf("ca_cert requires secure = true")
	}
	if !secure {
		return nil, nil
	}
	config := &tls.Config{}
	if caCert == "" {
		return config, nil
	}
	// extend the system trust store without changing process-wide certificate settings
	roots, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("load system CA certificates for ca_cert: %w", err)
	}
	if !roots.AppendCertsFromPEM([]byte(caCert)) {
		return nil, fmt.Errorf("ca_cert must contain at least one valid PEM certificate")
	}
	config.RootCAs = roots
	return config, nil
}
