package secure

import (
	"bytes"
	"testing"
)

func TestVaultRoundTripAndAAD(t *testing.T) {
	vault, err := OpenVault(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	plaintext := []byte("private-key-material")
	sealed, err := vault.Seal(plaintext, []byte("user-a"))
	if err != nil {
		t.Fatal(err)
	}
	opened, err := vault.Open(sealed, []byte("user-a"))
	if err != nil || !bytes.Equal(opened, plaintext) {
		t.Fatalf("round trip failed: %q, %v", opened, err)
	}
	if _, err := vault.Open(sealed, []byte("user-b")); err == nil {
		t.Fatal("ciphertext opened with the wrong AAD")
	}
}
