package sshkit

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	"github.com/go-git/go-billy/v5/memfs"
	billyutil "github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"github.com/go-git/go-git/v5/storage/memory"
	"github.com/hiddeco/sshsig"
	"golang.org/x/crypto/ssh"
)

// ErrStaleBase is returned when the remote has moved past the baseCommit the
// caller supplied. The caller should pull and retry.
var ErrStaleBase = fmt.Errorf("remote has moved past baseCommit — pull and retry")

// ErrNothingToCommit is returned when a commit+push produced no changes (clean
// working tree).
var ErrNothingToCommit = fmt.Errorf("nothing to commit")

// ErrInvalidInput wraps every caller-input validation failure (missing url/
// message, non-hex sha, bad path, etc.). The server layer maps it to 400.
var ErrInvalidInput = fmt.Errorf("invalid input")

// GitFile is a single file's path and bytes pulled from (or written to) a repo.
// Content travels as base64 in JSON (encoding/json handles []byte automatically).
type GitFile struct {
	Path    string `json:"path"`
	Size    int64  `json:"size,omitempty"`
	Content []byte `json:"content"`
}

// FileEntry is a tree listing entry: a path and its blob size.
type FileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// GitSnapshot is the result of a Pull: the head commit sha, the branch it came
// from, and either a tree listing (paths omitted on the request) or the
// requested files' contents.
type GitSnapshot struct {
	Commit string      `json:"commit"`
	Branch string      `json:"branch,omitempty"`
	Tree   []FileEntry `json:"tree,omitempty"`  // populated when no paths were requested (discovery)
	Files  []GitFile   `json:"files,omitempty"` // populated when paths were requested (selective)
}

// BranchInfo is a single remote branch ref.
type BranchInfo struct {
	Name   string `json:"name"`
	Commit string `json:"commit"`
}

// GitCommitRequest describes a fused signed-commit-and-push operation.
type GitCommitRequest struct {
	URL         string    `json:"url"`
	Branch      string    `json:"branch,omitempty"`     // "" → remote HEAD
	BaseCommit  string    `json:"baseCommit,omitempty"` // optional optimistic-concurrency check; "" → skip
	Message     string    `json:"message"`
	AuthorName  string    `json:"authorName,omitempty"`
	AuthorEmail string    `json:"authorEmail,omitempty"`
	Writes      []GitFile `json:"writes,omitempty"`
	Deletes     []string  `json:"deletes,omitempty"`
}

// CommitIdentity is an author or committer identity for a commit to sign.
type CommitIdentity struct {
	Name  string    `json:"name"`
	Email string    `json:"email"`
	Date  time.Time `json:"date"`
}

// CommitToSign mirrors GitHub's git/commits request shape. Either supply the
// structured fields (Tree, Parents, Message, Author) or RawPayload (a
// pre-built canonical commit payload, signed as-is).
type CommitToSign struct {
	Tree       string          `json:"tree,omitempty"` // tree sha (required unless RawPayload is set)
	Parents    []string        `json:"parents,omitempty"`
	Message    string          `json:"message,omitempty"`
	Author     *CommitIdentity `json:"author,omitempty"` // Committer defaults to Author when nil
	Committer  *CommitIdentity `json:"committer,omitempty"`
	RawPayload []byte          `json:"payload,omitempty"` // escape hatch: sign as-is, no struct build
}

// SignedCommit is the result of signing one commit. GitHub is the verbatim
// body for POST /repos/{o}/{r}/git/commits (its signature field is the armored
// SSH SIGNATURE block, which GitHub stores in gpgsig). For RawPayload inputs,
// SHA is empty (we don't reconstruct the signed commit bytes) and GitHub is
// the zero value — the caller drove the payload and owns assembly.
type SignedCommit struct {
	SHA       string           `json:"sha,omitempty"`
	Payload   []byte           `json:"payload"`
	Signature string           `json:"signature"`
	GitHub    GitHubCommitBody `json:"github,omitempty"`
}

// GitHubCommitBody is the verbatim body for POST /repos/{o}/{r}/git/commits.
type GitHubCommitBody struct {
	Message   string         `json:"message"`
	Tree      string         `json:"tree"`
	Parents   []string       `json:"parents"`
	Author    CommitIdentity `json:"author"`
	Committer CommitIdentity `json:"committer"`
	Signature string         `json:"signature"`
}

// GitGateway signs git commits and pulls/pushes over SSH (or file/http) using
// the user's agent-managed SSH key. Each call is stateless: a transient
// single-branch clone in RAM, one operation, then forgotten.
type GitGateway struct {
	agent    *AgentManager
	hostKeys HostKeyStore
}

// NewGitGateway constructs a gateway backed by the given agent (for signers)
// and host-key store (TOFU host key verification on the SSH transport).
func NewGitGateway(agent *AgentManager, hostKeys HostKeyStore) *GitGateway {
	return &GitGateway{agent: agent, hostKeys: hostKeys}
}

// sshCommitSigner adapts an ssh.Signer to go-git's object.Signer interface
// (Sign(message io.Reader) ([]byte, error)). It signs the canonical commit
// payload with sshsig under the "git" namespace and SHA-512, returning the
// armored PEM block git stores in gpgsig (with gpg.format=ssh).
type sshCommitSigner struct {
	signer ssh.Signer
}

func (s sshCommitSigner) Sign(message io.Reader) ([]byte, error) {
	sig, err := sshsig.Sign(message, s.signer, sshsig.HashSHA512, "git")
	if err != nil {
		return nil, fmt.Errorf("sshsig sign: %w", err)
	}
	return sshsig.Armor(sig), nil
}

// authFor builds the transport.AuthMethod for a git URL. For non-ssh protocols
// it returns nil (hermetic file:// tests, public http). For ssh it fetches the
// user's signer — short-circuiting with ErrNoKey before any dial — and wraps it
// in a go-git PublicKeys with the DB-backed TOFU host key callback.
func (g *GitGateway) authFor(userID int64, url string) (transport.AuthMethod, error) {
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		return nil, fmt.Errorf("parse git url: %w", err)
	}
	if ep.Protocol != "ssh" {
		return nil, nil
	}
	signer, err := g.agent.GetSigner(userID)
	if err != nil {
		return nil, fmt.Errorf("get signer: %w", err) // wraps ErrNoKey
	}
	user := ep.User
	if user == "" {
		user = "git"
	}
	return &gitssh.PublicKeys{
		User:   user,
		Signer: signer,
		HostKeyCallbackHelper: gitssh.HostKeyCallbackHelper{
			HostKeyCallback: NewTOFUHostKeyCallback(g.hostKeys),
		},
	}, nil
}

// SignCommits is a pure signing oracle: no clone, no network. It builds each
// commit's canonical payload (byte-identical to what GitHub reconstructs from
// the same fields), sshsig-signs it, and returns the signature plus the
// verbatim GitHub POST body. ed25519 sshsig is deterministic, so a caller can
// pre-chain parents across multiple commits in one call (commit[1].Parents =
// [commit[0].SHA]).
func (g *GitGateway) SignCommits(userID int64, commits []CommitToSign) ([]SignedCommit, error) {
	signer, err := g.agent.GetSigner(userID)
	if err != nil {
		return nil, fmt.Errorf("get signer: %w", err) // wraps ErrNoKey
	}
	s := sshCommitSigner{signer: signer}

	out := make([]SignedCommit, 0, len(commits))
	for _, c := range commits {
		sc, err := g.signOne(s, c)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

func (g *GitGateway) signOne(s sshCommitSigner, c CommitToSign) (SignedCommit, error) {
	if len(c.RawPayload) > 0 {
		sigBytes, err := s.Sign(bytes.NewReader(c.RawPayload))
		if err != nil {
			return SignedCommit{}, err
		}
		return SignedCommit{
			Payload:   c.RawPayload,
			Signature: string(sigBytes),
		}, nil
	}

	// Structured commit: validate and build.
	if err := validateHexSHA(c.Tree); err != nil {
		return SignedCommit{}, fmt.Errorf("tree sha: %w", err)
	}
	if c.Message == "" {
		return SignedCommit{}, fmt.Errorf("%w: commit message is required", ErrInvalidInput)
	}
	for _, p := range c.Parents {
		if err := validateHexSHA(p); err != nil {
			return SignedCommit{}, fmt.Errorf("parent sha: %w", err)
		}
	}

	author := CommitIdentity{}
	if c.Author != nil {
		author = *c.Author
	}
	if author.Name == "" || author.Email == "" {
		return SignedCommit{}, fmt.Errorf("%w: author name and email are required", ErrInvalidInput)
	}
	if author.Date.IsZero() {
		author.Date = time.Now()
	}
	committer := author
	if c.Committer != nil {
		committer = *c.Committer
		if committer.Name == "" || committer.Email == "" {
			committer.Name = author.Name
			committer.Email = author.Email
		}
		if committer.Date.IsZero() {
			committer.Date = author.Date
		}
	}

	commit := &object.Commit{
		Author:       identityToSignature(author),
		Committer:    identityToSignature(committer),
		Message:      c.Message,
		TreeHash:     plumbing.NewHash(c.Tree),
		ParentHashes: hashesFromStrings(c.Parents),
	}

	// Canonical payload = EncodeWithoutSignature (struct-encoded, no src →
	// uses the struct fields, byte-identical to GitHub's reconstruction).
	payloadObj := &plumbing.MemoryObject{}
	if err := commit.EncodeWithoutSignature(payloadObj); err != nil {
		return SignedCommit{}, fmt.Errorf("encode payload: %w", err)
	}
	r, err := payloadObj.Reader()
	if err != nil {
		return SignedCommit{}, fmt.Errorf("read payload: %w", err)
	}
	payload, err := io.ReadAll(r)
	if err != nil {
		return SignedCommit{}, fmt.Errorf("read payload: %w", err)
	}

	sigBytes, err := s.Sign(bytes.NewReader(payload))
	if err != nil {
		return SignedCommit{}, err
	}
	commit.PGPSignature = string(sigBytes)

	// SHA = hash of the full encode (with the gpgsig block).
	fullObj := &plumbing.MemoryObject{}
	if err := commit.Encode(fullObj); err != nil {
		return SignedCommit{}, fmt.Errorf("encode signed commit: %w", err)
	}

	return SignedCommit{
		SHA:       fullObj.Hash().String(),
		Payload:   payload,
		Signature: commit.PGPSignature,
		GitHub: GitHubCommitBody{
			Message:   c.Message,
			Tree:      c.Tree,
			Parents:   c.Parents,
			Author:    author,
			Committer: committer,
			Signature: commit.PGPSignature,
		},
	}, nil
}

// Branches lists remote refs via ls-remote (no clone, no packfile). It returns
// the symbolic HEAD's resolved sha plus every refs/heads/* branch.
func (g *GitGateway) Branches(userID int64, url string) (head string, branches []BranchInfo, err error) {
	auth, err := g.authFor(userID, url)
	if err != nil {
		return "", nil, err
	}
	remote := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	})
	refs, err := remote.List(&git.ListOptions{Auth: auth})
	if err != nil {
		return "", nil, mapRemoteErr(err)
	}

	// First pass: collect branch shas so a symbolic HEAD can be resolved.
	branchSHA := make(map[string]string)
	var headRef *plumbing.Reference
	for _, ref := range refs {
		name := ref.Name()
		if name == plumbing.HEAD {
			headRef = ref
			continue
		}
		if strings.HasPrefix(name.String(), "refs/heads/") {
			bn := strings.TrimPrefix(name.String(), "refs/heads/")
			branches = append(branches, BranchInfo{Name: bn, Commit: ref.Hash().String()})
			branchSHA[bn] = ref.Hash().String()
		}
	}

	if headRef != nil {
		switch headRef.Type() {
		case plumbing.HashReference:
			head = headRef.Hash().String()
		case plumbing.SymbolicReference:
			// HEAD → refs/heads/<branch>; resolve via the advertised branch sha.
			target := strings.TrimPrefix(headRef.Target().String(), "refs/heads/")
			head = branchSHA[target]
		}
	}
	return head, branches, nil
}

// Pull is the selective read side. With no paths it returns a tree listing plus
// the head sha; with paths it returns just those files' contents (a directory
// path includes its subtree). Implemented as a bare clone — contents are read
// straight off the commit's tree, no checkout, no memfs.
func (g *GitGateway) Pull(userID int64, url, branch string, paths []string) (*GitSnapshot, error) {
	auth, err := g.authFor(userID, url)
	if err != nil {
		return nil, err
	}
	cloneOpts := &git.CloneOptions{
		URL:          url,
		Auth:         auth,
		SingleBranch: true,
		NoCheckout:   true,
	}
	if branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(branch)
	}
	repo, err := git.Clone(memory.NewStorage(), nil, cloneOpts)
	if err != nil {
		return nil, mapRemoteErr(err)
	}

	headRef, err := repo.Head()
	if err != nil {
		return nil, fmt.Errorf("resolve head: %w", err)
	}
	headCommit, err := repo.CommitObject(headRef.Hash())
	if err != nil {
		return nil, fmt.Errorf("load head commit: %w", err)
	}
	resolvedBranch := branch
	if resolvedBranch == "" {
		// SingleBranch clone with no ReferenceName defaults to HEAD; report the
		// short branch name it resolved to.
		resolvedBranch = strings.TrimPrefix(headRef.Name().String(), "refs/heads/")
		if resolvedBranch == headRef.Name().String() {
			resolvedBranch = ""
		}
	}
	tree, err := headCommit.Tree()
	if err != nil {
		return nil, fmt.Errorf("load tree: %w", err)
	}

	snap := &GitSnapshot{
		Commit: headCommit.Hash.String(),
		Branch: resolvedBranch,
	}
	if len(paths) == 0 {
		if err := tree.Files().ForEach(func(f *object.File) error {
			snap.Tree = append(snap.Tree, FileEntry{Path: f.Name, Size: f.Size})
			return nil
		}); err != nil {
			return nil, fmt.Errorf("walk tree: %w", err)
		}
		return snap, nil
	}

	for _, p := range paths {
		clean, err := cleanGitPath(p)
		if err != nil {
			return nil, err
		}
		files, err := readTreePath(tree, clean)
		if err != nil {
			return nil, err
		}
		snap.Files = append(snap.Files, files...)
	}
	return snap, nil
}

// readTreePath resolves a path within a tree to file contents. A directory
// path yields every file in its subtree.
func readTreePath(tree *object.Tree, p string) ([]GitFile, error) {
	entry, err := tree.FindEntry(p)
	if err != nil {
		return nil, mapPathErr(p, err)
	}
	if entry.Mode == filemode.Dir {
		sub, err := tree.Tree(p)
		if err != nil {
			return nil, mapPathErr(p, err)
		}
		var out []GitFile
		if err := sub.Files().ForEach(func(f *object.File) error {
			content, err := readAll(f)
			if err != nil {
				return err
			}
			out = append(out, GitFile{
				Path:    path.Join(p, f.Name),
				Size:    f.Size,
				Content: content,
			})
			return nil
		}); err != nil {
			return nil, fmt.Errorf("read %s: %w", p, err)
		}
		return out, nil
	}
	f, err := tree.TreeEntryFile(entry)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	content, err := readAll(f)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return []GitFile{{Path: p, Size: f.Size, Content: content}}, nil
}

// CommitPush is the fused signed-commit-and-push. Signing is mandatory (no
// signer → ErrNoKey before any network work). It clones with checkout into a
// memfs worktree, applies writes and deletes, stages all, commits with the
// sshsig signer, and pushes. A stale baseCommit (remote moved) or a non-FF
// push race yields ErrStaleBase; a clean tree yields ErrNothingToCommit.
func (g *GitGateway) CommitPush(userID int64, req GitCommitRequest) (sha string, err error) {
	if req.URL == "" {
		return "", fmt.Errorf("%w: url is required", ErrInvalidInput)
	}
	if req.Message == "" {
		return "", fmt.Errorf("%w: commit message is required", ErrInvalidInput)
	}
	if req.AuthorName == "" || req.AuthorEmail == "" {
		return "", fmt.Errorf("%w: author name and email are required", ErrInvalidInput)
	}

	// Signer first: signing is mandatory, and this short-circuits to ErrNoKey
	// before any clone/dial work.
	signer, err := g.agent.GetSigner(userID)
	if err != nil {
		return "", fmt.Errorf("get signer: %w", err) // wraps ErrNoKey
	}
	auth, err := g.authFor(userID, req.URL)
	if err != nil {
		return "", err
	}

	cloneOpts := &git.CloneOptions{
		URL:          req.URL,
		Auth:         auth,
		SingleBranch: true,
	}
	if req.Branch != "" {
		cloneOpts.ReferenceName = plumbing.NewBranchReferenceName(req.Branch)
	}
	repo, err := git.Clone(memory.NewStorage(), memfs.New(), cloneOpts)
	if err != nil {
		return "", mapRemoteErr(err)
	}

	wt, err := repo.Worktree()
	if err != nil {
		return "", fmt.Errorf("worktree: %w", err)
	}

	headRef, err := repo.Head()
	if err != nil {
		return "", fmt.Errorf("resolve head: %w", err)
	}
	headSHA := headRef.Hash().String()
	if req.BaseCommit != "" && req.BaseCommit != headSHA {
		return "", fmt.Errorf("%w (head is %s)", ErrStaleBase, headSHA)
	}

	fs := wt.Filesystem
	for _, w := range req.Writes {
		clean, err := cleanGitPath(w.Path)
		if err != nil {
			return "", err
		}
		if dir := path.Dir(clean); dir != "." {
			if err := fs.MkdirAll(dir, 0o755); err != nil {
				return "", fmt.Errorf("mkdir %s: %w", dir, err)
			}
		}
		if err := billyutil.WriteFile(fs, clean, w.Content, 0o644); err != nil {
			return "", fmt.Errorf("write %s: %w", clean, err)
		}
	}
	for _, d := range req.Deletes {
		clean, err := cleanGitPath(d)
		if err != nil {
			return "", err
		}
		if err := billyutil.RemoveAll(fs, clean); err != nil {
			// A missing delete target is a no-op, not an error.
			if !errors.Is(err, os.ErrNotExist) && !errors.Is(err, object.ErrFileNotFound) {
				return "", fmt.Errorf("delete %s: %w", clean, err)
			}
		}
	}

	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return "", fmt.Errorf("stage: %w", err)
	}

	when := time.Now()
	commitSHA, err := wt.Commit(req.Message, &git.CommitOptions{
		Author:    &object.Signature{Name: req.AuthorName, Email: req.AuthorEmail, When: when},
		Committer: &object.Signature{Name: req.AuthorName, Email: req.AuthorEmail, When: when},
		Signer:    sshCommitSigner{signer: signer},
	})
	if err != nil {
		if errors.Is(err, git.ErrEmptyCommit) {
			return "", ErrNothingToCommit
		}
		return "", fmt.Errorf("commit: %w", err)
	}

	if err := repo.Push(&git.PushOptions{
		RemoteName: "origin",
		Auth:       auth,
	}); err != nil {
		if isNonFF(err) {
			return "", fmt.Errorf("%w (push rejected)", ErrStaleBase)
		}
		return "", mapRemoteErr(err)
	}
	return commitSHA.String(), nil
}

// --- helpers ---

func identityToSignature(id CommitIdentity) object.Signature {
	return object.Signature{Name: id.Name, Email: id.Email, When: id.Date}
}

func hashesFromStrings(ss []string) []plumbing.Hash {
	if len(ss) == 0 {
		return nil
	}
	out := make([]plumbing.Hash, len(ss))
	for i, s := range ss {
		out[i] = plumbing.NewHash(s)
	}
	return out
}

// validateHexSHA accepts a 40-char lowercase-or-uppercase hex SHA-1.
func validateHexSHA(s string) error {
	if len(s) != 40 {
		return fmt.Errorf("%w: expected 40-char sha, got %d", ErrInvalidInput, len(s))
	}
	if _, err := hex.DecodeString(s); err != nil {
		return fmt.Errorf("%w: non-hex sha: %v", ErrInvalidInput, err)
	}
	return nil
}

// cleanGitPath normalizes a caller-supplied git path: forward slashes, no
// leading slash, no '..' escape, no absolute paths.
func cleanGitPath(p string) (string, error) {
	p = strings.TrimPrefix(p, "/")
	p = path.Clean(p)
	if p == "." || p == "/" || p == "" {
		return "", fmt.Errorf("%w: empty path", ErrInvalidInput)
	}
	if p == ".." || strings.HasPrefix(p, "../") {
		return "", fmt.Errorf("%w: invalid path %q: '..' segments not allowed", ErrInvalidInput, p)
	}
	if path.IsAbs(p) {
		return "", fmt.Errorf("%w: invalid path %q: absolute paths not allowed", ErrInvalidInput, p)
	}
	return p, nil
}

func readAll(f *object.File) ([]byte, error) {
	r, err := f.Reader()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return io.ReadAll(r)
}

// mapPathErr wraps a tree-lookup miss as an os.ErrNotExist-chainable error so
// the server layer can map it to 404.
func mapPathErr(p string, err error) error {
	if errors.Is(err, object.ErrEntryNotFound) || errors.Is(err, object.ErrFileNotFound) || errors.Is(err, object.ErrDirectoryNotFound) {
		return fmt.Errorf("path %q: %w", p, os.ErrNotExist)
	}
	return fmt.Errorf("path %q: %w", p, err)
}

// mapRemoteErr surfaces clone/ls/transport failures as-is for the server layer
// to map to 502, while translating "reference not found" to a 404-mappable
// os.ErrNotExist.
func mapRemoteErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "reference not found") ||
		(strings.Contains(msg, "remote ref") && strings.Contains(msg, "not found")) {
		return fmt.Errorf("clone: %w", os.ErrNotExist)
	}
	return fmt.Errorf("git remote: %w", err)
}

// isNonFF detects a non-fast-forward push rejection from go-git. Kept specific
// so a protected-branch or other non-stale rejection surfaces as a 502 rather
// than masquerading as a retryable stale base.
func isNonFF(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "non fast forward") ||
		strings.Contains(msg, "fetch first")
}
