package main

import (
  "crypto/aes"
  "crypto/cipher"
  "crypto/ed25519"
  "crypto/hmac"
  "crypto/rand"
  "crypto/sha256"
  "encoding/hex"
  "encoding/pem"
  "fmt"
  "io"

  "golang.org/x/crypto/argon2"
  "golang.org/x/crypto/ssh"
)

// GenerateSSHKey creates an Ed25519 key pair, encrypts the private key with
// the given passphrase using Argon2id + AES-256-GCM, and returns SSHKeyInfo.
func GenerateSSHKey(passphrase string) (*SSHKeyInfo, error) {
  pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
  if err != nil {
    return nil, fmt.Errorf("ed25519 generate: %w", err)
  }

  // Encode public key in OpenSSH authorized_keys format
  sshPub, err := ssh.NewPublicKey(pubKey)
  if err != nil {
    return nil, fmt.Errorf("encode public key: %w", err)
  }
  pubStr := string(ssh.MarshalAuthorizedKey(sshPub))
  fp := ssh.FingerprintSHA256(sshPub)

  // Encode private key in OpenSSH PEM format (unencrypted at this layer;
  // we apply our own AES-GCM encryption instead of ssh passphrase encryption
  // so we can decrypt with a single passphrase attempt, no interactive prompt)
  pemBlock, err := ssh.MarshalPrivateKey(privKey, "")
  if err != nil {
    return nil, fmt.Errorf("marshal private key: %w", err)
  }
  privPEM := pem.EncodeToMemory(pemBlock)

  // Encrypt the PEM bytes with Argon2id → AES-256-GCM
  salt := make([]byte, 16)
  if _, err := io.ReadFull(rand.Reader, salt); err != nil {
    return nil, fmt.Errorf("salt: %w", err)
  }
  nonce := make([]byte, 12)
  if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
    return nil, fmt.Errorf("nonce: %w", err)
  }

  encKey := argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)
  block, err := aes.NewCipher(encKey)
  if err != nil {
    return nil, fmt.Errorf("aes cipher: %w", err)
  }
  aesgcm, err := cipher.NewGCM(block)
  if err != nil {
    return nil, fmt.Errorf("gcm: %w", err)
  }
  ciphertext := aesgcm.Seal(nil, nonce, privPEM, nil)

  return &SSHKeyInfo{
    PublicKey:       pubStr,
    Fingerprint:     fp,
    KeyType:         "ed25519",
    EncryptedSecret: hex.EncodeToString(ciphertext),
    Salt:            hex.EncodeToString(salt),
    Nonce:           hex.EncodeToString(nonce),
  }, nil
}

// DecryptSSHKey decrypts the private key using the passphrase.
// Returns the raw PEM-encoded private key bytes.
func DecryptSSHKey(info *SSHKeyInfo, passphrase string) ([]byte, error) {
  ciphertext, err := hex.DecodeString(info.EncryptedSecret)
  if err != nil {
    return nil, fmt.Errorf("decode ciphertext: %w", err)
  }
  salt, err := hex.DecodeString(info.Salt)
  if err != nil {
    return nil, fmt.Errorf("decode salt: %w", err)
  }
  nonce, err := hex.DecodeString(info.Nonce)
  if err != nil {
    return nil, fmt.Errorf("decode nonce: %w", err)
  }

  encKey := argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)
  block, err := aes.NewCipher(encKey)
  if err != nil {
    return nil, fmt.Errorf("aes cipher: %w", err)
  }
  aesgcm, err := cipher.NewGCM(block)
  if err != nil {
    return nil, fmt.Errorf("gcm: %w", err)
  }
  plaintext, err := aesgcm.Open(nil, nonce, ciphertext, nil)
  if err != nil {
    return nil, fmt.Errorf("decrypt: invalid passphrase or corrupted data")
  }
  return plaintext, nil
}

// VerifyPassphrase checks if the passphrase can decrypt the private key.
func VerifyPassphrase(info *SSHKeyInfo, passphrase string) bool {
  _, err := DecryptSSHKey(info, passphrase)
  return err == nil
}

// ParseSSHPrivateKey decrypts and parses the private key into an ssh.Signer
// ready for use with the agent or SSH connections.
func ParseSSHPrivateKey(info *SSHKeyInfo, passphrase string) (ssh.Signer, error) {
  pemBytes, err := DecryptSSHKey(info, passphrase)
  if err != nil {
    return nil, err
  }
  signer, err := ssh.ParsePrivateKey(pemBytes)
  if err != nil {
    return nil, fmt.Errorf("parse private key: %w", err)
  }
  return signer, nil
}

// deriveJWTSecretFromTLSKey derives a 32-byte HMAC-SHA256 signing key
// from the TLS private key using a fixed domain label. This makes the JWT
// secret stable across restarts without additional configuration.
func deriveJWTSecretFromTLSKey(tlsKeyPEM []byte) []byte {
  h := hmac.New(sha256.New, tlsKeyPEM)
  h.Write([]byte("freshbreath.jwt.v1"))
  return h.Sum(nil)
}
