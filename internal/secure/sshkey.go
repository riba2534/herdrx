package secure

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/ssh"
)

// SSHKeyMaterial is serialized only inside the authenticated encrypted vault.
type SSHKeyMaterial struct {
	PrivateKey  string `json:"private_key"`
	Passphrase  string `json:"passphrase,omitempty"`
	Certificate string `json:"certificate,omitempty"`
}

// Parse validates the key (and optional user certificate) before it is saved.
// It derives the public key, so a pasted .pub file cannot masquerade as a private key.
func (m SSHKeyMaterial) Parse() (ssh.Signer, bool, error) {
	if len(m.PrivateKey) > 65536 || len(m.Certificate) > 65536 || len(m.Passphrase) > 4096 {
		return nil, false, fmt.Errorf("密钥内容过长")
	}
	signer, err := ssh.ParsePrivateKey([]byte(m.PrivateKey))
	encrypted := false
	var missing *ssh.PassphraseMissingError
	if errors.As(err, &missing) {
		encrypted = true
		if m.Passphrase == "" {
			return nil, true, fmt.Errorf("该私钥已加密，请填写私钥口令")
		}
		signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(m.PrivateKey), []byte(m.Passphrase))
	}
	if err != nil {
		return nil, encrypted, fmt.Errorf("无法解析私钥，请检查 OpenSSH/PEM 文件和私钥口令")
	}
	if strings.TrimSpace(m.Certificate) != "" {
		public, _, _, rest, err := ssh.ParseAuthorizedKey([]byte(m.Certificate))
		if err != nil || len(bytes.TrimSpace(rest)) != 0 {
			return nil, encrypted, fmt.Errorf("SSH 证书格式无效")
		}
		cert, ok := public.(*ssh.Certificate)
		if !ok || cert.CertType != ssh.UserCert {
			return nil, encrypted, fmt.Errorf("请提供 SSH 用户证书")
		}
		signer, err = ssh.NewCertSigner(cert, signer)
		if err != nil {
			return nil, encrypted, fmt.Errorf("SSH 证书与私钥不匹配")
		}
	}
	return signer, encrypted, nil
}

func SSHSignerPublicKey(signer ssh.Signer) ssh.PublicKey {
	if cert, ok := signer.PublicKey().(*ssh.Certificate); ok {
		return cert.Key
	}
	return signer.PublicKey()
}

func GenerateSSHKey(comment string) (privatePEM, authorizedKey []byte, err error) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("generate ed25519 key: %w", err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	sshPublic, err := ssh.NewPublicKey(publicKey)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal SSH public key: %w", err)
	}
	privatePEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded})
	authorizedKey = ssh.MarshalAuthorizedKey(sshPublic)
	if comment != "" {
		authorizedKey = append(authorizedKey[:len(authorizedKey)-1], []byte(" "+comment+"\n")...)
	}
	return privatePEM, authorizedKey, nil
}
