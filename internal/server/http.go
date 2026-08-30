package server

import (
	"net/http"
	"net/url"
	"strings"

	"poggers.institute/freshbreath/internal/db"
)

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
		target = target.JoinPath(remaining)
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
		if kl == "host" || kl == "connection" || kl == "x-app-nonce" || kl == "content-length" || kl == "origin" || kl == "access-control-request-method" || kl == "access-control-request-headers" || kl == "user-agent" || kl == "x-api-key" {
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
		if kl == "content-length" || kl == "transfer-encoding" || kl == "connection" {
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
