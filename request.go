package spdy

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	framing "github.com/kr/spdy/spdyframing"
)

// ReadRequest reads an HTTP request. The header is taken from h,
// which must include the SPDY-specific fields starting with ':'.
// If r is not nil, the body will be read from r. If t is not nil,
// the trailer will be taken from t after the body is finished.
func ReadRequest(h, t http.Header, r io.Reader) (*http.Request, error) {
	req := new(http.Request)
	req.Header = make(http.Header)
	copyHeader(req.Header, h)
	path := h.Get(":path")
	if path == "" {
		return nil, errors.New("missing path")
	}
	if path[0] != '/' {
		return nil, errors.New("invalid path: " + path)
	}
	req.URL = &url.URL{
		Scheme: h.Get(":scheme"),
		Path:   path,
		Host:   h.Get(":host"),
	}
	req.Close = true
	req.Method = h.Get(":method")
	req.Host = h.Get(":host")
	req.Proto = h.Get(":version")
	var ok bool
	if req.ProtoMajor, req.ProtoMinor, ok = http.ParseHTTPVersion(req.Proto); !ok {
		return nil, errors.New("bad http version: " + req.Proto)
	}
	req.Header.Del("Host")

	cl := strings.TrimSpace(req.Header.Get("Content-Length"))
	if cl != "" {
		n, err := parseContentLength(cl)
		if err != nil {
			return nil, err
		}
		req.ContentLength = n
	} else {
		// Assume GET request has no body by default.
		if req.Method != "GET" {
			req.ContentLength = -1
		}
		req.Header.Del("Content-Length")
	}

	// TODO(kr): content length / limit reader?
	if r == nil {
		r = eofReader
	}
	if t != nil {
		req.Body = &body{r: r, hdr: req, trailer: t}
	} else {
		req.Body = &body{r: r}
	}
	return req, nil
}

// RequestFramingHeader copies r into a header suitable for use in the SPDY
// framing layer. It includes the SPDY-specific ':' fields such as :scheme,
// :method, and :version.
func RequestFramingHeader(r *http.Request) (http.Header, framing.ControlFlags, error) {
	if r.ContentLength > 0 && r.Body == nil {
		return nil, 0, fmt.Errorf("http: Request.ContentLength=%d with nil Body", r.ContentLength)
	}
	h := make(http.Header)
	for k, vv := range r.Header {
		if len(k) > 0 && k[0] != ':' && len(vv) > 0 {
			h[k] = vv
		}
	}
	if _, ok := h["User-Agent"]; !ok {
		h.Set("User-Agent", "github.com/kr/spdy")
	}
	h.Set(":method", r.Method)
	h.Set(":path", r.URL.RequestURI())
	scheme := r.URL.Scheme
	if scheme == "" {
		scheme = "http"
	}
	h.Set(":scheme", scheme)
	host := r.Host
	if host == "" {
		host = r.URL.Host
	}
	h.Set(":host", host)
	proto := r.Proto
	if proto == "" {
		proto = "HTTP/1.1"
	}
	h.Set(":version", proto)
	if r.ContentLength > 0 {
		h.Set("Content-Length", strconv.FormatInt(r.ContentLength, 10))
	} else if r.Method == "POST" && r.Body == nil {
		h.Set("Content-Length", "0")
	}
	for _, s := range badReqHeaderFields {
		delete(h, s)
	}
	var flag framing.ControlFlags
	if r.Body == nil {
		flag = framing.ControlFlagFin
	}
	return h, flag, nil
}
