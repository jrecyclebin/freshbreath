package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/tailscale/tailcat"

	"poggers.institute/freshbreath/internal/db"
	"poggers.institute/freshbreath/internal/utils"
)

// ── Remotes ─────────────────────────────────────────────────────────
//
// A remote is another Fresh Breath this one reaches through the remote's
// tailcat address, so the panel can drive it as an extension of this
// server. This server keeps only the address (sealed, and fixed for the
// remote's life: it is the remote's identity). Sessions on the remote
// belong to the browser, which keeps them at frbr:tunnel:<id>, and act on
// the remote as whichever of its users signed in. See
// design/remotes-todo.md.

// remoteFingerprint is a short tag for telling remotes apart without
// showing their address: the relay region and the start of the remote's
// path-discovery key, which travels in the clear anyway.
func remoteFingerprint(ci tailcat.ConnInfo) string {
	region := fmt.Sprintf("r%d", ci.RegionID)
	if len(ci.Region) > 0 && ci.Region[0].RegionCode != "" {
		region = ci.Region[0].RegionCode
	}
	disco := strings.TrimPrefix(ci.ServerDiscoPublic.String(), "discokey:")
	if len(disco) > 8 {
		disco = disco[:8]
	}
	return region + "-" + disco
}

func (s *Server) coreCreateRemote(actor *db.User, name, address string) (*db.Remote, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	name, address = strings.TrimSpace(name), strings.TrimSpace(address)
	if name == "" {
		return nil, cerr(http.StatusBadRequest, "name required")
	}
	ci, err := tailcat.ParseAddr(tailcat.Addr(address))
	if err != nil {
		return nil, cerr(http.StatusBadRequest, "address is not a tailcat address: %v", err)
	}
	r := &db.Remote{
		ID:          utils.GenNonce(),
		Name:        name,
		Address:     address,
		Fingerprint: remoteFingerprint(ci),
		CreatedBy:   actor.ID,
	}
	if err := s.store.CreateRemote(r); err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "added remote", r.Name+" ("+r.Fingerprint+")")
	return s.store.GetRemote(r.ID)
}

func (s *Server) coreListRemotes(actor *db.User) ([]*db.Remote, error) {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return nil, err
	}
	remotes, err := s.store.ListRemotes()
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "%v", err)
	}
	return remotes, nil
}

func (s *Server) coreRenameRemote(actor *db.User, id, name string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	if name = strings.TrimSpace(name); name == "" {
		return cerr(http.StatusBadRequest, "name required")
	}
	r, err := s.store.GetRemote(id)
	if err != nil {
		return cerr(http.StatusNotFound, "remote not found")
	}
	if err := s.store.RenameRemote(id, name); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "renamed remote", r.Name+" → "+name)
	return nil
}

func (s *Server) coreDeleteRemote(actor *db.User, id string) error {
	if err := s.gate(actor, rolesAdminPlus); err != nil {
		return err
	}
	r, err := s.store.GetRemote(id)
	if err != nil {
		return cerr(http.StatusNotFound, "remote not found")
	}
	if err := s.store.DeleteRemote(id); err != nil {
		return cerr(http.StatusInternalServerError, "%v", err)
	}
	s.audit(actor, "deleted remote", r.Name+" ("+r.Fingerprint+")")
	return nil
}

// handleRemotes serves GET (list) and POST {"name", "address"} (add) on
// /api/remotes.
func (s *Server) handleRemotes(w http.ResponseWriter, r *http.Request) {
	actor := actorFromContext(r)
	switch r.Method {
	case http.MethodGet:
		remotes, err := s.coreListRemotes(actor)
		if err != nil {
			writeErr(w, err)
			return
		}
		if remotes == nil {
			remotes = []*db.Remote{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"remotes": remotes})
	case http.MethodPost:
		var body struct {
			Name    string `json:"name"`
			Address string `json:"address"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		remote, err := s.coreCreateRemote(actor, body.Name, body.Address)
		if err != nil {
			writeErr(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(remote)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleRemoteDetail serves /api/remotes/{id}: PUT {"name"} renames,
// DELETE removes. An address can't be changed — a new address is a new
// remote.
func (s *Server) handleRemoteDetail(w http.ResponseWriter, r *http.Request) {
	id, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/remotes/"), "/")
	if id == "" || rest != "" {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	actor := actorFromContext(r)
	switch r.Method {
	case http.MethodPut:
		var body map[string]*string
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, jsonBodyMaxBytes)).Decode(&body); err != nil {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		if _, ok := body["address"]; ok {
			http.Error(w, "A remote's address can't change. Delete the remote and add it again (you'll sign in to it again too).", http.StatusBadRequest)
			return
		}
		if body["name"] == nil {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if err := s.coreRenameRemote(actor, id, *body["name"]); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case http.MethodDelete:
		if err := s.coreDeleteRemote(actor, id); err != nil {
			writeErr(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}
