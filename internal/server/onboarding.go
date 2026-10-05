package server

import (
	"encoding/json"
	"net/http"
	"strconv"

	"poggers.institute/freshbreath/internal/db"
)

// ── Onboarding ──────────────────────────────────────────────────────
//
// A fresh install has no users and no admin gate, so the control panel is
// wide open. Onboarding closes it in one step: the first Superuser, their
// SSH passphrase, and the built-in SSH record as the admin gate. Installs
// with their own plan (OIDC, or staying open) skip it, which only hides
// the prompt.

// onboardingNeeded reports whether the panel should offer onboarding: no
// users, no admin gate, and nobody has skipped it.
func (s *Server) onboardingNeeded() (bool, error) {
	fresh, err := s.installIsFresh()
	if err != nil || !fresh {
		return false, err
	}
	skipped, err := s.store.GetSetting("onboarding_skipped")
	return err == nil && skipped == "", err
}

// installIsFresh: nobody to sign in as, and no gate asking anyone to.
func (s *Server) installIsFresh() (bool, error) {
	rec, err := s.adminAuthRecord()
	if err != nil || rec != nil {
		return false, err
	}
	users, err := s.store.ListUsers()
	return err == nil && len(users) == 0, err
}

// coreOnboard creates the first Superuser with an SSH key sealed by
// passphrase and puts the admin gate on the built-in SSH record. Anything
// that fails partway is undone, so the install is never left gated with
// nobody able to get in.
func (s *Server) coreOnboard(actor *db.User, name, email, passphrase string) (*db.User, error) {
	if err := s.gate(actor, rolesSuperuser); err != nil {
		return nil, err
	}
	if name == "" || email == "" {
		return nil, cerr(http.StatusBadRequest, "name and email required")
	}
	if len(passphrase) < 8 {
		return nil, cerr(http.StatusBadRequest, "passphrase must be at least 8 characters")
	}
	fresh, err := s.installIsFresh()
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	if !fresh {
		return nil, cerr(http.StatusConflict, "this install is already set up")
	}
	sshID, err := s.store.BuiltinAuthID(db.AuthSSHKey)
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "built-in SSH record: %v", err)
	}

	user, err := s.store.CreateUser(name, email, "Superuser", "Active")
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	if _, err := s.coreGenerateSSHKey(actor, user, passphrase); err != nil {
		_ = s.store.DeleteUser(user.ID)
		return nil, err
	}
	if err := s.store.SetSetting("admin_auth_service", strconv.FormatInt(sshID, 10)); err != nil {
		_ = s.store.DeleteUser(user.ID)
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "onboarded first superuser", email)
	return s.store.GetUser(user.ID)
}

// coreSkipOnboarding stops the panel offering onboarding.
func (s *Server) coreSkipOnboarding(actor *db.User) error {
	if err := s.gate(actor, rolesSuperuser); err != nil {
		return err
	}
	if err := s.store.SetSetting("onboarding_skipped", "1"); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "skipped onboarding", "")
	return nil
}

// handleOnboarding: GET says whether to offer onboarding, POST runs it.
func (s *Server) handleOnboarding(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		needed, err := s.onboardingNeeded()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]bool{"needed": needed})
	case http.MethodPost:
		var req struct {
			Name       string `json:"name"`
			Email      string `json:"email"`
			Passphrase string `json:"passphrase"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
		user, err := s.coreOnboard(userFromContext(r.Context()), req.Name, req.Email, req.Passphrase)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]any{"user": user})
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleOnboardingSkip(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := s.coreSkipOnboarding(userFromContext(r.Context())); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
