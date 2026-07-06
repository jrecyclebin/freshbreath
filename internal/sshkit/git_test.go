package sshkit

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
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
)

const testUserID int64 = 1

// TestMain installs go-git's in-process file:// transport once for the whole
// package. The default file:// transport execs git-upload-pack (needs git on
// PATH); the server-client transport does it in-process against the on-disk
// repo. DefaultLoader chroots on osfs.New("") so absolute t.TempDir() paths
// resolve. Process-global and idempotent; safe because these tests aren't
// parallel. Supports both upload-pack (clone/pull) and receive-pack (push),
// so the hermetic CommitPush suite works without git on PATH.
func TestMain(m *testing.M) {
	client.InstallProtocol("file", server.NewClient(server.DefaultLoader))
	os.Exit(m.Run())
}

// ── fixtures ──

// gitFixture seeds a bare repo with a master branch (README.md, src/hello.txt,
// to-delete.txt) and a feature branch (adds feature.txt), both pushed
// explicitly to refs/heads/<name> so the default-branch drift (master vs main)
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

// testAgentWithSigner builds an AgentManager with a real ed25519 signer planted
// directly into entries[testUserID], skipping the slow Argon2id key-decrypt
// path. Legitimate because this test is package sshkit (reaches unexported
// fields). Returns the matching public key for signature verification.
func testAgentWithSigner(t *testing.T) (*AgentManager, ssh.PublicKey) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519 gen: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	m := NewAgentManager()
	m.entries[testUserID] = &AgentEntry{Expiry: time.Now().Add(time.Hour), Signer: signer}
	return m, signer.PublicKey()
}

func emptyAgent() *AgentManager { return NewAgentManager() }

// verifySSHSig unarmors an SSH SIGNATURE block and verifies it over payload
// under the "git" namespace with SHA-512 — exactly what git stores in gpgsig
// (with gpg.format=ssh) and what `git verify-commit` / GitHub check.
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

// bareMaster opens the bare repo's master and returns its commit + tree.
func bareMaster(t *testing.T, f *gitFixture) (*git.Repository, *object.Commit, *object.Tree) {
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
	tree, err := commit.Tree()
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	return repo, commit, tree
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

// ── Branches ──

func TestGitGateway_Branches(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil) // file:// needs no signer
	head, branches, err := gw.Branches(testUserID, f.url)
	if err != nil {
		t.Fatalf("branches: %v", err)
	}
	if head != f.headSHA {
		t.Errorf("head = %s, want %s", head, f.headSHA)
	}
	got := map[string]string{}
	for _, b := range branches {
		got[b.Name] = b.Commit
	}
	if got["master"] != f.headSHA {
		t.Errorf("master = %s, want %s", got["master"], f.headSHA)
	}
	if got["feature"] != f.featSHA {
		t.Errorf("feature = %s, want %s", got["feature"], f.featSHA)
	}
	if len(branches) != 2 {
		t.Errorf("len(branches) = %d, want 2 (%v)", len(branches), branches)
	}
}

// ── Pull ──

func TestGitGateway_PullDiscovery(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil)
	snap, err := gw.Pull(testUserID, f.url, "", nil)
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if snap.Commit != f.headSHA {
		t.Errorf("commit = %s, want %s", snap.Commit, f.headSHA)
	}
	have := map[string]bool{}
	for _, e := range snap.Tree {
		have[e.Path] = true
	}
	for _, want := range []string{"README.md", "src/hello.txt", "to-delete.txt"} {
		if !have[want] {
			t.Errorf("tree missing %q; tree = %v", want, snap.Tree)
		}
	}
	if len(snap.Files) != 0 {
		t.Errorf("expected no files on discovery, got %d", len(snap.Files))
	}
}

func TestGitGateway_PullSelectiveFile(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil)
	snap, err := gw.Pull(testUserID, f.url, "", []string{"README.md"})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(snap.Files) != 1 {
		t.Fatalf("files = %d, want 1 (%v)", len(snap.Files), snap.Files)
	}
	if snap.Files[0].Path != "README.md" {
		t.Errorf("path = %q, want README.md", snap.Files[0].Path)
	}
	if string(snap.Files[0].Content) != "# demo\n" {
		t.Errorf("content = %q, want %q", snap.Files[0].Content, "# demo\n")
	}
}

func TestGitGateway_PullNestedDir(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil)
	snap, err := gw.Pull(testUserID, f.url, "", []string{"src"})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if len(snap.Files) != 1 {
		t.Fatalf("files = %d, want 1 (%v)", len(snap.Files), snap.Files)
	}
	if snap.Files[0].Path != "src/hello.txt" {
		t.Errorf("path = %q, want src/hello.txt", snap.Files[0].Path)
	}
	if string(snap.Files[0].Content) != "hello world\n" {
		t.Errorf("content = %q, want %q", snap.Files[0].Content, "hello world\n")
	}
}

func TestGitGateway_PullNonDefaultBranch(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil)
	snap, err := gw.Pull(testUserID, f.url, "feature", []string{"feature.txt"})
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if snap.Commit != f.featSHA {
		t.Errorf("commit = %s, want %s", snap.Commit, f.featSHA)
	}
	if snap.Branch != "feature" {
		t.Errorf("branch = %q, want feature", snap.Branch)
	}
	if len(snap.Files) != 1 || snap.Files[0].Path != "feature.txt" {
		t.Fatalf("files = %v, want [feature.txt]", snap.Files)
	}
	if string(snap.Files[0].Content) != "feature work\n" {
		t.Errorf("content = %q, want %q", snap.Files[0].Content, "feature work\n")
	}
}

func TestGitGateway_PullMissingPath(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil)
	_, err := gw.Pull(testUserID, f.url, "", []string{"nope.txt"})
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestGitGateway_PullUnknownBranch(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil)
	_, err := gw.Pull(testUserID, f.url, "nope", nil)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

// ── CommitPush ──

func TestGitGateway_CommitPush(t *testing.T) {
	f := newGitFixture(t)
	agent, pub := testAgentWithSigner(t)
	gw := NewGitGateway(agent, nil) // file://: no host key callback
	sha, err := gw.CommitPush(testUserID, GitCommitRequest{
		URL:         f.url,
		BaseCommit:  f.headSHA,
		Message:     "add new, delete stale",
		AuthorName:  "Alice",
		AuthorEmail: "alice@example.com",
		Writes: []GitFile{
			{Path: "new.txt", Content: []byte("fresh\n")},
			{Path: "src/deeper/again.txt", Content: []byte("nested\n")},
		},
		Deletes: []string{"to-delete.txt"},
	})
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	_, commit, tree := bareMaster(t, f)
	if commit.Hash.String() != sha {
		t.Errorf("returned sha = %s, want bare head %s", sha, commit.Hash.String())
	}
	if !strings.Contains(commit.PGPSignature, "-----BEGIN SSH SIGNATURE-----") {
		t.Errorf("PGPSignature missing SSH header: %q", commit.PGPSignature)
	}
	verifySSHSig(t, commitPayload(t, commit), commit.PGPSignature, pub)

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

func TestGitGateway_CommitPushStaleBase(t *testing.T) {
	f := newGitFixture(t)
	agent, _ := testAgentWithSigner(t)
	gw := NewGitGateway(agent, nil)
	_, err := gw.CommitPush(testUserID, GitCommitRequest{
		URL:         f.url,
		BaseCommit:  strings.Repeat("0", 40),
		Message:     "x",
		AuthorName:  "Alice",
		AuthorEmail: "alice@example.com",
	})
	if !errors.Is(err, ErrStaleBase) {
		t.Fatalf("err = %v, want ErrStaleBase", err)
	}
}

func TestGitGateway_CommitPushNothingToCommit(t *testing.T) {
	f := newGitFixture(t)
	agent, _ := testAgentWithSigner(t)
	gw := NewGitGateway(agent, nil)
	_, err := gw.CommitPush(testUserID, GitCommitRequest{
		URL:         f.url,
		BaseCommit:  f.headSHA,
		Message:     "no-op",
		AuthorName:  "Alice",
		AuthorEmail: "alice@example.com",
	})
	if !errors.Is(err, ErrNothingToCommit) {
		t.Fatalf("err = %v, want ErrNothingToCommit", err)
	}
}

// CommitPush on file:// with no key → ErrNoKey. Pins signing-mandatory even for
// file:// (where the transport needs no auth); GetSigner runs before the clone.
func TestGitGateway_CommitPushFileNoKey(t *testing.T) {
	f := newGitFixture(t)
	gw := NewGitGateway(emptyAgent(), nil)
	_, err := gw.CommitPush(testUserID, GitCommitRequest{
		URL:         f.url,
		Message:     "x",
		AuthorName:  "Alice",
		AuthorEmail: "alice@example.com",
	})
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

// ── No-dial probe ──

// SSHNoDialOnNoKey proves authFor short-circuits at GetSigner before any dial:
// a host that doesn't listen returns ErrNoKey (not a connection error). This
// pins the 401-offline contract the server layer relies on.
func TestGitGateway_SSHNoDialOnNoKey(t *testing.T) {
	gw := NewGitGateway(emptyAgent(), nil)
	_, _, err := gw.Branches(testUserID, "ssh://nonexistent.invalid:2999/x.git")
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey (no dial should have happened)", err)
	}
}

// ── SignCommits ──

func TestGitGateway_SignCommitsStructured(t *testing.T) {
	f := newGitFixture(t)
	agent, pub := testAgentWithSigner(t)
	gw := NewGitGateway(agent, nil)
	when := time.Unix(1700000123, 0)
	out, err := gw.SignCommits(testUserID, []CommitToSign{{
		Tree:    f.treeSHA,
		Message: "structured commit\n",
		Author:  &CommitIdentity{Name: "Alice", Email: "alice@example.com", Date: when},
	}})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sc := out[0]

	// Payload byte-equals a reference EncodeWithoutSignature of the same fields.
	ref := &object.Commit{
		Author:    object.Signature{Name: "Alice", Email: "alice@example.com", When: when},
		Committer: object.Signature{Name: "Alice", Email: "alice@example.com", When: when},
		Message:   "structured commit\n",
		TreeHash:  plumbing.NewHash(f.treeSHA),
	}
	refObj := &plumbing.MemoryObject{}
	if err := ref.EncodeWithoutSignature(refObj); err != nil {
		t.Fatalf("ref encode: %v", err)
	}
	rr, _ := refObj.Reader()
	refPayload, _ := io.ReadAll(rr)
	if !bytes.Equal(refPayload, sc.Payload) {
		t.Errorf("payload mismatch:\n got %q\nwant %q", sc.Payload, refPayload)
	}

	verifySSHSig(t, sc.Payload, sc.Signature, pub)

	// SHA == hash of the full encode with the gpgsig set (ed25519 sshsig is
	// deterministic, so the caller can reproduce and pre-chain the sha).
	ref.PGPSignature = sc.Signature
	fullObj := &plumbing.MemoryObject{}
	if err := ref.Encode(fullObj); err != nil {
		t.Fatalf("full encode: %v", err)
	}
	if got := fullObj.Hash().String(); got != sc.SHA {
		t.Errorf("SHA = %s, want %s", got, sc.SHA)
	}

	// GitHub body mirrors the inputs (verbatim POST /repos/.../git/commits).
	if sc.GitHub.Message != "structured commit\n" || sc.GitHub.Tree != f.treeSHA {
		t.Errorf("github body = %+v", sc.GitHub)
	}
	if sc.GitHub.Author.Name != "Alice" {
		t.Errorf("github author = %+v", sc.GitHub.Author)
	}
	if sc.GitHub.Signature != sc.Signature {
		t.Error("github.Signature != sc.Signature")
	}
}

func TestGitGateway_SignCommitsChain(t *testing.T) {
	f := newGitFixture(t)
	agent, pub := testAgentWithSigner(t)
	gw := NewGitGateway(agent, nil)
	when := time.Unix(1700000456, 0)
	author := &CommitIdentity{Name: "Alice", Email: "alice@example.com", Date: when}

	// First commit (root).
	first, err := gw.SignCommits(testUserID, []CommitToSign{{
		Tree: f.treeSHA, Message: "first\n", Author: author,
	}})
	if err != nil {
		t.Fatalf("sign first: %v", err)
	}
	firstSHA := first[0].SHA

	// Two commits in one call: the same first (determinism check) and a
	// second that chains firstSHA as its parent.
	second, err := gw.SignCommits(testUserID, []CommitToSign{
		{Tree: f.treeSHA, Message: "first\n", Author: author},
		{Tree: f.treeSHA, Parents: []string{firstSHA}, Message: "second\n", Author: author},
	})
	if err != nil {
		t.Fatalf("sign second: %v", err)
	}
	if second[0].SHA != firstSHA {
		t.Errorf("determinism: first SHA was %s then %s", firstSHA, second[0].SHA)
	}
	sc2 := second[1]

	// The parent is embedded in the second commit's canonical payload.
	ref := &object.Commit{
		Author:       object.Signature{Name: "Alice", Email: "alice@example.com", When: when},
		Committer:    object.Signature{Name: "Alice", Email: "alice@example.com", When: when},
		Message:      "second\n",
		TreeHash:     plumbing.NewHash(f.treeSHA),
		ParentHashes: []plumbing.Hash{plumbing.NewHash(firstSHA)},
	}
	refObj := &plumbing.MemoryObject{}
	if err := ref.EncodeWithoutSignature(refObj); err != nil {
		t.Fatalf("ref encode: %v", err)
	}
	rr, _ := refObj.Reader()
	refPayload, _ := io.ReadAll(rr)
	if !bytes.Equal(refPayload, sc2.Payload) {
		t.Errorf("second payload mismatch:\n got %q\nwant %q", sc2.Payload, refPayload)
	}
	if !strings.Contains(string(sc2.Payload), firstSHA) {
		t.Errorf("second payload missing parent sha %s", firstSHA)
	}
	verifySSHSig(t, sc2.Payload, sc2.Signature, pub)

	ref.PGPSignature = sc2.Signature
	fullObj := &plumbing.MemoryObject{}
	if err := ref.Encode(fullObj); err != nil {
		t.Fatalf("full encode: %v", err)
	}
	if got := fullObj.Hash().String(); got != sc2.SHA {
		t.Errorf("second SHA = %s, want %s", got, sc2.SHA)
	}
}

func TestGitGateway_SignCommitsRawPayload(t *testing.T) {
	agent, pub := testAgentWithSigner(t)
	gw := NewGitGateway(agent, nil)
	raw := []byte("tree " + strings.Repeat("a", 40) + "\nauthor X <x@x> 1 +0000\n\nmsg\n")
	out, err := gw.SignCommits(testUserID, []CommitToSign{{RawPayload: raw}})
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	sc := out[0]
	if !bytes.Equal(sc.Payload, raw) {
		t.Errorf("payload altered: got %q, want %q", sc.Payload, raw)
	}
	if sc.SHA != "" {
		t.Errorf("SHA = %q, want empty for raw payload", sc.SHA)
	}
	// RawPayload leaves the GitHub body zero — the caller drove assembly.
	if sc.GitHub.Message != "" || sc.GitHub.Tree != "" || sc.GitHub.Signature != "" {
		t.Errorf("github body should be zero, got %+v", sc.GitHub)
	}
	verifySSHSig(t, raw, sc.Signature, pub)
}

func TestGitGateway_SignCommitsNoKey(t *testing.T) {
	gw := NewGitGateway(emptyAgent(), nil)
	_, err := gw.SignCommits(testUserID, []CommitToSign{{
		Tree:    strings.Repeat("0", 40),
		Message: "x",
		Author:  &CommitIdentity{Name: "A", Email: "a@a", Date: time.Now()},
	}})
	if !errors.Is(err, ErrNoKey) {
		t.Fatalf("err = %v, want ErrNoKey", err)
	}
}

func TestGitGateway_SignCommitsBadInput(t *testing.T) {
	f := newGitFixture(t)
	agent, _ := testAgentWithSigner(t)
	gw := NewGitGateway(agent, nil)
	when := time.Unix(1700000000, 0)
	author := &CommitIdentity{Name: "Alice", Email: "alice@example.com", Date: when}
	cases := []struct {
		name string
		c    CommitToSign
	}{
		{"empty message", CommitToSign{Tree: f.treeSHA, Author: author}},
		{"short tree sha", CommitToSign{Tree: "abc", Message: "x", Author: author}},
		{"no author", CommitToSign{Tree: f.treeSHA, Message: "x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := gw.SignCommits(testUserID, []CommitToSign{tc.c})
			if !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}
