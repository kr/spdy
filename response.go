package spdy

import (
	"io"
	"net/http"
	"strconv"
	"strings"
)

// ReadResponse reads an HTTP response. The header is taken from h,
// which must include the SPDY-specific fields starting with ':'.
// If r is not nil, the body will be read from r. If t is not nil,
// the trailer will be taken from t after the body is finished.
func ReadResponse(h, t http.Header, r io.Reader, req *http.Request) (*http.Response, error) {
	for _, s := range badRespHeaderFields {
		if _, ok := h[s]; ok {
			return nil, &badStringError{"invalid header field", s}
		}
	}

	var err error
	resp := new(http.Response)
	resp.Request = req
	resp.Close = true
	resp.Header = make(http.Header)
	copyHeader(resp.Header, h)
	f := strings.SplitN(h.Get(":status"), " ", 2)
	var s string
	if len(f) > 1 {
		s = f[1]
	}
	resp.Status = f[0] + " " + s
	resp.StatusCode, err = strconv.Atoi(f[0])
	if err != nil {
		return nil, &badStringError{"malformed HTTP status code", f[0]}
	}
	resp.Proto = h.Get(":version")
	var ok bool
	resp.ProtoMajor, resp.ProtoMinor, ok = http.ParseHTTPVersion(resp.Proto)
	if !ok {
		return nil, &badStringError{"malformed HTTP version", resp.Proto}
	}

	realLength, err := fixLength(true, resp.StatusCode, req.Method, resp.Header)
	if err != nil {
		return nil, err
	}
	if req.Method == "HEAD" {
		if n, err := parseContentLength(h.Get("Content-Length")); err != nil {
			return nil, err
		} else {
			resp.ContentLength = n
		}
	} else {
		resp.ContentLength = realLength
	}

	switch {
	case realLength == 0:
		r = eofReader
	case realLength > 0:
		if r == nil {
			// TODO(kr): return error
		}
		r = io.LimitReader(r, realLength)
	}
	if r == nil {
		r = eofReader
	}
	body := &body{r: r}
	resp.Body = body
	if t != nil {
		body.hdr = resp
		body.trailer = t
	}
	return resp, nil
}
