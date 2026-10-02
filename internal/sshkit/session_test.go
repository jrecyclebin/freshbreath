package sshkit

import (
	"errors"
	"testing"
	"time"
)

// A manager holding one session for user 7, without live connections —
// enough to exercise lookup, ownership and the reopen path up to the dial.
func testManager(expiresAt time.Time) *SessionManager {
	m := NewSessionManager(NewAgentManager(), nil, time.Hour)
	m.sessions["s1"] = &Session{ID: "s1", UserID: 7, Host: "example.com", Port: 22, ExpiresAt: expiresAt}
	return m
}

func TestSessionGetAnswersOnlyItsOwner(t *testing.T) {
	m := testManager(time.Now().Add(time.Hour))
	if _, err := m.Get("s1", 7); err != nil {
		t.Fatalf("owner: %v", err)
	}
	if _, err := m.Get("s1", 8); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("other user: err = %v, want ErrSessionNotFound", err)
	}
	if err := m.Close("s1", 8); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("other user close: err = %v, want ErrSessionNotFound", err)
	}
}

// Past its TTL with no agent key loaded, the reopen asks for a login and
// the id stays reopenable.
func TestExpiredSessionReopenNeedsAgentKey(t *testing.T) {
	m := testManager(time.Now().Add(-time.Minute))
	if _, err := m.Get("s1", 7); !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
	if !m.sessions["s1"].closed {
		t.Fatal("expired session's connections were not shut")
	}
	if _, err := m.Get("s1", 7); !errors.Is(err, ErrNoKey) {
		t.Fatalf("second try: err = %v, want ErrNoKey (id should still be held)", err)
	}
}

func TestExpireSessionsForgetsAfterReopenWindow(t *testing.T) {
	m := testManager(time.Now().Add(-time.Minute))
	m.ExpireSessions()
	if _, ok := m.sessions["s1"]; !ok {
		t.Fatal("session forgotten inside its reopen window")
	}
	m.sessions["s1"].ExpiresAt = time.Now().Add(-reopenWindow - time.Minute)
	m.ExpireSessions()
	if _, err := m.Get("s1", 7); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("err = %v, want ErrSessionNotFound", err)
	}
}
