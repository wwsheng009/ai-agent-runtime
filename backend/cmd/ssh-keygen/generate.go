package main

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
	"golang.org/x/term"
)

// runGenerate 实现密钥对生成（默认模式）。
func runGenerate(flags *cliFlags) error {
	kt := strings.ToLower(flags.keyType)
	switch kt {
	case "ed25519", "rsa", "ecdsa":
	default:
		return fmt.Errorf("unsupported key type %q (supported: ed25519, rsa, ecdsa)", flags.keyType)
	}
	bits := flags.bits
	if bits == 0 {
		switch kt {
		case "rsa":
			bits = defaultRSAKeyBits
		case "ecdsa":
			bits = defaultECDSAKeyBits
		}
	}

	// 私钥文件路径
	keyPath := flags.keyFile
	if keyPath == "" {
		var err error
		keyPath, err = defaultKeyFile(kt)
		if err != nil {
			return err
		}
	}
	// 检查覆盖
	info, err := os.Stat(keyPath)
	if err == nil {
		if info.Size() > 0 {
			if term.IsTerminal(int(os.Stdin.Fd())) {
				fmt.Fprintf(os.Stderr, "%s already exists.\nOverwrite (y/n)? ", keyPath)
				var answer string
				_, _ = fmt.Scan(&answer)
				answer = strings.ToLower(strings.TrimSpace(answer))
				if answer != "y" && answer != "yes" {
					return fmt.Errorf("aborted by user")
				}
			} else {
				return fmt.Errorf("%s already exists; use an interactive terminal to confirm overwrite", keyPath)
			}
		}
	} else if !os.IsNotExist(err) {
		return err
	}

	// 生成密钥
	var rawKey interface{}
	var pub ssh.PublicKey
	switch kt {
	case "ed25519":
		pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return fmt.Errorf("generate ed25519 key: %w", err)
		}
		rawKey = privKey
		pub, err = ssh.NewPublicKey(pubKey)
		if err != nil {
			return fmt.Errorf("wrap ed25519 public key: %w", err)
		}
	case "rsa":
		privKey, err := rsa.GenerateKey(rand.Reader, bits)
		if err != nil {
			return fmt.Errorf("generate rsa key: %w", err)
		}
		rawKey = privKey
		pub, err = ssh.NewPublicKey(&privKey.PublicKey)
		if err != nil {
			return fmt.Errorf("wrap rsa public key: %w", err)
		}
	case "ecdsa":
		var curve elliptic.Curve
		switch bits {
		case 256:
			curve = elliptic.P256()
		case 384:
			curve = elliptic.P384()
		case 521:
			curve = elliptic.P521()
		default:
			return fmt.Errorf("unsupported ecdsa bits %d (supported: 256, 384, 521)", bits)
		}
		privKey, err := ecdsa.GenerateKey(curve, rand.Reader)
		if err != nil {
			return fmt.Errorf("generate ecdsa key: %w", err)
		}
		rawKey = privKey
		pub, err = ssh.NewPublicKey(&privKey.PublicKey)
		if err != nil {
			return fmt.Errorf("wrap ecdsa public key: %w", err)
		}
	}

	// 注释
	comment := flags.comment
	if comment == "" {
		user := os.Getenv("USER")
		if user == "" {
			user = os.Getenv("USERNAME")
		}
		hostname, _ := os.Hostname()
		if hostname == "" {
			hostname = "unknown"
		}
		comment = fmt.Sprintf("%s@%s", user, hostname)
	}

	// 写入私钥
	var block *pem.Block
	if flags.passphrase != "" {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(rawKey, comment, []byte(flags.passphrase))
	} else {
		block, err = ssh.MarshalPrivateKey(rawKey, comment)
	}
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0700); err != nil {
		return fmt.Errorf("create key directory: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}
	if !flags.quiet {
		fmt.Fprintf(os.Stderr, "Your identification has been saved in %s\n", keyPath)
	}

	// 写入公钥（OpenSSH authorized_keys 格式：type key comment\n）
	pubPath := keyPath + ".pub"
	line := ssh.MarshalAuthorizedKey(pub)
	line = []byte(strings.TrimRight(string(line), "\n") + " " + comment + "\n")
	if err := os.WriteFile(pubPath, line, 0644); err != nil {
		return fmt.Errorf("write public key: %w", err)
	}
	if !flags.quiet {
		fmt.Fprintf(os.Stderr, "Your public key has been saved in %s\n", pubPath)
		fmt.Fprintf(os.Stderr, "The key fingerprint is:\n%s %s\n",
			ssh.FingerprintSHA256(pub), comment)
	}

	return nil
}
