package main

import (
	"bufio"
	"fmt"
	"os"
	"sort"
	"strings"

	"golang.org/x/crypto/ssh"
)

// runShowCert 实现 -L：打印证书内容（对应 OpenSSH do_show_cert + print_cert）。
// 支持从文件读取，也支持多行 authorized_keys 格式输入。
func runShowCert(flags *cliFlags) error {
	path := flags.keyFile
	if path == "" {
		return fmt.Errorf("must specify certificate file with -f")
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	ln := 0
	found := false
	for scanner.Scan() {
		ln++
		line := scanner.Text()
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		pub, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
		if err != nil {
			fmt.Fprintf(os.Stderr, "ssh-keygen: %s:%d: invalid key: %v\n", path, ln, err)
			continue
		}
		cert, ok := pub.(*ssh.Certificate)
		if !ok {
			fmt.Fprintf(os.Stderr, "ssh-keygen: %s:%d is not a certificate\n", path, ln)
			continue
		}
		found = true
		if ln == 1 {
			fmt.Printf("%s:\n", path)
		} else {
			fmt.Printf("%s:%d:\n", path, ln)
		}
		printCert(cert, comment)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %q: %w", path, err)
	}
	if !found {
		return fmt.Errorf("no certificates found in %q", path)
	}
	return nil
}

// printCert 打印单个证书详情（对齐 OpenSSH print_cert 输出）。
func printCert(cert *ssh.Certificate, comment string) {
	kind := "user"
	if cert.CertType == CertTypeHost {
		kind = "host"
	}
	keyFP := ssh.FingerprintSHA256(cert.Key)
	caFP := ssh.FingerprintSHA256(cert.SignatureKey)
	fmt.Printf("        Type: %s %s certificate\n", cert.Type(), kind)
	fmt.Printf("        Public key: %s %s\n", cert.Key.Type(), keyFP)
	fmt.Printf("        Signing CA: %s %s (using %s)\n",
		cert.SignatureKey.Type(), caFP, certSignatureAlgo(cert))
	fmt.Printf("        Key ID: %q\n", cert.KeyId)
	fmt.Printf("        Serial: %d\n", cert.Serial)
	fmt.Printf("        Valid: %s\n", formatValidity(cert.ValidAfter, cert.ValidBefore))
	fmt.Printf("        Principals: ")
	if len(cert.ValidPrincipals) == 0 {
		fmt.Printf("(none)\n")
	} else {
		fmt.Printf("\n")
		for _, p := range cert.ValidPrincipals {
			fmt.Printf("                %s\n", p)
		}
	}
	printCertOptions("Critical Options", cert.CriticalOptions, true)
	printCertOptions("Extensions", cert.Extensions, false)
	if comment != "" {
		fmt.Printf("        Comment: %s\n", comment)
	}
}

// certSignatureAlgo 还原签名算法名（Signature.Format 已带 -v01@openssh.com）。
func certSignatureAlgo(cert *ssh.Certificate) string {
	if cert.Signature == nil {
		return "unknown"
	}
	// x/crypto 的 Signature.Format 形如 "ssh-ed25519" 或 "rsa-sha2-512"，
	// OpenSSH 显示为 "ssh-ed25519" / "rsa-sha2-512"。
	return cert.Signature.Format
}

// printCertOptions 打印 Critical Options / Extensions 节。
// 与 OpenSSH show_options 类似：无值选项显示名称本身。
func printCertOptions(title string, opts map[string]string, isCritical bool) {
	fmt.Printf("        %s: ", title)
	if len(opts) == 0 {
		fmt.Printf("(none)\n")
		return
	}
	fmt.Printf("\n")
	keys := make([]string, 0, len(opts))
	for k := range opts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		v := opts[k]
		if v == "" {
			fmt.Printf("                %s\n", k)
		} else {
			// 与 OpenSSH 一致：force-command / source-address 显示值
			fmt.Printf("                %s %s\n", k, v)
		}
	}
}

// runFingerprint 实现 -l：打印公钥指纹。
func runFingerprint(flags *cliFlags) error {
	path := flags.keyFile
	if path == "" {
		return fmt.Errorf("must specify key file with -f")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("open %q: %w", path, err)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		// 可能是裸 blob（无类型前缀）
		if pub2, err2 := ssh.ParsePublicKey(data); err2 == nil {
			pub = pub2
		} else {
			return fmt.Errorf("parse key %q: %w", path, err)
		}
	}
	kind := strings.ToUpper(strings.ReplaceAll(pub.Type(), "-cert-v01@openssh.com", "-CERT"))
	fmt.Printf("%d %s %s (%s)\n", keyBits(pub), ssh.FingerprintSHA256(pub), comment, kind)
	return nil
}

// runPrintPublic 实现 -y：从私钥打印公钥。
func runPrintPublic(flags *cliFlags) error {
	path := flags.keyFile
	if path == "" {
		return fmt.Errorf("must specify private key file with -f")
	}
	signer, err := loadPrivateKey(path)
	if err != nil {
		return fmt.Errorf("load private key %q: %w", path, err)
	}
	comment := flags.comment
	line := ssh.MarshalAuthorizedKey(signer.PublicKey())
	if comment != "" {
		line = []byte(strings.TrimRight(string(line), "\n") + " " + comment + "\n")
	}
	fmt.Print(string(line))
	return nil
}

// keyBits 估算密钥位数（OpenSSH -l 显示 key size）。
func keyBits(pub ssh.PublicKey) int {
	if cpk, ok := pub.(ssh.CryptoPublicKey); ok {
		switch k := cpk.CryptoPublicKey().(type) {
		case interface{ Size() int }:
			return k.Size() * 8
		}
	}
	// ed25519 及未知类型按长度估算
	return len(pub.Marshal()) * 4
}
