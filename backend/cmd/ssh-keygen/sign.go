package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/crypto/ssh"
)

// caSignerForCert 包装 CA 签名器，确保 RSA 证书签名使用 rsa-sha2-512
// （与 OpenSSH ssh-keygen 默认一致，而非 x/crypto 的 rsa-sha2-256 默认）。
func caSignerForCert(ca ssh.Signer) (ssh.Signer, error) {
	if ca.PublicKey().Type() == ssh.KeyAlgoRSA {
		as, ok := ca.(ssh.AlgorithmSigner)
		if !ok {
			return nil, fmt.Errorf("RSA signer does not implement AlgorithmSigner")
		}
		// 仅保留 rsa-sha2-512，使 SignCert 使用该算法签名。
		return ssh.NewSignerWithAlgorithms(as, []string{ssh.KeyAlgoRSASHA512})
	}
	return ca, nil
}

// runSign 实现证书签发（对应 OpenSSH ssh-keygen.c do_ca_sign）。
// 用法：ssh-keygen -s ca_key -I identity [-h] [-n principals] [-V validity] [-z serial] [-O option] key.pub...
func runSign(flags *cliFlags) error {
	// CA 私钥
	ca, err := loadPrivateKey(flags.caKeyPath)
	if err != nil {
		return fmt.Errorf("load CA key %q: %w", flags.caKeyPath, err)
	}
	if _, isCert := ca.PublicKey().(*ssh.Certificate); isCert {
		return fmt.Errorf("CA key %q must not be a certificate", flags.caKeyPath)
	}
	// 包装 RSA CA 签名器，确保使用 rsa-sha2-512（与 OpenSSH 一致）
	ca, err = caSignerForCert(ca)
	if err != nil {
		return fmt.Errorf("prepare CA signer: %w", err)
	}
	if flags.keyID == "" {
		return fmt.Errorf("must specify key id (-I) when certifying")
	}
	if flags.principals == "" {
		fmt.Fprintf(os.Stderr, "Warning: certificate will contain no principals (-n)\n")
	}

	// 解析有效期
	var validFrom, validTo uint64
	if flags.validity == "" {
		// 与 OpenSSH 默认一致：从 now-1min 到 now+52w
		validFrom, validTo, err = parseCertTimes("+52w", nowFunc())
		if err != nil {
			return err
		}
	} else {
		validFrom, validTo, err = parseCertTimes(flags.validity, nowFunc())
		if err != nil {
			return err
		}
	}

	// 解析 -O 选项
	opts := newCertOptions()
	for _, o := range flags.options {
		if err := opts.addCertOption(o); err != nil {
			return err
		}
	}
	opts.finalise()

	// principals 拆分
	var principals []string
	if flags.principals != "" {
		for _, p := range strings.Split(flags.principals, ",") {
			if p == "" {
				return fmt.Errorf("empty principal name")
			}
			principals = append(principals, p)
		}
	}

	certType := uint32(CertTypeUser)
	if flags.hostCert {
		certType = CertTypeHost
	}

	for _, keyFile := range flags.keyFiles {
		if err := signOneKey(flags, ca, certType, principals, validFrom, validTo, opts, keyFile); err != nil {
			return err
		}
	}
	return nil
}

// signOneKey 为单个公钥文件签发证书。
func signOneKey(flags *cliFlags, ca ssh.Signer, certType uint32, principals []string, validFrom, validTo uint64, opts *certOptions, keyFile string) error {
	// 读取公钥（必须是普通公钥，不能是证书）
	data, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read pubkey %q: %w", keyFile, err)
	}
	pub, comment, _, _, err := ssh.ParseAuthorizedKey(data)
	if err != nil {
		return fmt.Errorf("load pubkey %q: %w", keyFile, err)
	}
	if _, isCert := pub.(*ssh.Certificate); isCert {
		return fmt.Errorf("key %q type %s cannot be certified", keyFile, pub.Type())
	}

	// 构造证书（对应 OpenSSH sshkey_to_certified + 字段填充）
	cert := &ssh.Certificate{
		Key:             pub,
		Serial:          flags.serial,
		CertType:        certType,
		KeyId:           flags.keyID,
		ValidPrincipals: principals,
		ValidAfter:      validFrom,
		ValidBefore:     validTo,
	}
	cert.CriticalOptions = opts.criticals
	cert.Extensions = opts.extensions

	// 签名
	if err := cert.SignCert(randReader, ca); err != nil {
		return fmt.Errorf("certify %q: %w", keyFile, err)
	}

	// 输出文件：key.pub -> key-cert.pub（去掉 .pub 后缀）
	out := keyFile
	if strings.HasSuffix(out, ".pub") {
		out = strings.TrimSuffix(out, ".pub")
	}
	out = out + "-cert.pub"

	line := ssh.MarshalAuthorizedKey(cert)
	line = []byte(strings.TrimRight(string(line), "\n") + " " + comment + "\n")
	if err := os.WriteFile(out, line, 0644); err != nil {
		return fmt.Errorf("save certificate to %q: %w", out, err)
	}

	if !flags.quiet {
		certKind := "user"
		if certType == CertTypeHost {
			certKind = "host"
		}
		serialStr := fmt.Sprintf("%d", cert.Serial)
		fmt.Fprintf(os.Stderr, "Signed %s key %s: id %q serial %s%s valid %s\n",
			certKind, out, cert.KeyId, serialStr,
			principalSuffix(principals), formatValidity(validFrom, validTo))
	}

	// -z+ 自动递增
	if flags.serialAutoInc {
		flags.serial++
	}
	return nil
}

// principalSuffix 生成 OpenSSH 风格的 " for alice,bob" 后缀。
func principalSuffix(principals []string) string {
	if len(principals) == 0 {
		return ""
	}
	return " for " + strings.Join(principals, ",")
}
