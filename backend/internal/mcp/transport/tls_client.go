//go:build !win7compat

package transport

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"os"
	"strings"
)

// newBaseTransport materializes the TLS settings.
func newBaseTransport(cfg *TLSConfig) (http.RoundTripper, error) {
	if cfg == nil {
		return http.DefaultTransport, nil
	}
	if cfg.InsecureSkipVerify {
		// Deliberately loud: disabling verification turns mTLS into encryption
		// only and defeats the point of a client-certificate deployment.
		fmt.Fprintln(os.Stderr, "aicli-remote-tools: WARNING tls.insecureSkipVerify=true; server identity is NOT verified")
	}

	tlsConfig := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.InsecureSkipVerify,
	}
	if name := strings.TrimSpace(cfg.ServerName); name != "" {
		tlsConfig.ServerName = name
	}

	if path := strings.TrimSpace(cfg.CAFile); path != "" {
		pem, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read tls.caFile: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("tls.caFile %q contains no PEM certificates", path)
		}
		tlsConfig.RootCAs = pool
	}

	certFile := strings.TrimSpace(cfg.CertFile)
	keyFile := strings.TrimSpace(cfg.KeyFile)
	if (certFile == "") != (keyFile == "") {
		return nil, fmt.Errorf("tls.certFile and tls.keyFile must be set together")
	}
	if certFile != "" {
		certificate, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			return nil, fmt.Errorf("load client certificate: %w", err)
		}
		tlsConfig.Certificates = []tls.Certificate{certificate}
	}

	// Clone the default transport so proxy env vars, dial timeouts and HTTP/2
	// keep their standard behavior.
	if defaultTransport, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := defaultTransport.Clone()
		cloned.TLSClientConfig = tlsConfig
		return cloned, nil
	}
	return &http.Transport{TLSClientConfig: tlsConfig}, nil
}

// ValidateTLS checks one TLS block before a connection is attempted.
//
// It is called by the config loader: a relative CA path or a half-configured
// client certificate is a startup error, not a connection-time surprise.
func ValidateTLS(cfg *TLSConfig) error {
	if cfg == nil {
		return nil
	}
	certFile := strings.TrimSpace(cfg.CertFile)
	keyFile := strings.TrimSpace(cfg.KeyFile)
	if (certFile == "") != (keyFile == "") {
		return fmt.Errorf("tls.certFile and tls.keyFile must be set together")
	}
	for _, pair := range []struct {
		name string
		path string
	}{
		{"tls.caFile", cfg.CAFile},
		{"tls.certFile", cfg.CertFile},
		{"tls.keyFile", cfg.KeyFile},
	} {
		resolved := strings.TrimSpace(pair.path)
		if resolved == "" {
			continue
		}
		if _, err := os.Stat(resolved); err != nil {
			return fmt.Errorf("%s %q is not readable: %w", pair.name, resolved, err)
		}
	}
	return nil
}