package server

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func readCert(t *testing.T, path string) *x509.Certificate {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		t.Fatalf("%s: no PEM block", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

// The server cert chains to the local CA for every name it was issued for,
// and only those.
func TestIssueLocalTLSChainsToCA(t *testing.T) {
	dir := t.TempDir()
	hosts := []string{"fresh.local", "localhost", "192.168.1.20", "::1"}
	files, err := issueLocalTLS(dir, hosts)
	if err != nil {
		t.Fatalf("issueLocalTLS: %v", err)
	}

	ca := readCert(t, files.CA)
	if !ca.IsCA {
		t.Error("CA cert is not a CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	leaf := readCert(t, files.Cert)
	for _, h := range hosts {
		if _, err := leaf.Verify(x509.VerifyOptions{DNSName: h, Roots: roots}); err != nil {
			t.Errorf("cert doesn't verify for %s: %v", h, err)
		}
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "elsewhere.example", Roots: roots}); err == nil {
		t.Error("cert verifies for a name it wasn't issued for")
	}

	for _, secret := range []string{files.Key, filepath.Join(dir, "ca-key.pem")} {
		info, err := os.Stat(secret)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", filepath.Base(secret), info.Mode().Perm())
		}
	}
}

// Issuing again keeps the CA, so devices that already trust it keep
// trusting the new cert.
func TestIssueLocalTLSReusesCA(t *testing.T) {
	dir := t.TempDir()
	first, err := issueLocalTLS(dir, []string{"localhost"})
	if err != nil {
		t.Fatal(err)
	}
	caBefore, _ := os.ReadFile(first.CA)
	second, err := issueLocalTLS(dir, []string{"localhost", "fresh.local"})
	if err != nil {
		t.Fatal(err)
	}
	caAfter, _ := os.ReadFile(second.CA)
	if !bytes.Equal(caBefore, caAfter) {
		t.Error("issuing again replaced the CA")
	}
	roots := x509.NewCertPool()
	roots.AddCert(readCert(t, second.CA))
	if _, err := readCert(t, second.Cert).Verify(x509.VerifyOptions{DNSName: "fresh.local", Roots: roots}); err != nil {
		t.Errorf("reissued cert doesn't chain to the kept CA: %v", err)
	}
}

func TestSetEnvValuesKeepsTheRestOfTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	os.WriteFile(path, []byte(strings.Join([]string{
		"# Fresh Breath config",
		"FRBR_LISTEN_ADDR=:9009",
		"export FRBR_TLS_CERT=/old/cert.pem",
		"",
	}, "\n")), 0o600)

	if err := setEnvValues(path, [][2]string{{"FRBR_TLS_CERT", "/new/cert.pem"}, {"FRBR_TLS_KEY", "/new/key.pem"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := strings.Join([]string{
		"# Fresh Breath config",
		"FRBR_LISTEN_ADDR=:9009",
		"FRBR_TLS_CERT=/new/cert.pem",
		"FRBR_TLS_KEY=/new/key.pem",
		"",
	}, "\n")
	if string(got) != want {
		t.Errorf("config.env =\n%s\nwant\n%s", got, want)
	}
}

func TestSetEnvValuesCreatesTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "freshbreath", "config.env")
	if err := setEnvValues(path, [][2]string{{"FRBR_TLS_CERT", "/c.pem"}}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "FRBR_TLS_CERT=/c.pem\n" {
		t.Errorf("config.env = %q", got)
	}
}

// POST /api/tls issues the cert, points the env file at it, moves an http
// base URL to https, and the CA can then be downloaded.
func TestTLSSetupWritesConfig(t *testing.T) {
	srv := newTestServer(t)
	cfgDir := t.TempDir()
	srv.config.EnvFile = filepath.Join(cfgDir, "config.env")
	os.WriteFile(srv.config.EnvFile, []byte("FRBR_BASE_URL=http://fresh.local:9009\n"), 0o600)

	req := httptest.NewRequest("POST", "http://fresh.local:9009/api/tls", nil)
	rr := httptest.NewRecorder()
	srv.ServeHTTP(rr, req)
	if rr.Code != 200 {
		t.Fatalf("POST /api/tls: %d %s", rr.Code, rr.Body.String())
	}
	var res struct {
		Hosts    []string `json:"hosts"`
		HTTPSURL string   `json:"https_url"`
		EnvFile  string   `json:"env_file"`
	}
	json.Unmarshal(rr.Body.Bytes(), &res)
	if res.HTTPSURL != "https://fresh.local:9009/control" {
		t.Errorf("https_url = %q", res.HTTPSURL)
	}
	if !slices.Contains(res.Hosts, "fresh.local") || !slices.Contains(res.Hosts, "localhost") {
		t.Errorf("hosts = %v, want the request host and localhost", res.Hosts)
	}

	env, _ := os.ReadFile(srv.config.EnvFile)
	tlsDir := filepath.Join(cfgDir, "tls")
	for _, line := range []string{
		"FRBR_BASE_URL=https://fresh.local:9009",
		"FRBR_TLS_CERT=" + filepath.Join(tlsDir, "cert.pem"),
		"FRBR_TLS_KEY=" + filepath.Join(tlsDir, "key.pem"),
	} {
		if !strings.Contains(string(env), line+"\n") {
			t.Errorf("config.env missing %q:\n%s", line, env)
		}
	}

	ca := testRequest(t, srv, "GET", "/api/tls/ca.pem", nil, nil)
	if ca.Code != 200 || !strings.HasPrefix(ca.Body.String(), "-----BEGIN CERTIFICATE-----") {
		t.Errorf("GET /api/tls/ca.pem = %d %.40q", ca.Code, ca.Body.String())
	}
	if !strings.Contains(ca.Header().Get("Content-Disposition"), "attachment") {
		t.Errorf("CA download Content-Disposition = %q", ca.Header().Get("Content-Disposition"))
	}
}

// A server already serving TLS has certs someone chose; don't replace them.
func TestTLSSetupRefusedWhenTLSIsOn(t *testing.T) {
	srv := newTestServer(t)
	srv.config.EnvFile = filepath.Join(t.TempDir(), "config.env")
	srv.config.TLSCertFile = "/etc/letsencrypt/live/x/fullchain.pem"
	if rr := testRequest(t, srv, "POST", "/api/tls", nil, nil); rr.Code != http.StatusConflict {
		t.Errorf("POST /api/tls = %d %s, want 409", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(srv.config.EnvFile); err == nil {
		t.Error("config.env written anyway")
	}
}
