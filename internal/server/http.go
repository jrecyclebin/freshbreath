package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"poggers.institute/freshbreath/internal/db"
)

// errOutsideService is a proxied path that resolves outside the
// service's base URL.
var errOutsideService = errors.New("path is outside this service")

// serviceDoProxy forwards a proxied request upstream, applying the resolved
// outbound credential. The caller's own Authorization only survives on a
// Verbatim verdict — anything else at the door was the gate credential,
// which is never valid upstream.
func (s *Server) serviceDoProxy(svc *db.Service, r *http.Request, cred outboundCred) (*http.Response, error) {
	target, err := url.Parse(svc.URL)
	if err != nil {
		return nil, err
	}

	prefix := "/service/" + r.PathValue("id") + "/"
	remaining := strings.TrimPrefix(r.URL.Path, prefix)
	if remaining != "" && remaining != "/" {
		base := strings.TrimSuffix(target.Path, "/")
		target = target.JoinPath(remaining)
		if !strings.HasPrefix(target.Path, "/") {
			target.Path = "/" + target.Path // JoinPath on a URL with no path
		}
		// JoinPath resolves "..", and an encoded %2e%2e or %2f arrives here
		// decoded — so a caller could climb out of the service's base path
		// with its credential attached. The result must stay under it.
		if target.Path != base && !strings.HasPrefix(target.Path, base+"/") {
			return nil, errOutsideService
		}
	}

	if r.URL.RawQuery != "" {
		target.RawQuery = r.URL.RawQuery
	}

	proxyReq, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), r.Body)
	if err != nil {
		return nil, err
	}

	// Forward headers, stripping hop-by-hop and internal ones. The gate
	// credential (Authorization or X-API-Key) is consumed at the door.
	for k, v := range r.Header {
		kl := strings.ToLower(k)
		// Cookie is this server's (the gate pass among them), never the
		// upstream's.
		if kl == "host" || kl == "connection" || kl == "x-app-nonce" || kl == "content-length" || kl == "origin" || kl == "access-control-request-method" || kl == "access-control-request-headers" || kl == "user-agent" || kl == "x-api-key" || kl == "cookie" {
			continue
		}
		if kl == "authorization" && !cred.Verbatim {
			continue
		}
		proxyReq.Header[k] = v
	}

	if cred.Token != "" {
		if cred.Header != "" {
			proxyReq.Header.Set(cred.Header, cred.Token)
		} else {
			proxyReq.Header.Set("Authorization", "Bearer "+cred.Token)
		}
	}

	return s.httpClient.Do(proxyReq)
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		kl := strings.ToLower(k)
		// An upstream must not set cookies on this server's origin, where
		// they would sit beside the gate pass and refresh cookies.
		if kl == "content-length" || kl == "transfer-encoding" || kl == "connection" || kl == "set-cookie" {
			continue
		}
		// Skip CORS headers — our ServeHTTP middleware already sets these.
		// Upstream values would duplicate and cause browser rejections
		// ("header contains multiple values").
		if strings.HasPrefix(kl, "access-control-") {
			continue
		}
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

type flushWriter struct {
	w http.ResponseWriter
}

func (f *flushWriter) Write(p []byte) (int, error) {
	n, err := f.w.Write(p)
	if fl, ok := f.w.(http.Flusher); ok {
		fl.Flush()
	}
	return n, err
}
