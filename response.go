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
func ReadResponse(h, t http.Header, r io.Reader) (*http.Response, error) {
	var err error
	resp := new(http.Response)
	resp.Header = make(http.Header)
	copyHeader(resp.Header, h)
	resp.Status = h.Get(":status")
	f := strings.SplitN(resp.Status, " ", 2)
	resp.StatusCode, err = strconv.Atoi(f[0])
	if err != nil {
		return nil, &badStringError{"malformed HTTP status code", resp.Status}
	}
	if r == nil {
		r = eofReadCloser
	}
	if t != nil {
		resp.Body = &body{r: r, hdr: resp, trailer: t}
	} else {
		resp.Body = &body{r: r}
	}
	return resp, nil
}
