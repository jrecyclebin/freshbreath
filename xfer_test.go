package main

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	"poggers.institute/freshbreath/internal/db"
)

// tokenOf strips the public-base prefix from a minted transfer URL.
func tokenOf(t *testing.T, url string) string {
	t.Helper()
	prefix := "http://localhost:9009/api/xfer/"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("transfer URL %q missing expected prefix", url)
	}
	return strings.TrimPrefix(url, prefix)
}

func TestTransferSingleUse(t *testing.T) {
	srv := newTestServer(t)
	actor := &db.User{ID: 1, Role: "Admin"}

	url := srv.newTransfer("download", "nonce123", "", actor)
	tok := tokenOf(t, url)

	if e := srv.takeTransfer(tok); e == nil || e.Nonce != "nonce123" {
		t.Fatalf("first redemption should return the entry, got %+v", e)
	}
	if e := srv.takeTransfer(tok); e != nil {
		t.Errorf("second redemption should be nil (single-use), got %+v", e)
	}
}

func TestTransferExpiry(t *testing.T) {
	srv := newTestServer(t)
	actor := &db.User{ID: 1, Role: "Admin"}

	tok := tokenOf(t, srv.newTransfer("download", "n", "", actor))
	srv.xfers[tok].ExpiresAt = time.Now().Add(-time.Minute)

	if e := srv.takeTransfer(tok); e != nil {
		t.Errorf("expired token should not redeem, got %+v", e)
	}
}

func TestTransferUnknownToken(t *testing.T) {
	srv := newTestServer(t)
	if e := srv.takeTransfer("nope"); e != nil {
		t.Errorf("unknown token should be nil, got %+v", e)
	}
}

// TestTransferRoundTrip exercises the full upload-then-download cycle through
// the HTTP redemption endpoint, the way an LLM-driven client would.
func TestTransferRoundTrip(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Admin"}

	nonce, err := srv.coreCreateApp(admin, "My App", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Upload a single index.html via a POST transfer token.
	upTok := tokenOf(t, srv.newTransfer("upload", nonce, "index.html", admin))
	body := []byte("<h1>hello</h1>")
	rr := testRequest(t, srv, "POST", "/api/xfer/"+upTok, bytes.NewReader(body), nil)
	if rr.Code != 200 {
		t.Fatalf("upload status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}

	// Download the app's files via a GET transfer token; expect a zip holding
	// the file we just uploaded.
	dlTok := tokenOf(t, srv.newTransfer("download", nonce, "", admin))
	rr = testRequest(t, srv, "GET", "/api/xfer/"+dlTok, nil, nil)
	if rr.Code != 200 {
		t.Fatalf("download status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/zip" {
		t.Errorf("download Content-Type = %q, want application/zip", ct)
	}

	zr, err := zip.NewReader(bytes.NewReader(rr.Body.Bytes()), int64(rr.Body.Len()))
	if err != nil {
		t.Fatalf("open downloaded zip: %v", err)
	}
	var got string
	for _, f := range zr.File {
		if f.Name == "index.html" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			got = string(b)
		}
	}
	if got != string(body) {
		t.Errorf("round-tripped index.html = %q, want %q", got, string(body))
	}

	// The download token is single-use: a second GET is rejected.
	rr = testRequest(t, srv, "GET", "/api/xfer/"+dlTok, nil, nil)
	if rr.Code != 404 {
		t.Errorf("reused token status = %d, want 404", rr.Code)
	}
}

func TestListAppFiles(t *testing.T) {
	srv := newTestServer(t)
	srv.config.DataDir = t.TempDir()
	admin := &db.User{ID: 1, Role: "Admin"}

	nonce, err := srv.coreCreateApp(admin, "My App", "", "", nil)
	if err != nil {
		t.Fatalf("create app: %v", err)
	}

	// Nothing published yet — an empty list, not an error.
	files, err := srv.coreListAppWeb(admin, nonce, "")
	if err != nil {
		t.Fatalf("list empty: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("fresh app should list no files, got %v", files)
	}

	body := []byte("<h1>hi</h1>")
	if _, err := srv.coreUploadAppWeb(admin, nonce, body, "index.html"); err != nil {
		t.Fatalf("upload: %v", err)
	}

	files, err = srv.coreListAppWeb(admin, nonce, "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(files) != 1 || files[0].Path != "index.html" || files[0].Size != int64(len(body)) {
		t.Errorf("listing = %+v, want one index.html of %d bytes", files, len(body))
	}
}

func TestTransferMethodMismatch(t *testing.T) {
	srv := newTestServer(t)
	admin := &db.User{ID: 1, Role: "Admin"}

	// POSTing to a download token is the wrong verb for the action.
	tok := tokenOf(t, srv.newTransfer("download", "n", "", admin))
	rr := testRequest(t, srv, "POST", "/api/xfer/"+tok, bytes.NewReader([]byte("x")), nil)
	if rr.Code != 405 {
		t.Errorf("method mismatch status = %d, want 405", rr.Code)
	}
}
