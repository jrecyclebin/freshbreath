package main

import (
	"testing"
)

func TestGenerateAndDecryptSSHKey(t *testing.T) {
	passphrase := "correct-horse-battery-staple"
	info, err := GenerateSSHKey(passphrase)
	if err != nil {
		t.Fatalf("GenerateSSHKey: %v", err)
	}

	// Public fields should be populated
	if info.PublicKey == "" {
		t.Fatal("PublicKey is empty")
	}
	if info.Fingerprint == "" {
		t.Fatal("Fingerprint is empty")
	}
	if info.KeyType != "ed25519" {
		t.Fatalf("KeyType = %q, want ed25519", info.KeyType)
	}
	if info.EncryptedSecret == "" {
		t.Fatal("EncryptedSecret is empty")
	}
	if info.Salt == "" {
		t.Fatal("Salt is empty")
	}
	if info.Nonce == "" {
		t.Fatal("Nonce is empty")
	}
}

func TestDecryptSSHKeyCorrectPassphrase(t *testing.T) {
	passphrase := "correct-horse-battery-staple"
	info, err := GenerateSSHKey(passphrase)
	if err != nil {
		t.Fatalf("GenerateSSHKey: %v", err)
	}

	plaintext, err := DecryptSSHKey(info, passphrase)
	if err != nil {
		t.Fatalf("DecryptSSHKey with correct passphrase: %v", err)
	}
	if len(plaintext) == 0 {
		t.Fatal("Decrypted private key is empty")
	}
}

func TestDecryptSSHKeyWrongPassphrase(t *testing.T) {
	info, err := GenerateSSHKey("correct-passphrase")
	if err != nil {
		t.Fatalf("GenerateSSHKey: %v", err)
	}

	_, err = DecryptSSHKey(info, "wrong-passphrase")
	if err == nil {
		t.Fatal("DecryptSSHKey with wrong passphrase should fail")
	}
}

func TestVerifyPassphrase(t *testing.T) {
	info, err := GenerateSSHKey("test-passphrase-here")
	if err != nil {
		t.Fatalf("GenerateSSHKey: %v", err)
	}

	if !VerifyPassphrase(info, "test-passphrase-here") {
		t.Fatal("VerifyPassphrase should return true for correct passphrase")
	}
	if VerifyPassphrase(info, "wrong-passphrase") {
		t.Fatal("VerifyPassphrase should return false for wrong passphrase")
	}
}

func TestParseSSHPrivateKey(t *testing.T) {
	info, err := GenerateSSHKey("test-passphrase-here")
	if err != nil {
		t.Fatalf("GenerateSSHKey: %v", err)
	}

	signer, err := ParseSSHPrivateKey(info, "test-passphrase-here")
	if err != nil {
		t.Fatalf("ParseSSHPrivateKey: %v", err)
	}
	if signer.PublicKey() == nil {
		t.Fatal("Signer has no public key")
	}
}
