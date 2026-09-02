package sshclient

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// newTestKeyPair 生成 ed25519 私钥并落盘（OpenSSH 格式），返回公钥。
func newTestKeyPair(t *testing.T, dir, name string) ssh.PublicKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, marshalOpenSSHPriv(t, priv), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	spub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("new public key: %v", err)
	}
	return spub
}

// marshalOpenSSHPriv 将 ed25519 私钥序列化为 OpenSSH 私钥格式。
func marshalOpenSSHPriv(t *testing.T, priv ed25519.PrivateKey) []byte {
	t.Helper()
	block, err := ssh.MarshalPrivateKey(priv, "")
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	return pem.EncodeToMemory(block)
}

// signUserCert 用 CA 私钥签发用户证书，并写为 authorized_keys 格式的 <key>-cert.pub。
func signUserCert(t *testing.T, dir string, caPriv ed25519.PrivateKey, userPub ssh.PublicKey, principals []string) string {
	t.Helper()
	caSigner, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatalf("ca signer: %v", err)
	}
	cert := &ssh.Certificate{
		Key:             userPub,
		CertType:        ssh.UserCert,
		KeyId:           "test-identity",
		ValidPrincipals: principals,
		ValidAfter:      uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore:     uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("sign cert: %v", err)
	}
	certPath := filepath.Join(dir, "id_ed25519-cert.pub")
	if err := os.WriteFile(certPath, ssh.MarshalAuthorizedKey(cert), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	return certPath
}

func TestLoadKeySignersAutoDetectCert(t *testing.T) {
	dir := t.TempDir()
	// CA 与用户密钥
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	userPub := newTestKeyPair(t, dir, "id_ed25519")
	signUserCert(t, dir, caPriv, userPub, []string{"testuser"})

	keyPath := filepath.Join(dir, "id_ed25519")
	signers, err := loadKeySigners(keyPath, "", io.Discard)
	if err != nil {
		t.Fatalf("loadKeySigners: %v", err)
	}
	if len(signers) != 2 {
		t.Fatalf("expected [certSigner, rawSigner], got %d signers", len(signers))
	}
	// 第一个必须是证书签名器（证书优先）
	if _, ok := signers[0].PublicKey().(*ssh.Certificate); !ok {
		t.Fatalf("first signer should carry a certificate, got %T", signers[0].PublicKey())
	}
	cert, _ := signers[0].PublicKey().(*ssh.Certificate)
	if cert.CertType != ssh.UserCert {
		t.Fatalf("cert type = %d, want UserCert", cert.CertType)
	}
	if cert.Key.Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("cert key algo = %q", cert.Key.Type())
	}
	// 第二个是裸私钥签名器
	if signers[1].PublicKey().Type() != ssh.KeyAlgoED25519 {
		t.Fatalf("second signer type = %q, want %q", signers[1].PublicKey().Type(), ssh.KeyAlgoED25519)
	}
}

func TestLoadKeySignersNoCert(t *testing.T) {
	dir := t.TempDir()
	newTestKeyPair(t, dir, "id_ed25519")
	keyPath := filepath.Join(dir, "id_ed25519")

	signers, err := loadKeySigners(keyPath, "", io.Discard)
	if err != nil {
		t.Fatalf("loadKeySigners: %v", err)
	}
	if len(signers) != 1 {
		t.Fatalf("expected 1 raw signer without cert, got %d", len(signers))
	}
	if _, ok := signers[0].PublicKey().(*ssh.Certificate); ok {
		t.Fatal("unexpected certificate signer")
	}
}

func TestLoadKeySignersMismatchCert(t *testing.T) {
	dir := t.TempDir()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	// 证书签给另一个用户密钥，与加载的私钥不匹配
	_, otherPriv, _ := ed25519.GenerateKey(rand.Reader)
	otherPub, err := ssh.NewPublicKey(otherPriv.Public())
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	signUserCert(t, dir, caPriv, otherPub, []string{"testuser"})

	// 加载一个与证书不匹配的密钥
	newTestKeyPair(t, dir, "id_ed25519")
	keyPath := filepath.Join(dir, "id_ed25519")

	var stderrBuf testWriter
	signers, err := loadKeySigners(keyPath, "", &stderrBuf)
	if err != nil {
		t.Fatalf("loadKeySigners should not fail on mismatch, got %v", err)
	}
	// 回退到裸私钥
	if len(signers) != 1 {
		t.Fatalf("expected fallback to 1 raw signer, got %d", len(signers))
	}
	if _, ok := signers[0].PublicKey().(*ssh.Certificate); ok {
		t.Fatal("mismatched cert must not be used")
	}
	if stderrBuf.s == "" {
		t.Fatal("expected a warning about skipped certificate")
	}
}

func TestLoadKeySignersExplicitCertPath(t *testing.T) {
	dir := t.TempDir()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	// 私钥命名不遵循 -cert.pub 约定，证书放其他名字
	_, userPriv, _ := ed25519.GenerateKey(rand.Reader)
	userPub, _ := ssh.NewPublicKey(userPriv.Public())
	keyPath := filepath.Join(dir, "mykey")
	if err := os.WriteFile(keyPath, marshalOpenSSHPriv(t, userPriv), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	// 构造证书并写为自定义路径
	caSigner, _ := ssh.NewSignerFromKey(caPriv)
	cert := &ssh.Certificate{
		Key:         userPub,
		CertType:    ssh.UserCert,
		KeyId:       "test-identity",
		ValidAfter:  uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore: uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("sign cert: %v", err)
	}
	explicitCertPath := filepath.Join(dir, "mykey-certificates.pub")
	if err := os.WriteFile(explicitCertPath, ssh.MarshalAuthorizedKey(cert), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	signers, err := loadKeySigners(keyPath, explicitCertPath, io.Discard)
	if err != nil {
		t.Fatalf("loadKeySigners: %v", err)
	}
	if len(signers) != 2 {
		t.Fatalf("expected 2 signers with explicit cert, got %d", len(signers))
	}
	if _, ok := signers[0].PublicKey().(*ssh.Certificate); !ok {
		t.Fatal("expected cert signer first")
	}
}

func TestBuildKeySignersFromOptions(t *testing.T) {
	dir := t.TempDir()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	userPub := newTestKeyPair(t, dir, "id_ed25519")
	signUserCert(t, dir, caPriv, userPub, []string{"testuser"})

	opts := Defaults()
	opts.IdentityFiles = []string{filepath.Join(dir, "id_ed25519")}

	signers, err := buildKeySigners(opts, io.Discard)
	if err != nil {
		t.Fatalf("buildKeySigners: %v", err)
	}
	if len(signers) != 2 {
		t.Fatalf("expected cert+raw signers, got %d", len(signers))
	}
	if _, ok := signers[0].PublicKey().(*ssh.Certificate); !ok {
		t.Fatalf("first signer should be certificate, got %T", signers[0].PublicKey())
	}
}

func TestBuildKeySignersCertificateFileOption(t *testing.T) {
	dir := t.TempDir()
	_, caPriv, _ := ed25519.GenerateKey(rand.Reader)
	_, userPriv, _ := ed25519.GenerateKey(rand.Reader)
	userPub, _ := ssh.NewPublicKey(userPriv.Public())
	keyPath := filepath.Join(dir, "custom_key")
	if err := os.WriteFile(keyPath, marshalOpenSSHPriv(t, userPriv), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	caSigner, _ := ssh.NewSignerFromKey(caPriv)
	cert := &ssh.Certificate{
		Key:         userPub,
		CertType:    ssh.UserCert,
		KeyId:       "test-identity",
		ValidAfter:  uint64(time.Now().Add(-time.Minute).Unix()),
		ValidBefore: uint64(time.Now().Add(time.Hour).Unix()),
	}
	if err := cert.SignCert(rand.Reader, caSigner); err != nil {
		t.Fatalf("sign cert: %v", err)
	}
	// 证书文件名不含 -cert.pub，需要 CertificateFile 显式指定
	certPath := filepath.Join(dir, "extra.cer")
	if err := os.WriteFile(certPath, ssh.MarshalAuthorizedKey(cert), 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	opts := Defaults()
	opts.IdentityFiles = []string{keyPath}
	opts.CertificateFiles = []string{certPath}

	signers, err := buildKeySigners(opts, io.Discard)
	if err != nil {
		t.Fatalf("buildKeySigners: %v", err)
	}
	if len(signers) != 2 {
		t.Fatalf("expected 2 signers, got %d", len(signers))
	}
	if _, ok := signers[0].PublicKey().(*ssh.Certificate); !ok {
		t.Fatalf("expected cert signer first, got %T", signers[0].PublicKey())
	}
}

func TestHasIdentitySource(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// 1. 空配置 + 无默认密钥 → false
	opts := Defaults()
	if hasIdentitySource(opts) {
		t.Fatal("empty config with no default keys should have no source")
	}

	// 2. 显式 IdentityFile → true
	kPath := filepath.Join(dir, "mykey")
	if err := os.WriteFile(kPath, []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts.IdentityFiles = []string{kPath}
	if !hasIdentitySource(opts) {
		t.Fatal("explicit IdentityFile should count as source")
	}

	// 3. 显式 CertificateFile → true
	opts = Defaults()
	opts.CertificateFiles = []string{filepath.Join(dir, "mycert.pub")}
	if !hasIdentitySource(opts) {
		t.Fatal("explicit CertificateFile should count as source")
	}

	// 4. ~/.ssh/id_ed25519 默认密钥 → true
	sshDir := filepath.Join(dir, ".ssh")
	if err := os.Mkdir(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sshDir, "id_ed25519"), []byte("dummy"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts = Defaults()
	if !hasIdentitySource(opts) {
		t.Fatal("default key ~/.ssh/id_ed25519 should count as source")
	}
}

func TestBuildAuthMethodsLazyKeyLoading(t *testing.T) {
	// 验证 BuildAuthMethods 不会急切加载带口令的私钥。
	// 带口令的密钥在旧实现中会立即触发 readPassphrase → stdin 非终端时报错。
	// 懒加载后 BuildAuthMethods 应正常返回方法，不触发 readPassphrase。
	dir := t.TempDir()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	// 加密私钥（带口令 "secret"）
	block, err := ssh.MarshalPrivateKey(priv, "secret")
	if err != nil {
		t.Fatalf("marshal encrypted key: %v", err)
	}
	encPath := filepath.Join(dir, "id_ed25519")
	if err := os.WriteFile(encPath, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write encrypted key: %v", err)
	}
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	unsetEnv(t, "SSH_KEY_PASSPHRASE")

	opts := Defaults()
	opts.IdentityFiles = []string{encPath}

	// 旧实现：BuildAuthMethods 在此处会因 readPassphrase 失败而报错
	// 新实现：懒加载，应正常返回
	methods, err := BuildAuthMethods(opts, io.Discard)
	if err != nil {
		t.Fatalf("BuildAuthMethods should not load keys eagerly: %v", err)
	}
	if len(methods) == 0 {
		t.Fatal("expected at least one auth method (file-key callback)")
	}
}

// unsetEnv 清除环境变量，恢复由 t.Cleanup 处理。
func unsetEnv(t *testing.T, key string) {
	t.Helper()
	old, ok := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unsetenv %q: %v", key, err)
	}
	t.Cleanup(func() {
		if ok {
			os.Setenv(key, old)
		}
	})
}

// testWriter 捕获 stderr 警告输出。
type testWriter struct{ s string }

func (w *testWriter) Write(p []byte) (int, error) {
	w.s += string(p)
	return len(p), nil
}
