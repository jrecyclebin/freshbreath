package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	billyutil "github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"

	"poggers.institute/freshbreath/internal/sshkit"
)

// TestMain installs go-git's in-process file:// transport once for the whole
// server package. The default file:// transport execs git-upload-pack (needs
// git on PATH); the server-client transport does it in-process against the
// on-disk repo. Package-scoped — the install in internal/sshkit's TestMain
// does NOT carry across to this package's test binary. Supports upload-pack
// (clone/pull) and receive-pack (push), so hermetic end-to-end git tests work
// without git on PATH.
func TestMain(m *testing.M) {
	client.InstallProtocol("file", server.NewClient(server.DefaultLoader))
	os.Exit(m.Run())
}

// ── shared test key ──

// The agent's real AddKey path re-decrypts (Argon2id) per call, but the key
// generation (which also runs Argon2id to encrypt) is expensive enough to do
// once per package run. sync.Once caches the SSHKeyInfo + parsed public key.
var (
	testKeyOnce sync.Once
	testKeyInfo *sshkit.SSHKeyInfo
	testKeyPub  ssh.PublicKey
	testKeyErr  error
)

func ensureTestKey() {
	testKeyOnce.Do(func() {
		info, err := sshkit.GenerateSSHKey("passphrase")
		if err != nil {
			testKeyErr = fmt.Errorf("generate test key: %w", err)
			return
		}
		testKeyInfo = info
		pub, _, _, _, err := ssh.ParseAuthorizedKey([]byte(info.PublicKey))
		if err != nil {
			testKeyErr = fmt.Errorf("parse test pubkey: %w", err)
			return
		}
		testKeyPub = pub
	})
}

// ── git fixture (bare repo with master + feature) ──

// gitFixture seeds a bare repo with a master branch (README.md, src/hello.txt,
// to-delete.txt) and a feature branch (adds feature.txt), both pushed
// explicitly to refs/heads/<name> so default-branch drift (master vs main)
// can't bite. The file:// URL targets the bare repo through the in-process
// transport.
type gitFixture struct {
	url     string
	bareDir string
	headSHA string // master head
	featSHA string // feature head
	treeSHA string // master head's tree sha
}

func newGitFixture(t *testing.T) *gitFixture {
	t.Helper()
	bareDir := filepath.Join(t.TempDir(), "bare.git")
	if err := os.MkdirAll(bareDir, 0o755); err != nil {
		t.Fatalf("mkdir bare: %v", err)
	}
	if _, err := git.PlainInit(bareDir, true); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	seedDir := filepath.Join(t.TempDir(), "seed")
	seedRepo, err := git.PlainInit(seedDir, false)
	if err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if _, err := seedRepo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{"file://" + bareDir},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}
	wt, err := seedRepo.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	sig := object.Signature{Name: "Seeder", Email: "seed@example.com", When: time.Unix(1700000000, 0)}
	seedFile(t, wt, "README.md", []byte("# demo\n"))
	seedFile(t, wt, "src/hello.txt", []byte("hello world\n"))
	seedFile(t, wt, "to-delete.txt", []byte("gone soon\n"))
	headSHA := seedAddCommit(t, seedRepo, wt, sig, "initial commit")
	pushRef(t, seedRepo, "refs/heads/master")

	// feature branch off master.
	if err := wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName("feature"),
		Create: true,
	}); err != nil {
		t.Fatalf("checkout feature: %v", err)
	}
	seedFile(t, wt, "feature.txt", []byte("feature work\n"))
	featSHA := seedAddCommit(t, seedRepo, wt, sig, "feature work")
	pushRef(t, seedRepo, "refs/heads/feature")

	headCommit, err := seedRepo.CommitObject(plumbing.NewHash(headSHA))
	if err != nil {
		t.Fatalf("head commit: %v", err)
	}
	tree, err := headCommit.Tree()
	if err != nil {
		t.Fatalf("head tree: %v", err)
	}
	return &gitFixture{
		url:     "file://" + bareDir,
		bareDir: bareDir,
		headSHA: headSHA,
		featSHA: featSHA,
		treeSHA: tree.Hash.String(),
	}
}

func seedFile(t *testing.T, wt *git.Worktree, path string, content []byte) {
	t.Helper()
	fs := wt.Filesystem
	if dir := filepath.Dir(path); dir != "." {
		if err := fs.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	if err := billyutil.WriteFile(fs, path, content, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func seedAddCommit(t *testing.T, repo *git.Repository, wt *git.Worktree, sig object.Signature, msg string) string {
	t.Helper()
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		t.Fatalf("add: %v", err)
	}
	h, err := wt.Commit(msg, &git.CommitOptions{Author: &sig, Committer: &sig})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return h.String()
}

// pushRef force-pushes the seed repo's current HEAD to dstRef on the bare
// repo. The explicit refspec (rather than HEAD's default) pins the remote
// branch name regardless of go-git's local default-branch choice.
func pushRef(t *testing.T, repo *git.Repository, dstRef string) {
	t.Helper()
	head, err := repo.Head()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	spec := config.RefSpec("+" + head.Name().String() + ":" + dstRef)
	if err := repo.Push(&git.PushOptions{RemoteName: "origin", RefSpecs: []config.RefSpec{spec}}); err != nil {
		t.Fatalf("push %s: %v", dstRef, err)
	}
}

// bareMasterCommit opens the bare repo's master head commit for post-push
// assertions (sha match, gpgsig block, tree contents).
func bareMasterCommit(t *testing.T, f *gitFixture) *object.Commit {
	t.Helper()
	repo, err := git.PlainOpen(f.bareDir)
	if err != nil {
		t.Fatalf("open bare: %v", err)
	}
	ref, err := repo.Reference(plumbing.ReferenceName("refs/heads/master"), false)
	if err != nil {
		t.Fatalf("master ref: %v", err)
	}
	commit, err := repo.CommitObject(ref.Hash())
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	return commit
}

// commitPayload returns the canonical signed payload (the bytes the signature
// was computed over) for a decoded commit.
func commitPayload(t *testing.T, commit *object.Commit) []byte {
	t.Helper()
	obj := &plumbing.MemoryObject{}
	if err := commit.EncodeWithoutSignature(obj); err != nil {
		t.Fatalf("encode payload: %v", err)
	}
	r, err := obj.Reader()
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	return b
}

// verifySSHSig unarmors an SSH SIGNATURE block and verifies it over payload
// under the "git" namespace with SHA-512 — what git stores in gpgsig (with
// gpg.format=ssh) and what `git verify-commit` / GitHub check.
func verifySSHSig(t *testing.T, payload []byte, armoredSig string, pub ssh.PublicKey) {
	t.Helper()
	sig, err := sshsig.Unarmor([]byte(armoredSig))
	if err != nil {
		t.Fatalf("unarmor: %v", err)
	}
	if err := sshsig.Verify(bytes.NewReader(payload), sig, pub, sshsig.HashSHA512, "git"); err != nil {
		t.Fatalf("sshsig verify: %v", err)
	}
}

// ── server auth environment ──

// gitEnv is a server with an app, an allowed ssh service, and (optionally) a
// real authenticated Admin user + planted SSH signer.
type gitEnv struct {
	srv      *Server
	appNonce string
	token    string // empty when auth is off (pre-auth env)
	userID   int64
}

// newGitEnvPreAuth sets up the app + allowed ssh service + Admin user while
// auth is still off (so createApp passes the synthetic-superuser gate), but
// does NOT flip auth on or mint a token. Used for the auth-off 401 test
// (synthetic -1 user reaches the handler then 401s at resolveGitUser).
func newGitEnvPreAuth(t *testing.T) *gitEnv {
	t.Helper()
	srv := newTestServer(t)
	appNonce := createApp(t, srv, "gitapp")
	sshSvcID, err := srv.store.EnsureSSHService()
	if err != nil {
		t.Fatalf("ensure ssh service: %v", err)
	}
	linkServiceToApp(t, srv, appNonce, strconv.FormatInt(sshSvcID, 10))
	user, err := srv.store.CreateUser("Alice", "alice@example.com", "Admin", "Active")
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return &gitEnv{srv: srv, appNonce: appNonce, userID: user.ID}
}

// newGitEnv builds the full auth stack: app + allowed ssh service + Admin user
// + a real identity token (auth flipped on by authTokenForUser). No signer is
// planted — call addSigner for tests that need one.
func newGitEnv(t *testing.T) *gitEnv {
	t.Helper()
	e := newGitEnvPreAuth(t)
	e.token = authTokenForUser(t, e.srv, "alice@example.com", "Alice", "Admin")
	return e
}

// addSigner plants the cached test key into the env's agent under the user's
// ID via the real AddKey path (package server can't reach sshkit's unexported
// entries, so we exercise the decrypt path that production uses). Returns the
// public key for signature verification.
func (e *gitEnv) addSigner(t *testing.T) ssh.PublicKey {
	t.Helper()
	ensureTestKey()
	if testKeyErr != nil {
		t.Fatalf("test key: %v", testKeyErr)
	}
	if err := e.srv.agentMgr.AddKey(e.userID, testKeyInfo, "passphrase", time.Hour); err != nil {
		t.Fatalf("add key to agent: %v", err)
	}
	return testKeyPub
}

// gitPost issues an authenticated POST to a /ssh/git/* route. The
// Authorization header is added only when the env has a token (so the
// pre-auth env exercises the auth-off path).
func gitPost(t *testing.T, e *gitEnv, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		r = bytes.NewReader(b)
	}
	headers := map[string]string{"X-App-Nonce": e.appNonce}
	if e.token != "" {
		headers["Authorization"] = "Bearer " + e.token
	}
	return testRequest(t, e.srv, "POST", path, r, headers)
}

func gitGet(t *testing.T, e *gitEnv, path string) *httptest.ResponseRecorder {
	t.Helper()
	headers := map[string]string{"X-App-Nonce": e.appNonce}
	if e.token != "" {
		headers["Authorization"] = "Bearer " + e.token
	}
	return testRequest(t, e.srv, "GET", path, nil, headers)
}

func decodeBody(t *testing.T, rr *httptest.ResponseRecorder, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(rr.Body.Bytes(), v); err != nil {
		t.Fatalf("decode response %d %q: %v", rr.Code, rr.Body.String(), err)
	}
}

func wantStatus(t *testing.T, rr *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rr.Code != want {
		t.Fatalf("status = %d, want %d, body = %q", rr.Code, want, rr.Body.String())
	}
}

// ── 401: auth off (synthetic -1 user reaches handler, resolveGitUser 401s) ──

func TestGitHTTP_AuthOff401(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnvPreAuth(t) // no token: auth still off
	rr := gitPost(t, e, "/ssh/git/branches", map[string]string{"url": f.url})
	wantStatus(t, rr, http.StatusUnauthorized)
}

// ── 401: empty agent, ssh:// (no dial — authFor short-circuits at GetSigner) ──

func TestGitHTTP_BranchesNoKeySSH(t *testing.T) {
	e := newGitEnv(t) // no signer planted
	rr := gitPost(t, e, "/ssh/git/branches", map[string]string{
		"url": "ssh://nonexistent.invalid:2999/x.git",
	})
	wantStatus(t, rr, http.StatusUnauthorized)
	if strings.Contains(rr.Body.String(), "dial") || strings.Contains(rr.Body.String(), "connect") {
		t.Errorf("body suggests a dial happened: %q", rr.Body.String())
	}
}

// ── 401: empty agent, file:// commit (signing mandatory — GetSigner first) ──

func TestGitHTTP_CommitNoKey(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	rr := gitPost(t, e, "/ssh/git/commit", sshkit.GitCommitRequest{
		URL:     f.url,
		Message: "x",
	})
	wantStatus(t, rr, http.StatusUnauthorized)
}

// ── 401: empty agent, sign (SignCommits calls GetSigner) ──

func TestGitHTTP_SignNoKey(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	when := time.Unix(1700000000, 0)
	rr := gitPost(t, e, "/ssh/git/sign", struct {
		Commits []sshkit.CommitToSign `json:"commits"`
	}{Commits: []sshkit.CommitToSign{{
		Tree:    f.treeSHA,
		Message: "x",
		Author:  &sshkit.CommitIdentity{Name: "Alice", Email: "alice@example.com", Date: when},
	}}})
	wantStatus(t, rr, http.StatusUnauthorized)
}

// ── 400: bad input ──

func TestGitHTTP_BadRequest(t *testing.T) {
	f := newGitFixture(t)
	cases := []struct {
		name string
		path string
		body interface{}
	}{
		{"branches empty url", "/ssh/git/branches", map[string]string{"url": ""}},
		{"pull empty url", "/ssh/git/pull", map[string]string{"url": ""}},
		{"commit empty message", "/ssh/git/commit", sshkit.GitCommitRequest{URL: f.url}},
		{"sign empty commits", "/ssh/git/sign", struct {
			Commits []sshkit.CommitToSign `json:"commits"`
		}{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := newGitEnv(t)
			rr := gitPost(t, e, tc.path, tc.body)
			wantStatus(t, rr, http.StatusBadRequest)
		})
	}
}

// ── 405: wrong method ──

func TestGitHTTP_MethodNotAllowed(t *testing.T) {
	e := newGitEnv(t)
	// The method check is the handler's first gate, before resolveGitUser or
	// any git work, so a GET on a POST-only route → 405 with no fixture needed.
	rr := gitGet(t, e, "/ssh/git/branches")
	wantStatus(t, rr, http.StatusMethodNotAllowed)
}

// ── 404: unknown branch (the mapRemoteErr fix — NoMatchingRefSpecError → 404) ──

func TestGitHTTP_UnknownBranch404(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	rr := gitPost(t, e, "/ssh/git/pull", map[string]interface{}{
		"url":    f.url,
		"branch": "nope",
	})
	wantStatus(t, rr, http.StatusNotFound)
}

// ── 404: missing pull path ──

func TestGitHTTP_MissingPath404(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	rr := gitPost(t, e, "/ssh/git/pull", map[string]interface{}{
		"url":   f.url,
		"paths": []string{"nope.txt"},
	})
	wantStatus(t, rr, http.StatusNotFound)
}

// ── 409: stale base ──

func TestGitHTTP_StaleBase409(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	e.addSigner(t)
	rr := gitPost(t, e, "/ssh/git/commit", sshkit.GitCommitRequest{
		URL:        f.url,
		BaseCommit: strings.Repeat("0", 40), // != head
		Message:    "x",
	})
	wantStatus(t, rr, http.StatusConflict)
}

// ── 409: nothing to commit ──

func TestGitHTTP_NothingToCommit409(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	e.addSigner(t)
	rr := gitPost(t, e, "/ssh/git/commit", sshkit.GitCommitRequest{
		URL:        f.url,
		BaseCommit: f.headSHA,
		Message:    "no-op",
	})
	wantStatus(t, rr, http.StatusConflict)
}

// ── end-to-end happy path: branches → pull tree → pull file → commit ──

func TestGitHTTP_HappyPath(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	pub := e.addSigner(t)

	// 1. branches — head + both refs.
	rr := gitPost(t, e, "/ssh/git/branches", map[string]string{"url": f.url})
	wantStatus(t, rr, http.StatusOK)
	var br struct {
		Head     string              `json:"head"`
		Branches []sshkit.BranchInfo `json:"branches"`
	}
	decodeBody(t, rr, &br)
	if br.Head != f.headSHA {
		t.Errorf("head = %s, want %s", br.Head, f.headSHA)
	}
	got := map[string]string{}
	for _, b := range br.Branches {
		got[b.Name] = b.Commit
	}
	if got["master"] != f.headSHA {
		t.Errorf("master = %s, want %s", got["master"], f.headSHA)
	}
	if got["feature"] != f.featSHA {
		t.Errorf("feature = %s, want %s", got["feature"], f.featSHA)
	}
	if len(br.Branches) != 2 {
		t.Errorf("len(branches) = %d, want 2 (%v)", len(br.Branches), br.Branches)
	}

	// 2. pull tree (discovery) — listing + head sha, no files.
	rr = gitPost(t, e, "/ssh/git/pull", map[string]interface{}{"url": f.url})
	wantStatus(t, rr, http.StatusOK)
	var snap sshkit.GitSnapshot
	decodeBody(t, rr, &snap)
	if snap.Commit != f.headSHA {
		t.Errorf("commit = %s, want %s", snap.Commit, f.headSHA)
	}
	have := map[string]bool{}
	for _, entry := range snap.Tree {
		have[entry.Path] = true
	}
	for _, want := range []string{"README.md", "src/hello.txt", "to-delete.txt"} {
		if !have[want] {
			t.Errorf("tree missing %q; tree = %v", want, snap.Tree)
		}
	}
	if len(snap.Files) != 0 {
		t.Errorf("expected no files on discovery, got %d", len(snap.Files))
	}

	// 3. pull a single file.
	rr = gitPost(t, e, "/ssh/git/pull", map[string]interface{}{
		"url":   f.url,
		"paths": []string{"README.md"},
	})
	wantStatus(t, rr, http.StatusOK)
	var snap2 sshkit.GitSnapshot
	decodeBody(t, rr, &snap2)
	if len(snap2.Files) != 1 {
		t.Fatalf("files = %d, want 1 (%v)", len(snap2.Files), snap2.Files)
	}
	if snap2.Files[0].Path != "README.md" {
		t.Errorf("path = %q, want README.md", snap2.Files[0].Path)
	}
	if string(snap2.Files[0].Content) != "# demo\n" {
		t.Errorf("content = %q, want %q", snap2.Files[0].Content, "# demo\n")
	}

	// 4. commit — writes + deletes, with baseCommit from the pulled head.
	rr = gitPost(t, e, "/ssh/git/commit", sshkit.GitCommitRequest{
		URL:        f.url,
		BaseCommit: f.headSHA,
		Message:    "add new, delete stale",
		Writes: []sshkit.GitFile{
			{Path: "new.txt", Content: []byte("fresh\n")},
			{Path: "src/deeper/again.txt", Content: []byte("nested\n")},
		},
		Deletes: []string{"to-delete.txt"},
	})
	wantStatus(t, rr, http.StatusOK)
	var commit struct {
		Commit string `json:"commit"`
		Status string `json:"status"`
		Signed bool   `json:"signed"`
	}
	decodeBody(t, rr, &commit)
	if commit.Status != "pushed" {
		t.Errorf("status = %q, want pushed", commit.Status)
	}
	if !commit.Signed {
		t.Error("signed = false, want true")
	}

	// Inspect the bare repo: returned sha == master head, gpgsig is an SSH
	// block, signature verifies, tree reflects writes + delete.
	c := bareMasterCommit(t, f)
	if c.Hash.String() != commit.Commit {
		t.Errorf("returned commit = %s, want bare head %s", commit.Commit, c.Hash.String())
	}
	if !strings.Contains(c.PGPSignature, "-----BEGIN SSH SIGNATURE-----") {
		t.Errorf("PGPSignature missing SSH header: %q", c.PGPSignature)
	}
	verifySSHSig(t, commitPayload(t, c), c.PGPSignature, pub)

	tree, err := c.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if _, err := tree.FindEntry("new.txt"); err != nil {
		t.Errorf("new.txt missing after push: %v", err)
	}
	if _, err := tree.FindEntry("src/deeper/again.txt"); err != nil {
		t.Errorf("src/deeper/again.txt missing after push: %v", err)
	}
	if _, err := tree.FindEntry("to-delete.txt"); err == nil {
		t.Error("to-delete.txt still present after push")
	}
}

// ── sign happy path: structured commit → sha + signature + github body ──

func TestGitHTTP_Sign(t *testing.T) {
	f := newGitFixture(t)
	e := newGitEnv(t)
	pub := e.addSigner(t)
	when := time.Unix(1700000123, 0)

	rr := gitPost(t, e, "/ssh/git/sign", struct {
		Commits []sshkit.CommitToSign `json:"commits"`
	}{Commits: []sshkit.CommitToSign{{
		Tree:    f.treeSHA,
		Message: "structured commit\n",
		Author:  &sshkit.CommitIdentity{Name: "Alice", Email: "alice@example.com", Date: when},
	}}})
	wantStatus(t, rr, http.StatusOK)
	var resp struct {
		Commits []sshkit.SignedCommit `json:"commits"`
	}
	decodeBody(t, rr, &resp)
	if len(resp.Commits) != 1 {
		t.Fatalf("commits = %d, want 1", len(resp.Commits))
	}
	sc := resp.Commits[0]

	if sc.SHA == "" {
		t.Error("SHA empty, want a computed commit id")
	}
	if !strings.Contains(sc.Signature, "-----BEGIN SSH SIGNATURE-----") {
		t.Errorf("signature missing SSH header: %q", sc.Signature)
	}
	verifySSHSig(t, sc.Payload, sc.Signature, pub)

	// GitHub body mirrors the inputs (verbatim POST /repos/.../git/commits).
	if sc.GitHub.Message != "structured commit\n" {
		t.Errorf("github.Message = %q", sc.GitHub.Message)
	}
	if sc.GitHub.Tree != f.treeSHA {
		t.Errorf("github.Tree = %s, want %s", sc.GitHub.Tree, f.treeSHA)
	}
	if sc.GitHub.Author.Name != "Alice" {
		t.Errorf("github.Author.Name = %q", sc.GitHub.Author.Name)
	}
	if sc.GitHub.Signature != sc.Signature {
		t.Error("github.Signature != sc.Signature")
	}
}
