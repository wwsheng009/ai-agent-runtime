package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

// ---------- 时间解析 ----------

func TestConvtime(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"52w", 52 * 7 * 24 * 3600, true},
		{"1h30m", 5400, true},
		{"2d", 2 * 24 * 3600, true},
		{"30", 30, true},
		{"30s", 30, true},
		{"1w2d3h4m5s", 1*604800 + 2*86400 + 3*3600 + 4*60 + 5, true},
		{"", 0, false},
		{"1x", 0, false},
		{"h", 0, false},
		{"1h30", 3630, true}, // OpenSSH: 1h30s = 3630
	}
	for _, c := range cases {
		got, err := convtime(c.in)
		if c.ok && err != nil {
			t.Errorf("convtime(%q): unexpected error %v", c.in, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("convtime(%q): expected error, got %d", c.in, got)
			continue
		}
		if c.ok && got != c.want {
			t.Errorf("convtime(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestParseAbsoluteTime(t *testing.T) {
	// UTC
	got, err := parseAbsoluteTime("20260101Z")
	if err != nil {
		t.Fatalf("parseAbsoluteTime(20260101Z): %v", err)
	}
	want := uint64(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	if got != want {
		t.Errorf("parseAbsoluteTime(20260101Z) = %d, want %d", got, want)
	}

	got, err = parseAbsoluteTime("20260101120000UTC")
	if err != nil {
		t.Fatalf("parseAbsoluteTime(20260101120000UTC): %v", err)
	}
	want = uint64(time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).Unix())
	if got != want {
		t.Errorf("parseAbsoluteTime(20260101120000UTC) = %d, want %d", got, want)
	}

	// 非法
	for _, bad := range []string{"2026", "2026010112", "abcdefgh", "20261301"} {
		if _, err := parseAbsoluteTime(bad); err == nil {
			t.Errorf("parseAbsoluteTime(%q): expected error", bad)
		}
	}
}

func TestParseCertTimes(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.Local)

	// +52w：from = (now-59)/60*60（回拨 1 分钟），to = now+52w
	from, to, err := parseCertTimes("+52w", now)
	if err != nil {
		t.Fatalf("parseCertTimes(+52w): %v", err)
	}
	wantFrom := uint64(((now.Unix() - 59) / 60) * 60)
	wantTo := uint64(now.Unix() + 52*7*24*3600)
	if from != wantFrom || to != wantTo {
		t.Errorf("parseCertTimes(+52w) = (%d,%d), want (%d,%d)", from, to, wantFrom, wantTo)
	}

	// always:forever
	from, to, err = parseCertTimes("always:forever", now)
	if err != nil {
		t.Fatalf("parseCertTimes(always:forever): %v", err)
	}
	if from != 0 || to != CertTimeInfinity {
		t.Errorf("parseCertTimes(always:forever) = (%d,%d), want (0,%d)", from, to, CertTimeInfinity)
	}

	// -1h:+1d
	from, to, err = parseCertTimes("-1h:+1d", now)
	if err != nil {
		t.Fatalf("parseCertTimes(-1h:+1d): %v", err)
	}
	if from != uint64(now.Unix()-3600) || to != uint64(now.Unix()+86400) {
		t.Errorf("parseCertTimes(-1h:+1d) = (%d,%d)", from, to)
	}

	// 空区间报错
	if _, _, err := parseCertTimes("20260101:20260101", now); err == nil {
		t.Error("parseCertTimes(20260101:20260101): expected error")
	}

	// 非法格式
	for _, bad := range []string{"", "+", "1h", "always:always", "forever:forever", "20260101"} {
		if _, _, err := parseCertTimes(bad, now); err == nil {
			t.Errorf("parseCertTimes(%q): expected error", bad)
		}
	}
}

// ---------- -O 选项 ----------

func TestAddCertOption(t *testing.T) {
	opts := newCertOptions()
	if err := opts.addCertOption("permit-pty"); err != nil {
		t.Fatalf("permit-pty: %v", err)
	}
	if err := opts.addCertOption("no-agent-forwarding"); err != nil {
		t.Fatalf("no-agent-forwarding: %v", err)
	}
	if err := opts.addCertOption("force-command=/usr/bin/backup"); err != nil {
		t.Fatalf("force-command: %v", err)
	}
	if err := opts.addCertOption("source-address=192.168.1.0/24,10.0.0.1"); err != nil {
		t.Fatalf("source-address: %v", err)
	}
	if err := opts.addCertOption("extension:permit-whatever=x"); err != nil {
		t.Fatalf("extension: %v", err)
	}
	if err := opts.addCertOption("critical:no-unknown=1"); err != nil {
		t.Fatalf("critical: %v", err)
	}
	opts.finalise()

	if opts.criticals["force-command"] != "/usr/bin/backup" {
		t.Errorf("force-command critical = %q", opts.criticals["force-command"])
	}
	if opts.criticals["source-address"] != "192.168.1.0/24,10.0.0.1" {
		t.Errorf("source-address critical = %q", opts.criticals["source-address"])
	}
	if _, ok := opts.extensions["permit-agent-forwarding"]; ok {
		t.Error("permit-agent-forwarding should be removed by no-agent-forwarding")
	}
	if _, ok := opts.extensions["permit-pty"]; !ok {
		t.Error("permit-pty extension missing")
	}
	if opts.extensions["permit-whatever"] != "x" {
		t.Errorf("extension permit-whatever = %q", opts.extensions["permit-whatever"])
	}

	// clear 重置
	opts2 := newCertOptions()
	if err := opts2.addCertOption("clear"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	opts2.finalise()
	if len(opts2.extensions) != 0 {
		t.Errorf("clear: extensions = %v, want none", opts2.extensions)
	}

	// 非法选项
	if err := newCertOptions().addCertOption("bogus-option"); err == nil {
		t.Error("bogus-option: expected error")
	}
	if err := newCertOptions().addCertOption("force-command="); err == nil {
		t.Error("force-command=: expected error")
	}
}

// ---------- 证书签名 round-trip ----------

// makeCA 生成测试用 CA 私钥。
func makeCA(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}
	return signer
}

func TestSignAndVerify(t *testing.T) {
	dir := t.TempDir()

	// 生成 CA 密钥对
	caPrivPath := filepath.Join(dir, "ca_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: caPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate CA: %v", err)
	}

	// 生成用户密钥对
	userPrivPath := filepath.Join(dir, "user_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: userPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate user: %v", err)
	}
	userPubPath := userPrivPath + ".pub"

	// 签发用户证书
	flags := &cliFlags{
		caKeyPath:  caPrivPath,
		keyID:      "alice@example.com",
		principals: "alice,bob",
		validity:   "+52w",
		keyFiles:   []string{userPubPath},
	}
	if err := runSign(flags); err != nil {
		t.Fatalf("runSign: %v", err)
	}

	// 证书应输出为 user_key-cert.pub
	certPath := filepath.Join(dir, "user_key-cert.pub")
	data, err := os.ReadFile(certPath)
	if err != nil {
		t.Fatalf("read cert: %v", err)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		t.Fatalf("ParseAuthorizedKey cert: %v", err)
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		t.Fatalf("expected certificate, got %T", pub)
	}
	if cert.CertType != CertTypeUser {
		t.Errorf("CertType = %d, want %d", cert.CertType, CertTypeUser)
	}
	if cert.KeyId != "alice@example.com" {
		t.Errorf("KeyId = %q", cert.KeyId)
	}
	if len(cert.ValidPrincipals) != 2 || cert.ValidPrincipals[0] != "alice" || cert.ValidPrincipals[1] != "bob" {
		t.Errorf("ValidPrincipals = %v", cert.ValidPrincipals)
	}
	if cert.ValidAfter == 0 || cert.ValidBefore <= cert.ValidAfter {
		t.Errorf("invalid validity: %d..%d", cert.ValidAfter, cert.ValidBefore)
	}
	if cert.SignatureKey.Type() != "ssh-ed25519" {
		t.Errorf("SignatureKey type = %s", cert.SignatureKey.Type())
	}
	// 验证 principal 被正确记录
	hasAlice := false
	for _, p := range cert.ValidPrincipals {
		if p == "alice" {
			hasAlice = true
			break
		}
	}
	if !hasAlice {
		t.Error("ValidPrincipals missing alice")
	}
	if comment == "" {
		t.Error("cert comment should be preserved")
	}

	// 用用户私钥包装证书签名器，验证 NewCertSigner 配对成功
	userSigner, err := loadPrivateKey(userPrivPath)
	if err != nil {
		t.Fatalf("loadPrivateKey user: %v", err)
	}
	if _, err := ssh.NewCertSigner(cert, userSigner); err != nil {
		t.Errorf("NewCertSigner: %v", err)
	}
}

func TestSignHostCert(t *testing.T) {
	dir := t.TempDir()
	caPrivPath := filepath.Join(dir, "ca_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: caPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate CA: %v", err)
	}
	hostPrivPath := filepath.Join(dir, "host_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: hostPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate host: %v", err)
	}

	flags := &cliFlags{
		caKeyPath:  caPrivPath,
		keyID:      "web1",
		principals: "web1.example.com",
		hostCert:   true,
		validity:   "always:forever",
		keyFiles:   []string{hostPrivPath + ".pub"},
	}
	if err := runSign(flags); err != nil {
		t.Fatalf("runSign host: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "host_key-cert.pub"))
	if err != nil {
		t.Fatalf("read host cert: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		t.Fatalf("ParseAuthorizedKey: %v", err)
	}
	cert := pub.(*ssh.Certificate)
	if cert.CertType != CertTypeHost {
		t.Errorf("CertType = %d, want HostCert", cert.CertType)
	}
	if cert.ValidBefore != CertTimeInfinity {
		t.Errorf("ValidBefore = %d, want infinity", cert.ValidBefore)
	}
}

func TestSignRejectsCertifiedKey(t *testing.T) {
	dir := t.TempDir()
	caPrivPath := filepath.Join(dir, "ca_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: caPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate CA: %v", err)
	}
	userPrivPath := filepath.Join(dir, "user_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: userPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate user: %v", err)
	}
	// 第一次签发
	flags := &cliFlags{
		caKeyPath:  caPrivPath,
		keyID:      "x",
		principals: "x",
		keyFiles:   []string{userPrivPath + ".pub"},
		quiet:      true,
	}
	if err := runSign(flags); err != nil {
		t.Fatalf("first sign: %v", err)
	}
	// 对证书再签发应报错
	flags2 := &cliFlags{
		caKeyPath:  caPrivPath,
		keyID:      "y",
		principals: "y",
		keyFiles:   []string{filepath.Join(dir, "user_key-cert.pub")},
		quiet:      true,
	}
	if err := runSign(flags2); err == nil {
		t.Error("signing a certificate: expected error")
	}
}

func TestSignSerialAutoInc(t *testing.T) {
	dir := t.TempDir()
	caPrivPath := filepath.Join(dir, "ca_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: caPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate CA: %v", err)
	}
	for _, name := range []string{"a", "b"} {
		p := filepath.Join(dir, name)
		if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: p, quiet: true}); err != nil {
			t.Fatalf("runGenerate %s: %v", name, err)
		}
	}

	flags := &cliFlags{
		caKeyPath:     caPrivPath,
		keyID:         "serial-test",
		principals:    "x",
		serial:        100,
		serialAutoInc: true,
		keyFiles:      []string{filepath.Join(dir, "a.pub"), filepath.Join(dir, "b.pub")},
		quiet:         true,
	}
	if err := runSign(flags); err != nil {
		t.Fatalf("runSign: %v", err)
	}

	for i, name := range []string{"a", "b"} {
		data, err := os.ReadFile(filepath.Join(dir, name+"-cert.pub"))
		if err != nil {
			t.Fatalf("read cert %s: %v", name, err)
		}
		pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
		if err != nil {
			t.Fatalf("parse cert %s: %v", name, err)
		}
		cert := pub.(*ssh.Certificate)
		if want := uint64(100 + i); cert.Serial != want {
			t.Errorf("cert %s serial = %d, want %d", name, cert.Serial, want)
		}
	}
}

func TestSignRSAKey(t *testing.T) {
	dir := t.TempDir()
	caPrivPath := filepath.Join(dir, "ca_key")
	if err := runGenerate(&cliFlags{keyType: "rsa", bits: 2048, keyFile: caPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate rsa CA: %v", err)
	}
	userPrivPath := filepath.Join(dir, "user_key")
	if err := runGenerate(&cliFlags{keyType: "rsa", bits: 2048, keyFile: userPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate rsa user: %v", err)
	}
	flags := &cliFlags{
		caKeyPath:  caPrivPath,
		keyID:      "rsa-test",
		principals: "x",
		keyFiles:   []string{userPrivPath + ".pub"},
		quiet:      true,
	}
	if err := runSign(flags); err != nil {
		t.Fatalf("runSign rsa: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "user_key-cert.pub"))
	if err != nil {
		t.Fatalf("read rsa cert: %v", err)
	}
	pub, _, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		t.Fatalf("parse rsa cert: %v", err)
	}
	cert := pub.(*ssh.Certificate)
	if !strings.HasPrefix(cert.Type(), "ssh-rsa-cert-v01@openssh.com") {
		t.Errorf("rsa cert type = %s", cert.Type())
	}
	// 验证 principal 记录
	found := false
	for _, p := range cert.ValidPrincipals {
		if p == "x" {
			found = true
			break
		}
	}
	if !found {
		t.Error("ValidPrincipals missing x")
	}
}

// ---------- RSA 签名算法兼容 ----------

func TestRSASignatureAlgoCompat(t *testing.T) {
	// OpenSSH 使用 rsa-sha2-512 作为 RSA CA 默认算法；
	// x/crypto 默认是 rsa-sha2-256，因此需要 caSignerForCert 包装。
	caPriv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(caPriv)
	if err != nil {
		t.Fatalf("NewSignerFromKey: %v", err)
	}
	// 使用 caSignerForCert 包装（与 runSign 一致）
	signer, err = caSignerForCert(signer)
	if err != nil {
		t.Fatalf("caSignerForCert: %v", err)
	}
	cert := &ssh.Certificate{
		Key:             signer.PublicKey(),
		CertType:        CertTypeUser,
		KeyId:           "t",
		ValidPrincipals: []string{"x"},
		ValidAfter:      0,
		ValidBefore:     CertTimeInfinity,
	}
	if err := cert.SignCert(rand.Reader, signer); err != nil {
		t.Fatalf("SignCert: %v", err)
	}
	if cert.Signature.Format != "rsa-sha2-512" {
		t.Errorf("RSA signature format = %q, want rsa-sha2-512", cert.Signature.Format)
	}
}

// ---------- -L / -l / -y ----------

func TestRunShowCert(t *testing.T) {
	dir := t.TempDir()
	caPrivPath := filepath.Join(dir, "ca_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: caPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate CA: %v", err)
	}
	userPrivPath := filepath.Join(dir, "user_key")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: userPrivPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate user: %v", err)
	}
	flags := &cliFlags{
		caKeyPath:  caPrivPath,
		keyID:      "alice@example.com",
		principals: "alice",
		validity:   "+52w",
		keyFiles:   []string{userPrivPath + ".pub"},
		quiet:      true,
	}
	if err := runSign(flags); err != nil {
		t.Fatalf("runSign: %v", err)
	}

	certPath := filepath.Join(dir, "user_key-cert.pub")
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	// 捕获输出
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	t.Cleanup(func() { w.Close() })
	err = runShowCert(&cliFlags{keyFile: certPath})
	w.Close()
	out := <-done
	if err != nil {
		t.Fatalf("runShowCert: %v", err)
	}
	for _, want := range []string{
		"Type: ssh-ed25519-cert-v01@openssh.com user certificate",
		"Key ID: \"alice@example.com\"",
		"Serial: 0",
		"Principals:",
		"alice",
		"Signing CA:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("runShowCert output missing %q\n%s", want, out)
		}
	}
}

func TestRunFingerprintAndPrintPublic(t *testing.T) {
	dir := t.TempDir()
	privPath := filepath.Join(dir, "k")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: privPath, quiet: true}); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}

	// -y
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	t.Cleanup(func() { w.Close() })
	err := runPrintPublic(&cliFlags{keyFile: privPath})
	w.Close()
	out := <-done
	os.Stdout = old
	if err != nil {
		t.Fatalf("runPrintPublic: %v", err)
	}
	if !strings.HasPrefix(out, "ssh-ed25519 ") {
		t.Errorf("print-public output = %q", out)
	}

	// -l
	old = os.Stdout
	r, w, _ = os.Pipe()
	os.Stdout = w
	done = make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	err = runFingerprint(&cliFlags{keyFile: privPath + ".pub"})
	w.Close()
	out = <-done
	os.Stdout = old
	if err != nil {
		t.Fatalf("runFingerprint: %v", err)
	}
	if !strings.Contains(out, "SHA256:") {
		t.Errorf("fingerprint output = %q", out)
	}
}

// ---------- CLI 解析 ----------

func TestParseFlagsSerial(t *testing.T) {
	flags, err := parseFlags([]string{"-s", "ca", "-I", "id", "-n", "x", "-z", "42", "a.pub"})
	if err != nil {
		t.Fatalf("parseFlags: %v", err)
	}
	if flags.serial != 42 || flags.serialAutoInc {
		t.Errorf("serial = %d auto=%v", flags.serial, flags.serialAutoInc)
	}

	flags, err = parseFlags([]string{"-s", "ca", "-I", "id", "-z+", "a.pub"})
	if err != nil {
		t.Fatalf("parseFlags -z+: %v", err)
	}
	if !flags.serialAutoInc {
		t.Error("serialAutoInc not set for -z+")
	}
}

func TestParseFlagsErrors(t *testing.T) {
	if _, err := parseFlags([]string{"-s", "ca", "-I", "id"}); err == nil {
		t.Error("missing key file: expected error")
	}
	if _, err := parseFlags([]string{"-s", "ca", "a.pub", "extra.pub"}); err != nil {
		t.Errorf("multiple key files should be allowed: %v", err)
	}
	if _, err := parseFlags([]string{"stray"}); err == nil {
		t.Error("stray arg without -s: expected error")
	}
}

// ---------- 辅助 ----------

func TestDefaultKeyFile(t *testing.T) {
	p, err := defaultKeyFile("ed25519")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(p, "id_ed25519") {
		t.Errorf("defaultKeyFile = %q", p)
	}
}

func TestPrintCertHost(t *testing.T) {
	// printCert 对 host cert 的 "host" 标记
	ca := makeCA(t)
	cert := &ssh.Certificate{
		Key:             ca.PublicKey(),
		CertType:        CertTypeHost,
		KeyId:           "web",
		ValidPrincipals: []string{"web.example.com"},
		ValidAfter:      0,
		ValidBefore:     CertTimeInfinity,
		Permissions: ssh.Permissions{
			Extensions: map[string]string{"permit-pty": ""},
		},
	}
	if err := cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatalf("SignCert: %v", err)
	}
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	t.Cleanup(func() { w.Close() })
	printCert(cert, "")
	w.Close()
	out := <-done
	os.Stdout = old
	if !strings.Contains(out, "host certificate") {
		t.Errorf("printCert host output missing 'host certificate':\n%s", out)
	}
	if !strings.Contains(out, "permit-pty") {
		t.Errorf("printCert host output missing extension:\n%s", out)
	}
	if !strings.Contains(out, "from past to forever") {
		t.Errorf("printCert host output missing validity:\n%s", out)
	}
}

// TestGenerateEncryptedKey 验证带口令的密钥生成可被重新解析。
func TestGenerateEncryptedKey(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "enc")
	if err := runGenerate(&cliFlags{keyType: "ed25519", keyFile: p, passphrase: "secret", quiet: true}); err != nil {
		t.Fatalf("runGenerate: %v", err)
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ssh.ParseRawPrivateKeyWithPassphrase(data, []byte("secret")); err != nil {
		t.Errorf("ParseRawPrivateKeyWithPassphrase: %v", err)
	}
	if _, err := ssh.ParseRawPrivateKey(data); err == nil {
		t.Error("unencrypted parse should fail for encrypted key")
	}
}

var _ = fmt.Sprintf // 保持 fmt 导入（调试辅助）
