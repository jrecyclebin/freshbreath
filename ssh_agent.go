package main

import (
  "fmt"
  "sync"
  "time"

  "golang.org/x/crypto/ssh"
)

// AgentEntry tracks a single user's decrypted SSH key in memory.
type AgentEntry struct {
  Expiry time.Time
  Signer ssh.Signer
}

// AgentManager manages per-user SSH key signers in memory.
// When a user logs in via SSH passphrase, their decrypted key is stored here
// with a TTL. SessionManager retrieves signers to open outbound SSH connections.
// Agent TTL is decoupled from web JWT expiry — agent timeout doesn't kill
// active SSH sessions (the connection stays open), and the web session dying
// doesn't remove the key.
type AgentManager struct {
  mu      sync.Mutex
  entries map[int64]*AgentEntry // keyed by user ID
}

// NewAgentManager creates a manager. No socket cleanup needed — we're
// purely in-memory now.
func NewAgentManager() *AgentManager {
  return &AgentManager{
    entries: make(map[int64]*AgentEntry),
  }
}

// AddKey decrypts the user's SSH key with the given passphrase and stores
// the signer. If the user already has an entry, it's replaced.
func (m *AgentManager) AddKey(userID int64, info *SSHKeyInfo, passphrase string, ttl time.Duration) error {
  signer, err := ParseSSHPrivateKey(info, passphrase)
  if err != nil {
    return fmt.Errorf("parse key for agent: %w", err)
  }

  m.mu.Lock()
  defer m.mu.Unlock()

  m.entries[userID] = &AgentEntry{
    Expiry: time.Now().Add(ttl),
    Signer: signer,
  }
  return nil
}

// GetSigner returns the active signer for a user, or an error if no key
// is loaded or the TTL has elapsed.
func (m *AgentManager) GetSigner(userID int64) (ssh.Signer, error) {
  m.mu.Lock()
  defer m.mu.Unlock()

  entry, ok := m.entries[userID]
  if !ok || time.Now().After(entry.Expiry) {
    return nil, ErrNoKey
  }
  return entry.Signer, nil
}

// ErrNoKey is returned when the user has no active SSH key in the agent.
var ErrNoKey = fmt.Errorf("no active SSH key — please log in again")

// RemoveKey removes a user's key entry.
func (m *AgentManager) RemoveKey(userID int64) {
  m.mu.Lock()
  defer m.mu.Unlock()
  delete(m.entries, userID)
}

// ExpireKeys removes entries whose TTL has elapsed.
func (m *AgentManager) ExpireKeys() {
  m.mu.Lock()
  defer m.mu.Unlock()

  now := time.Now()
  for id, entry := range m.entries {
    if now.After(entry.Expiry) {
      delete(m.entries, id)
    }
  }
}
