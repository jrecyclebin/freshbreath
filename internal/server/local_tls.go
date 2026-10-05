package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// ── Local TLS ───────────────────────────────────────────────────────
//
// mkcert, built in: a small CA of this install's own and a server cert it
// signs. Trusting the CA once (browser, OS, Claude Desktop) makes the cert
// fully valid, where a bare self-signed cert would be refused by MCP
// clients outright. Files live in a tls/ folder beside the env file the
// server boots from, and the env file is pointed at them; a restart turns
// TLS on.

type localTLSFiles struct {
	CA, Cert, Key string
}

const (
	localCAValidity   = 10 * 365 * 24 * time.Hour
	localCertValidity = 825 * 24 * time.Hour // what mkcert issues
)

// issueLocalTLS writes a server cert for hosts into dir, signed by the CA
// already there, or by a new one when there isn't. Keeping the CA means
// devices that trust it keep trusting every cert issued after.
func issueLocalTLS(dir string, hosts []string) (*localTLSFiles, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	files := &localTLSFiles{
		CA:   filepath.Join(dir, "ca.pem"),
		Cert: filepath.Join(dir, "cert.pem"),
		Key:  filepath.Join(dir, "key.pem"),
	}
	caKeyPath := filepath.Join(dir, "ca-key.pem")

	ca, caKey, err := loadLocalCA(files.CA, caKeyPath)
	if errors.Is(err, os.ErrNotExist) {
		ca, caKey, err = newLocalCA(files.CA, caKeyPath)
	}
	if err != nil {
		return nil, err
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: randomSerial(),
		Subject:      pkix.Name{Organization: []string{"Fresh Breath"}, CommonName: hosts[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(localCertValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	if err := writePEM(files.Key, "PRIVATE KEY", mustPKCS8(key), 0o600); err != nil {
		return nil, err
	}
	if err := writePEM(files.Cert, "CERTIFICATE", der, 0o644); err != nil {
		return nil, err
	}
	return files, nil
}

func newLocalCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	host, _ := os.Hostname()
	tmpl := &x509.Certificate{
		SerialNumber:          randomSerial(),
		Subject:               pkix.Name{Organization: []string{"Fresh Breath"}, CommonName: "Fresh Breath local CA (" + host + ")"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(localCAValidity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	if err := writePEM(keyPath, "PRIVATE KEY", mustPKCS8(key), 0o600); err != nil {
		return nil, nil, err
	}
	if err := writePEM(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(der)
	return cert, key, err
}

func loadLocalCA(certPath, keyPath string) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certDER, err := readPEM(certPath)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := readPEM(keyPath)
	if err != nil {
		return nil, nil, err
	}
	cert, err := x509.ParseCertificate(certDER)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", certPath, err)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(keyDER)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", keyPath, err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("%s: not an ECDSA key", keyPath)
	}
	return cert, key, nil
}

func readPEM(path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, fmt.Errorf("%s: no PEM block", path)
	}
	return block.Bytes, nil
}

func writePEM(path, kind string, der []byte, perm os.FileMode) error {
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: der}), perm); err != nil {
		return err
	}
	return os.Chmod(path, perm) // WriteFile keeps an existing file's mode
}

func mustPKCS8(key *ecdsa.PrivateKey) []byte {
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		panic(err) // only fails for key types it doesn't know
	}
	return der
}

func randomSerial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		panic(err)
	}
	return n
}

// ── The env file ────────────────────────────────────────────────────

// setEnvValues sets KEY=value lines in an env file, replacing a key's
// existing line (an `export` one included) in place and appending the
// rest. Comments and other keys are left as they were.
func setEnvValues(path string, pairs [][2]string) error {
	raw, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var lines []string
	if text := strings.TrimSuffix(string(raw), "\n"); text != "" {
		lines = strings.Split(text, "\n")
	}
	for _, kv := range pairs {
		line := kv[0] + "=" + kv[1]
		if i := slices.IndexFunc(lines, func(l string) bool { return envKey(l) == kv[0] }); i >= 0 {
			lines[i] = line
		} else {
			lines = append(lines, line)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600)
}

// envValue reads one key from an env file; "" when it isn't there.
func envValue(path, key string) string {
	raw, _ := os.ReadFile(path)
	for _, l := range strings.Split(string(raw), "\n") {
		if envKey(l) == key {
			_, v, _ := strings.Cut(l, "=")
			return strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return ""
}

func envKey(line string) string {
	line = strings.TrimPrefix(strings.TrimSpace(line), "export ")
	k, _, ok := strings.Cut(line, "=")
	if !ok || strings.HasPrefix(k, "#") {
		return ""
	}
	return strings.TrimSpace(k)
}

// ── Setting it up ───────────────────────────────────────────────────

type localTLSResult struct {
	Hosts    []string `json:"hosts"`
	HTTPSURL string   `json:"https_url"`
	EnvFile  string   `json:"env_file"`
	OS       string   `json:"os"`
}

// coreSetupLocalTLS issues a cert for the names this server answers to (the
// one the caller used first) and points the env file at it.
func (s *Server) coreSetupLocalTLS(actor *db.User, requestHost string) (*localTLSResult, error) {
	if err := s.gate(actor, rolesSuperuser); err != nil {
		return nil, err
	}
	if s.config.TLSCertFile != "" {
		return nil, cerr(http.StatusConflict, "this server already serves TLS with %s", s.config.TLSCertFile)
	}
	if s.config.EnvFile == "" {
		return nil, cerr(http.StatusInternalServerError, "no config file to record the certificate in")
	}

	hosts := localTLSHosts(requestHost)
	files, err := issueLocalTLS(filepath.Join(filepath.Dir(s.config.EnvFile), "tls"), hosts)
	if err != nil {
		return nil, cerr(http.StatusInternalServerError, "couldn't write the certificate: %v", err)
	}
	pairs := [][2]string{{"FRBR_TLS_CERT", files.Cert}, {"FRBR_TLS_KEY", files.Key}}
	if base := envValue(s.config.EnvFile, "FRBR_BASE_URL"); strings.HasPrefix(base, "http://") {
		pairs = append(pairs, [2]string{"FRBR_BASE_URL", "https://" + strings.TrimPrefix(base, "http://")})
	}
	if err := setEnvValues(s.config.EnvFile, pairs); err != nil {
		return nil, cerr(http.StatusInternalServerError, "couldn't update %s: %v", s.config.EnvFile, err)
	}
	s.audit(actor, "issued local TLS certificate", strings.Join(hosts, ", "))
	return &localTLSResult{
		Hosts:    hosts,
		HTTPSURL: "https://" + requestHost + "/control",
		EnvFile:  s.config.EnvFile,
		OS:       runtime.GOOS,
	}, nil
}

// localTLSHosts: the name the caller reached us by, this machine's name,
// and loopback — so the cert holds from here, from the LAN, and locally.
func localTLSHosts(requestHost string) []string {
	host := requestHost
	if h, _, err := net.SplitHostPort(requestHost); err == nil {
		host = h
	}
	machine, _ := os.Hostname()
	var hosts []string
	for _, h := range []string{strings.Trim(host, "[]"), machine, "localhost", "127.0.0.1", "::1"} {
		if h != "" && !slices.Contains(hosts, h) {
			hosts = append(hosts, h)
		}
	}
	return hosts
}

func (s *Server) handleLocalTLS(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	res, err := s.coreSetupLocalTLS(userFromContext(r.Context()), r.Host)
	if err != nil {
		writeErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(res)
}

// handleLocalCA downloads the CA certificate, for installing as trusted.
func (s *Server) handleLocalCA(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.config.EnvFile == "" {
		http.NotFound(w, r)
		return
	}
	pemBytes, err := os.ReadFile(filepath.Join(filepath.Dir(s.config.EnvFile), "tls", "ca.pem"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="freshbreath-ca.pem"`)
	w.Write(pemBytes)
}
