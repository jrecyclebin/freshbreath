package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"poggers.institute/freshbreath/internal/formats"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Virtual-service elicitation steps (FORM / URL directives). Both suspend
// the tool at the step (SEP-2322 multi-round-trip): the tools/call handler
// returns an input-required result, the host answers the elicitation through
// its own UI and retries the call, and the handler resumes from the stored
// suspension state. The go-sdk bridges old-protocol hosts transparently by
// fulfilling the input request with a blocking ss.Elicit.
//
// Suspend-resume state lives in pendingElicits, keyed by the crypto-random
// elicitation ID the executor generates. That ID doubles as the RequestState
// echoed by the retry and, for URL steps, the ElicitationID on the wire.
// Nothing is persisted — process restart clears it, and an outstanding
// suspension dies with it (the retry surfaces "unknown or expired"). Entries
// are swept by the cleanup ticker next to the act tickets.
//
// URL steps additionally get a public completion callback,
// /elicitation/{id}: the out-of-band target (or a page it redirects to) hits
// it when the user finishes, which records any query/body payload — it
// becomes the resumed scope — and fires notifications/elicitation/complete
// so hosts that hold the URL prompt open until it arrives retry the call.

const (
	elicitTTL = 15 * time.Minute

	// inputRequestID is the single key we address an elicitation by inside
	// InputRequests/InputResponses. One suspension = one open elicitation;
	// a resumed run that hits another elicitation suspends into a fresh
	// entry with a fresh ID.
	inputRequestID = "0"
)

type pendingElicit struct {
	id        string
	created   time.Time
	slug      string // service slug, for logs
	session   *mcp.ServerSession
	resume    *formats.ResumeState
	sqlRunner formats.SQLRunner
	mode      string // "form" | "url"
	completed bool
	payload   map[string]interface{} // completion-callback data (url mode)
}

// pendingElicits is the in-memory suspension store (the actTickets
// precedent): one map, one mutex, swept by the cleanup ticker.
type pendingElicits struct {
	mu      sync.Mutex
	entries map[string]*pendingElicit
}

func (p *pendingElicits) add(e *pendingElicit, now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries == nil {
		p.entries = make(map[string]*pendingElicit)
	}
	// Lazy sweep: cheap, and keeps entries from outliving the TTL even
	// when the periodic sweeper races a busy patch.
	for id, old := range p.entries {
		if now.Sub(old.created) > elicitTTL {
			delete(p.entries, id)
		}
	}
	e.created = now
	p.entries[e.id] = e
}

func (p *pendingElicits) get(id string) *pendingElicit {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e != nil && time.Since(e.created) > elicitTTL {
		delete(p.entries, id)
		return nil
	}
	return e
}

// take removes and returns the entry — the resume consumes it. A second
// retry with the same RequestState therefore finds nothing and surfaces the
// expiry error, which is the honest outcome for a replayed capability.
func (p *pendingElicits) take(id string) *pendingElicit {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	delete(p.entries, id)
	return e
}

// complete marks an entry completed with the callback payload WITHOUT
// consuming it: the retrying tools/call still needs the stored suspension to
// resume from. Only the resume path (take) consumes an entry.
func (p *pendingElicits) complete(id string, payload map[string]interface{}) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[id]
	if e == nil || time.Since(e.created) > elicitTTL {
		return false
	}
	e.completed = true
	e.payload = payload
	return true
}

func (p *pendingElicits) sweep(now time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, e := range p.entries {
		if now.Sub(e.created) > elicitTTL {
			delete(p.entries, id)
		}
	}
}

// ── Handler glue ─────────────────────────────────────────────────────

// resumeVirtualTool handles a tools/call retry carrying a RequestState: it
// consumes the stored suspension, applies the client's elicitation response
// (plus any completion-callback payload for URL steps), and continues the
// run. It returns the tool result — or, if the resumed run reaches another
// elicitation, a fresh input-required result.
func (s *Server) resumeVirtualTool(tools []formats.VirtualTool, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	entry := s.pendingElicits.take(req.Params.RequestState)
	if entry == nil {
		return mcpToolError("elicitation state unknown or expired — call the tool again"), nil
	}
	if req.Params.Name != entry.resume.ToolName {
		return mcpToolError("elicitation state belongs to tool %q, not %q", entry.resume.ToolName, req.Params.Name), nil
	}
	if entry.session != nil && entry.session.ID() != req.Session.ID() {
		return mcpToolError("elicitation state belongs to a different session"), nil
	}
	resp, ok := req.Params.InputResponses[inputRequestID].(*mcp.ElicitResult)
	if !ok || resp == nil {
		return mcpToolError("retry is missing the elicitation response"), nil
	}
	response := &formats.ElicitResponse{Action: resp.Action, Content: resp.Content}
	if entry.mode == "url" && entry.completed && len(entry.payload) > 0 {
		// The out-of-band completion carried data (e.g. OAuth callback
		// query params); it outranks the bare accept the host echoes.
		response.Content = entry.payload
	}
	entry.resume.Response = response
	return s.answerRun(tools, entry.resume, entry.sqlRunner, entry.slug, req.Session)
}

// answerRun resumes a suspended run and converts a suspension into the
// input-required result (a resumed run may reach another elicitation).
func (s *Server) answerRun(tools []formats.VirtualTool, resume *formats.ResumeState, sqlRunner formats.SQLRunner, slug string, session *mcp.ServerSession) (*mcp.CallToolResult, error) {
	result, err := formats.ExecuteVirtualTool(s.httpClient, tools, resume.ToolName, resume.Args, resume.Auth, sqlRunner,
		&formats.ExecContext{Hooks: s.elicitHooks(), Resume: resume})
	var susp *formats.ErrSuspend
	if errors.As(err, &susp) {
		return s.suspendVirtualTool(susp.Susp, slug, session, sqlRunner), nil
	}
	if err != nil {
		return mcpToolError("%v", err), nil
	}
	resultJSON, _ := json.Marshal(result)
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: string(resultJSON)}},
	}, nil
}

// elicitHooks wires the public completion-callback URL into the executor.
// The callback is global (IDs are unguessable and never scoped by service),
// so no per-service knowledge is needed.
func (s *Server) elicitHooks() *formats.VirtualHooks {
	return &formats.VirtualHooks{
		ElicitationURL: func(elicitationID string) string {
			return s.config.PublicBaseURL + "/elicitation/" + elicitationID
		},
	}
}

// suspendVirtualTool stores a suspended execution and returns the
// input-required result the host fulfills and retries. slug and session come
// from the serving request; the suspension itself carries the executor state
// and the elicitation ID it generated.
func (s *Server) suspendVirtualTool(susp *formats.Suspension, slug string, session *mcp.ServerSession, sqlRunner formats.SQLRunner) *mcp.CallToolResult {
	entry := &pendingElicit{
		id:        susp.ElicitationID,
		slug:      slug,
		session:   session,
		resume:    susp.ResumeState(),
		sqlRunner: sqlRunner,
		mode:      susp.Elicit.Mode,
	}
	s.pendingElicits.add(entry, time.Now())

	params := &mcp.ElicitParams{Message: susp.Elicit.Message}
	if susp.Elicit.Mode == "form" {
		params.RequestedSchema = susp.Elicit.Schema
	} else {
		params.URL = susp.Elicit.URL
		params.ElicitationID = susp.ElicitationID
	}
	return &mcp.CallToolResult{
		InputRequests: mcp.InputRequestMap{inputRequestID: params},
		RequestState:  susp.ElicitationID,
	}
}

// ── Completion callback ──────────────────────────────────────────────

// handleElicitationComplete serves /elicitation/{id}: the public callback the
// out-of-band target (or a page it redirects to) hits when the user finishes
// a URL elicitation step. It records the completion payload — query params
// on GET, a JSON body on POST — which becomes the resumed scope, and fires
// notifications/elicitation/complete so hosts that hold the URL prompt open
// until it arrives retry the call.
//
// Mounted bare: the callback comes from the target site or the user's
// browser, never an authenticated MCP client. The capability is the
// unguessable elicitation ID; possessing it is the whole auth model, and
// completing one only unblocks the pending run that issued it.
func (s *Server) handleElicitationComplete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	entry := s.pendingElicits.get(id)
	if entry == nil {
		http.Error(w, "unknown or expired elicitation", http.StatusNotFound)
		return
	}
	if entry.mode != "url" {
		http.Error(w, "elicitation does not take a completion callback", http.StatusBadRequest)
		return
	}
	var payload map[string]interface{}
	switch r.Method {
	case http.MethodGet:
		payload = map[string]interface{}{}
		for k, v := range r.URL.Query() {
			if k == "" {
				continue
			}
			payload[k] = strings.Join(v, ",")
		}
	case http.MethodPost:
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
		if err != nil {
			http.Error(w, "read body: "+err.Error(), http.StatusBadRequest)
			return
		}
		if len(body) > 0 {
			if err := json.Unmarshal(body, &payload); err != nil {
				http.Error(w, "body must be a JSON object", http.StatusBadRequest)
				return
			}
		}
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if !s.pendingElicits.complete(id, payload) {
		http.Error(w, "unknown or expired elicitation", http.StatusNotFound)
		return
	}

	if entry.session != nil {
		// Best-effort: the notification rides the session's standalone SSE
		// stream (the tools/call stream that started it is long gone). A
		// host without a GET stream open just won't auto-retry.
		if err := entry.session.NotifyElicitationComplete(r.Context(), &mcp.ElicitationCompleteParams{ElicitationID: id}); err != nil {
			slog.Warn("elicitation complete notification failed", "id", id, "err", err)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<!doctype html><title>Complete</title><body style="font-family:system-ui;max-width:32rem;margin:15vh auto;text-align:center"><h1>✓ Complete</h1><p>You can close this window and return to your assistant.</p></body>`)
}
