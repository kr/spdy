package spdy

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
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
func RequestFramingHeader(r *http.Request) http.Header {
	h := make(http.Header)
	copyHeader(h, r.Header)
	h.Set(":scheme", r.URL.Scheme)
	h.Set(":host", r.URL.Host)
	h.Set(":method", r.Method)
	h.Set(":path", r.URL.Path)
	h.Set(":version", r.Proto)
	return h
}
