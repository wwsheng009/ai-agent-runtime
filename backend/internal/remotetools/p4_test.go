package remotetools_test

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
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/wwsheng009/ai-agent-runtime/internal/remotetools"
)

// testPKI holds a throwaway CA plus a server and a client certificate.
type testPKI struct {
	caPEM      []byte
	caCert     *x509.Certificate
	serverCert tls.Certificate
	clientCert tls.Certificate
}

// newTestPKI builds a CA and two leaves.
func newTestPKI(t *testing.T) testPKI {
	t.Helper()

	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("ca key: %v", err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("ca cert: %v", err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}

	issue := func(commonName string, usage x509.ExtKeyUsage, dnsNames []string, ips []string) tls.Certificate {
		key, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if keyErr != nil {
			t.Fatalf("leaf key: %v", keyErr)
		}
		template := &x509.Certificate{
			SerialNumber: big.NewInt(time.Now().UnixNano()),
			Subject:      pkix.Name{CommonName: commonName},
			NotBefore:    time.Now().Add(-time.Hour),
			NotAfter:     time.Now().Add(24 * time.Hour),
			KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
			ExtKeyUsage:  []x509.ExtKeyUsage{usage},
			DNSNames:     dnsNames,
		}
		for _, raw := range ips {
			if ip := parseIP(raw); ip != nil {
				template.IPAddresses = append(template.IPAddresses, ip)
			}
		}
		der, certErr := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
		if certErr != nil {
			t.Fatalf("leaf cert: %v", certErr)
		}
		return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
	}

	return testPKI{
		caPEM:      pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		caCert:     caCert,
		serverCert: issue("localhost", x509.ExtKeyUsageServerAuth, []string{"localhost"}, []string{"127.0.0.1"}),
		clientCert: issue("test-client", x509.ExtKeyUsageClientAuth, nil, nil),
	}
}

// writeCertPEM writes one certificate to a temp file.
func writeCertPEM(t *testing.T, dir, name string, der []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// writeKeyPEM writes an EC private key.
func writeKeyPEM(t *testing.T, dir, name string, key interface{}) string {
	t.Helper()
	privateKey, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatalf("unexpected key type %T", key)
	}
	der, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	path := filepath.Join(dir, name)
	encoded := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// startTLSRemoteServer launches the remote server over HTTPS and returns its address.
func startTLSRemoteServer(t *testing.T, cfg *remotetools.Config) (string, *remotetools.Server) {
	t.Helper()
	server, err := remotetools.NewServer(cfg, "dev")
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	t.Cleanup(func() { _ = server.Close() })

	addrCh := make(chan string, 1)
	go func() {
		_ = server.RunHTTPWithListener(ctx, func(addr string) { addrCh <- addr })
	}()
	select {
	case addr := <-addrCh:
		return addr, server
	case <-time.After(5 * time.Second):
		t.Fatal("http server did not report a listener")
		return "", nil
	}
}

// TestMutualTLSAuthorizesWithoutToken proves a verified client certificate is a
// sufficient credential even when no bearer token is presented.
func TestMutualTLSAuthorizesWithoutToken(t *testing.T) {
	root := newTestWorkspace(t)
	pki := newTestPKI(t)
	dir := t.TempDir()

	serverCertPath := writeCertPEM(t, dir, "server.pem", pki.serverCert.Certificate[0])
	serverKeyPath := writeKeyPEM(t, dir, "server.key", pki.serverCert.PrivateKey)
	caPath := writeCertPEM(t, dir, "ca.pem", pki.caCert.Raw)

	cfg := newTestConfig(t, root)
	cfg.HTTP.Listen = "127.0.0.1:0"
	cfg.HTTP.TLSCert = serverCertPath
	cfg.HTTP.TLSKey = serverKeyPath
	cfg.HTTP.ClientCAFile = caPath
	cfg.HTTP.RequireClientCert = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	addr, _ := startTLSRemoteServer(t, cfg)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pki.caPEM)
	httpClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      pool,
			ServerName:   "localhost",
			Certificates: []tls.Certificate{pki.clientCert},
			MinVersion:   tls.VersionTLS12,
		}},
		Timeout: 10 * time.Second,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := mcp.NewClient(&mcp.Implementation{Name: "mtls-test", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   "https://" + addr + "/mcp",
		HTTPClient: httpClient,
	}, nil)
	if err != nil {
		t.Fatalf("connect with client certificate: %v", err)
	}
	defer session.Close()

	body := callTool(t, ctx, session, "remote_dev__ls", map[string]interface{}{"path": "."})
	if !strings.Contains(body, "README.md") {
		t.Fatalf("mTLS session could not list the workspace: %s", body)
	}
}

// TestMutualTLSRejectsWithoutClientCert proves RequireClientCert fails closed: a
// TLS client that presents no certificate cannot reach the endpoint.
func TestMutualTLSRejectsWithoutClientCert(t *testing.T) {
	root := newTestWorkspace(t)
	pki := newTestPKI(t)
	dir := t.TempDir()

	serverCertPath := writeCertPEM(t, dir, "server.pem", pki.serverCert.Certificate[0])
	serverKeyPath := writeKeyPEM(t, dir, "server.key", pki.serverCert.PrivateKey)
	caPath := writeCertPEM(t, dir, "ca.pem", pki.caCert.Raw)

	cfg := newTestConfig(t, root)
	cfg.HTTP.Listen = "127.0.0.1:0"
	cfg.HTTP.TLSCert = serverCertPath
	cfg.HTTP.TLSKey = serverKeyPath
	cfg.HTTP.ClientCAFile = caPath
	cfg.HTTP.RequireClientCert = true
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}

	addr, _ := startTLSRemoteServer(t, cfg)

	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pki.caPEM)
	plainClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:    pool,
			ServerName: "localhost",
			MinVersion: tls.VersionTLS12,
		}},
		Timeout: 5 * time.Second,
	}

	response, err := plainClient.Get("https://" + addr + "/metrics")
	if err == nil {
		defer response.Body.Close()
		if response.StatusCode == http.StatusOK {
			t.Fatal("expected a certificate-less request to be rejected")
		}
	}
}

// TestMutualTLSRequiresTLSFiles locks the config invariant: a client CA without a
// server certificate is refused (certificates must not travel over plaintext).
func TestMutualTLSRequiresTLSFiles(t *testing.T) {
	root := newTestWorkspace(t)
	cfg := newTestConfig(t, root)
	cfg.HTTP.ClientCAFile = filepath.Join(t.TempDir(), "missing-ca.pem")
	if err := cfg.Normalize(); err == nil {
		t.Fatal("expected a missing client CA file to fail")
	}

	// A readable CA without tlsCert must also fail.
	dir := t.TempDir()
	caPath := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caPath, []byte("not a real cert"), 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}
	cfg = newTestConfig(t, root)
	cfg.HTTP.ClientCAFile = caPath
	err := cfg.Normalize()
	if err == nil {
		t.Fatal("expected mTLS without tlsCert to fail")
	}
	if !strings.Contains(err.Error(), "tlsCert") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestWindowsPathSemantics covers the Windows jail rules.
//
// The assertions are Windows-only: on POSIX the equivalent behavior (traversal,
// symlink escape) is covered by TestWorkspaceResolveRejectsEscape.
func TestWindowsPathSemantics(t *testing.T) {
	if os.PathSeparator != '\\' {
		t.Skip("Windows-only semantics")
	}
	root := newTestWorkspace(t)
	workspace, err := remotetools.NewWorkspace(root, nil)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// Forward slashes must resolve like backslashes.
	resolved, err := workspace.Resolve("src/main.go")
	if err != nil {
		t.Fatalf("forward-slash path must resolve on Windows: %v", err)
	}
	if !strings.Contains(resolved, "main.go") {
		t.Fatalf("unexpected resolution: %s", resolved)
	}

	// Device namespaces must be refused before any I/O.
	for _, bad := range []string{
		`\\.\PhysicalDrive0`,
		`\\?\GLOBALROOT\Device\HarddiskVolumeShadowCopy1\Windows`,
		`NUL`,
		`CON.txt`,
		`sub\COM1`,
	} {
		if _, err := workspace.Resolve(bad); err == nil {
			t.Fatalf("expected %q to be refused", bad)
		}
	}

	// A sibling directory sharing the workspace prefix must not be accepted.
	if _, err := workspace.Resolve(root + "-sibling"); err == nil {
		t.Fatalf("expected sibling path to be refused")
	}
}

// TestRootsNarrowOnly proves the MCP roots direction: a client may narrow the
// workspace but never widen it.
func TestRootsNarrowOnly(t *testing.T) {
	root := newTestWorkspace(t)
	outside := t.TempDir()
	workspace, err := remotetools.NewWorkspace(root, nil)
	if err != nil {
		t.Fatalf("NewWorkspace: %v", err)
	}

	// A permitted subdirectory narrows the effective set.
	roots, err := remotetools.NewRoots(workspace, []string{"src"})
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}
	effective := roots.Effective()
	if len(effective) != 1 || !strings.HasSuffix(effective[0], "src") {
		t.Fatalf("unexpected effective roots: %v", effective)
	}
	if len(roots.Violations()) != 0 {
		t.Fatalf("unexpected violations: %v", roots.Violations())
	}

	// A declaration outside the workspace is recorded, never granted.
	roots, err = remotetools.NewRoots(workspace, []string{"src", outside})
	if err != nil {
		t.Fatalf("NewRoots: %v", err)
	}
	if len(roots.Effective()) != 1 {
		t.Fatalf("outside root must not be added: %v", roots.Effective())
	}
	if len(roots.Violations()) != 1 {
		t.Fatalf("expected one violation, got %v", roots.Violations())
	}

	// Declaring only an outside root fails closed.
	if _, err := remotetools.NewRoots(workspace, []string{outside}); err == nil {
		t.Fatal("expected a roots declaration entirely outside the workspace to fail")
	}

	// The narrowed workspace keeps the jail.
	narrowed := roots.Workspace()
	if narrowed == nil {
		t.Fatal("expected a narrowed workspace")
	}
	if _, err := narrowed.Resolve("../../etc/passwd"); err == nil {
		t.Fatal("narrowed workspace must still refuse traversal")
	}
}

// TestNormalizeRootURI covers file:// handling.
func TestNormalizeRootURI(t *testing.T) {
	if _, err := remotetools.NormalizeRootURI("https://example.com/x"); err == nil {
		t.Fatal("expected a non-file scheme to be refused")
	}
	if _, err := remotetools.NormalizeRootURI(""); err == nil {
		t.Fatal("expected an empty uri to be refused")
	}
	path, err := remotetools.NormalizeRootURI("file:///srv/app")
	if err != nil {
		t.Fatalf("NormalizeRootURI: %v", err)
	}
	if !strings.Contains(path, "srv") {
		t.Fatalf("unexpected path: %s", path)
	}
	if _, err := remotetools.NormalizeRootURI("/srv/app"); err != nil {
		t.Fatalf("bare path must be accepted: %v", err)
	}
}

// parseIP parses an address for certificate SANs.
func parseIP(raw string) []byte {
	parsed := net.ParseIP(raw)
	if parsed == nil {
		return nil
	}
	return parsed
}
