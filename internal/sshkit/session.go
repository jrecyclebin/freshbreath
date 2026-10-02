package sshkit

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// ErrSessionNotFound covers an unknown id, one owned by another user, and
// one forgotten after its reopen window — all a 404 to the caller.
var ErrSessionNotFound = errors.New("session not found")

// How long an expired session's id stays good for a reopen. Past this the
// record is dropped and the id reads as not found.
const reopenWindow = 24 * time.Hour

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

	closed bool // connections shut at expiry; guarded by SessionManager.mu
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
	sshClient, sftpClient, err := m.dial(userID, host, port, username)
	if err != nil {
		return nil, err
	}
	session := m.newSession(genNonce(), userID, host, port, username, sshClient, sftpClient)

	m.mu.Lock()
	m.sessions[session.ID] = session
	m.mu.Unlock()

	return session, nil
}

// Get returns the user's session by ID. A session past its TTL is redialed
// under the same ID with the owner's agent key, so the id is a durable
// handle and the TTL only decides when the user must prove themselves
// again: the agent key lapses an hour after the passphrase login that
// loaded it — always before the TTL — and then the reopen fails with
// ErrNoKey until they log in again.
func (m *SessionManager) Get(id string, userID int64) (*Session, error) {
	m.mu.Lock()
	s, ok := m.sessions[id]
	if !ok || s.UserID != userID {
		m.mu.Unlock()
		return nil, ErrSessionNotFound
	}
	if time.Now().Before(s.ExpiresAt) {
		m.mu.Unlock()
		return s, nil
	}
	shut(s)
	m.mu.Unlock()

	// Dial outside the lock — it can take seconds.
	sshClient, sftpClient, err := m.dial(s.UserID, s.Host, s.Port, s.Username)
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.sessions[id]
	if !ok || cur != s {
		// Closed meanwhile, or a concurrent request already reopened it.
		sftpClient.Close()
		sshClient.Close()
		if !ok {
			return nil, ErrSessionNotFound
		}
		return cur, nil
	}
	fresh := m.newSession(id, s.UserID, s.Host, s.Port, s.Username, sshClient, sftpClient)
	m.sessions[id] = fresh
	return fresh, nil
}

// Close shuts down the user's session and forgets it.
func (m *SessionManager) Close(id string, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	s, ok := m.sessions[id]
	if !ok || s.UserID != userID {
		return ErrSessionNotFound
	}
	shut(s)
	delete(m.sessions, id)
	return nil
}

// ExpireSessions shuts the connections of sessions past their TTL, and
// forgets them once their reopen window has passed too.
func (m *SessionManager) ExpireSessions() {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for id, s := range m.sessions {
		if now.After(s.ExpiresAt) {
			shut(s)
		}
		if now.After(s.ExpiresAt.Add(reopenWindow)) {
			delete(m.sessions, id)
		}
	}
}

// Stop closes all sessions. Call on server shutdown.
func (m *SessionManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()

	for id, s := range m.sessions {
		shut(s)
		delete(m.sessions, id)
	}
}

// dial connects to host with the user's agent key and opens SFTP on top.
func (m *SessionManager) dial(userID int64, host string, port int, username string) (*ssh.Client, *sftp.Client, error) {
	signer, err := m.agent.GetSigner(userID)
	if err != nil {
		return nil, nil, fmt.Errorf("no SSH key available: %w", err)
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
		return nil, nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}

	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		sshClient.Close()
		return nil, nil, fmt.Errorf("sftp subsystem: %w", err)
	}
	return sshClient, sftpClient, nil
}

func (m *SessionManager) newSession(id string, userID int64, host string, port int, username string, sshClient *ssh.Client, sftpClient *sftp.Client) *Session {
	now := time.Now()
	return &Session{
		ID:          id,
		UserID:      userID,
		Host:        host,
		Port:        port,
		Username:    username,
		SSHClient:   sshClient,
		SFTPClient:  sftpClient,
		ConnectedAt: now,
		ExpiresAt:   now.Add(m.ttl),
	}
}

// shut closes a session's connections once. The caller holds m.mu.
func shut(s *Session) {
	if s.closed {
		return
	}
	s.closed = true
	if s.SFTPClient != nil {
		s.SFTPClient.Close()
	}
	if s.SSHClient != nil {
		s.SSHClient.Close()
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
