package main

import (
	"net/http"
	"net/url"
	"strings"
)

func (s *Server) serviceDoProxy(svc *Service, r *http.Request) (*http.Response, error) {
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

	// Forward all headers from the browser (including Authorization)
	// but strip hop-by-hop and internal headers.
	hasAuth := false
	apiKey := svc.Descriptor.APIKey
	for k, v := range r.Header {
		kl := strings.ToLower(k)
		if kl == "host" || kl == "connection" || kl == "x-app-nonce" || kl == "content-length" || kl == "origin" || kl == "access-control-request-method" || kl == "access-control-request-headers" || kl == "user-agent" {
			continue
		}
		if kl == "authorization" {
			hasAuth = true
		}
		if kl == "x-api-key" {
			apiKey = v[0]
			continue // consumed — will be re-emitted as the proper auth header below
		}
		proxyReq.Header[k] = v
	}

	// Inject API key if no Authorization header was provided.
	// x-api-key from the client takes precedence over the descriptor key.
	if !hasAuth && svc.Descriptor.Auth == "key" && apiKey != "" {
		if svc.Descriptor.Header != "" {
			proxyReq.Header.Set(svc.Descriptor.Header, apiKey)
		} else {
			proxyReq.Header.Set("Authorization", "Bearer "+apiKey)
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
