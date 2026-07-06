package sshkit

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// HostKeyStore abstracts TOFU host key persistence. The caller provides
// an implementation backed by whatever database they use.
type HostKeyStore interface {
	GetSSHHostKey(host string, port int) (keyData []byte, fingerprint string, err error)
	StoreSSHHostKey(host string, port int, keyData []byte, fingerprint string) error
}

// Session represents an open SSH + SFTP connection to a remote host.
type Session struct {
	ID          string
	UserID      int64
	Host        string
	Port        int
	Username    string
	SSHClient   *ssh.Client
	SFTPClient  *sftp.Client
	ConnectedAt time.Time
	ExpiresAt   time.Time
}

// SessionManager manages SSH sessions. Each session is an authenticated
// SSH + SFTP connection to a remote host, keyed by a random session ID.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]*Session
	agent    *AgentManager
	hostKeys HostKeyStore
	ttl      time.Duration
}

// NewSessionManager creates a session manager backed by the given agent and host key store.
// The host key store is used for TOFU host key storage. The ttl controls how long
// sessions stay open before automatic expiry.
func NewSessionManager(agent *AgentManager, hostKeys HostKeyStore, ttl time.Duration) *SessionManager {
	return &SessionManager{
		sessions: make(map[string]*Session),
		agent:    agent,
		hostKeys: hostKeys,
		ttl:      ttl,
	}
}

// Open dials an SSH connection to the given host using the user's key from
// the agent manager, then opens the SFTP subsystem. Returns the new session.
func (m *SessionManager) Open(userID int64, host string, port int, username string) (*Session, error) {
	signer, err := m.agent.GetSigner(userID)
	if err != nil {
		return nil, fmt.Errorf("no SSH key available: %w", err)
	}

	config := &ssh.ClientConfig{
		User:            username,
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: NewTOFUHostKeyCallback(m.hostKeys),
		Timeout:         15 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	sshClient, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, fmt.Errorf("sftp subsystem: %w", err)
	}

	now := time.Now()
	session := &Session{
		ID:          genNonce(),
		UserID:      userID,
		Host:        host,
		Port:        port,
		Username:    username,
		SSHClient:   sshClient,
		SFTPClient:  sftpClient,
		ConnectedAt: now,
		ExpiresAt:   now.Add(m.ttl),
	}

	m.mu.Lock()
	m.sessions[session.ID] = session
	m.mu.Unlock()

	return session, nil
}

// Get returns a session by ID. Returns an error if not found or expired.
func (m *SessionManager) Get(id string) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return nil, fmt.Errorf("session not found")
	}
	if time.Now().After(s.ExpiresAt) {
		// Lazy expiry — close and remove.
		s.SFTPClient.Close()
		s.SSHClient.Close()
		delete(m.sessions, id)
		return nil, fmt.Errorf("session expired")
	}
	return s, nil
}

// Close shuts down a session's SSH + SFTP connections and removes it.
func (m *SessionManager) Close(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok {
		return fmt.Errorf("session not found")
	}
	s.SFTPClient.Close()
	s.SSHClient.Close()
	delete(m.sessions, id)
	return nil
}

// ExpireSessions closes and removes sessions past their TTL.
func (m *SessionManager) ExpireSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			s.SFTPClient.Close()
			s.SSHClient.Close()
			delete(m.sessions, id)
		}
	}
}

// Stop closes all sessions. Call on server shutdown.
func (m *SessionManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, s := range m.sessions {
		s.SFTPClient.Close()
		s.SSHClient.Close()
		delete(m.sessions, id)
	}
}

// NewTOFUHostKeyCallback returns an ssh.HostKeyCallback implementing Trust
// On First Use host key verification against the given store. On first
// connection to a host, its key is recorded. On subsequent connections, the
// key must match — if it's changed, the connection is rejected with a clear
// error. This is the same security model as the default OpenSSH client
// behavior. Shared by SessionManager.Open and the git gateway's SSH transport.
func NewTOFUHostKeyCallback(store HostKeyStore) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		// Parse host:port from the remote address
		host, portStr, err := net.SplitHostPort(remote.String())
		if err != nil {
			host = hostname
			portStr = "22"
		}
		var port int
		fmt.Sscanf(portStr, "%d", &port)
		if port == 0 {
			port = 22
		}

		storedData, storedFP, err := store.GetSSHHostKey(host, port)
		if err != nil {
			return fmt.Errorf("host key lookup failed: %w", err)
		}

		keyData := key.Marshal()
		keyFP := ssh.FingerprintSHA256(key)

		if storedData == nil {
			// First connection — trust and store.
			if err := store.StoreSSHHostKey(host, port, keyData, keyFP); err != nil {
				return fmt.Errorf("failed to store host key: %w", err)
			}
			return nil
		}

		// Subsequent connection — verify the key matches.
		if !bytes.Equal(storedData, keyData) {
			return fmt.Errorf("host key mismatch for %s:%d — expected %s, got %s. The server's key may have changed (could indicate a MITM attack). Delete the stored key to accept the new one.",
				host, port, storedFP, keyFP)
		}

		return nil
	}
}

// genNonce generates a random 48-char hex string for session IDs.
func genNonce() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// DeriveJWTSecretFromTLSKey derives a 32-byte HMAC-SHA256 signing key
// from the TLS private key using a fixed domain label. This makes the JWT
// secret stable across restarts without additional configuration.
func DeriveJWTSecretFromTLSKey(tlsKeyPEM []byte) []byte {
	h := hmac.New(sha256.New, tlsKeyPEM)
	h.Write([]byte("freshbreath.jwt.v1"))
	return h.Sum(nil)
}
