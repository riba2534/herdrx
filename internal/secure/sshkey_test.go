package secure

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHKeyImportFormats(t *testing.T) {
	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ecDER, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	openssh, err := ssh.MarshalPrivateKey(rsaKey, "fixture")
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"RSA PEM":     {Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rsaKey)},
		"ECDSA PEM":   {Type: "EC PRIVATE KEY", Bytes: ecDER},
		"RSA OpenSSH": openssh,
	} {
		t.Run(name, func(t *testing.T) {
			if _, encrypted, err := (SSHKeyMaterial{PrivateKey: string(pem.EncodeToMemory(block))}).Parse(); err != nil || encrypted {
				t.Fatalf("import: encrypted=%v err=%v", encrypted, err)
			}
		})
	}
}

func TestSSHKeyEncryptedImportAndCertificate(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	block, err := ssh.MarshalPrivateKeyWithPassphrase(private, "fixture", []byte("fixture-passphrase"))
	if err != nil {
		t.Fatal(err)
	}
	m := SSHKeyMaterial{PrivateKey: string(pem.EncodeToMemory(block))}
	if _, _, err = m.Parse(); err == nil {
		t.Fatal("encrypted private key accepted without passphrase")
	}
	m.Passphrase = "wrong"
	if _, _, err = m.Parse(); err == nil {
		t.Fatal("wrong passphrase accepted")
	}
	m.Passphrase = "fixture-passphrase"
	signer, encrypted, err := m.Parse()
	if err != nil || !encrypted {
		t.Fatal("encrypted key could not be imported", err)
	}
	caPrivate, _, _ := GenerateSSHKey("")
	ca, _ := ssh.ParsePrivateKey(caPrivate)
	cert := &ssh.Certificate{Key: signer.PublicKey(), CertType: ssh.UserCert, ValidPrincipals: []string{"deploy"}, ValidAfter: uint64(time.Now().Add(-time.Minute).Unix()), ValidBefore: uint64(time.Now().Add(time.Hour).Unix())}
	if err = cert.SignCert(rand.Reader, ca); err != nil {
		t.Fatal(err)
	}
	m.Certificate = string(ssh.MarshalAuthorizedKey(cert))
	certSigner, _, err := m.Parse()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := certSigner.PublicKey().(*ssh.Certificate); !ok {
		t.Fatal("certificate not used for authentication")
	}
	if string(SSHSignerPublicKey(certSigner).Marshal()) != string(signer.PublicKey().Marshal()) {
		t.Fatal("certificate changed derived public key")
	}
	other, _, _ := GenerateSSHKey("")
	m.PrivateKey = string(other)
	m.Passphrase = ""
	if _, _, err = m.Parse(); err == nil {
		t.Fatal("mismatched certificate accepted")
	}
}
