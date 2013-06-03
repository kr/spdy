package spdy

import (
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"strconv"
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
		r = eofReadCloser
	}
	if t != nil {
		req.Body = &body{r: r, hdr: req, trailer: t}
	} else {
		req.Body = &body{r: r}
	}
	return req, nil
}

// body turns a Reader into a ReadCloser.
// Close ensures that the body has been fully read
// and then copies the trailer if necessary.
// In spdy, every request is "closing"; there's
// no such thing as a keepalive stream.
type body struct {
	r      io.Reader
	closed bool

	// non-nil (Response or Request) value means copy trailer
	hdr interface{}

	// contains trailer values to copy.
	// will be filled in by the frame reader.
	// should be considered incomplete until EOF.
	trailer http.Header

	res *response // response writer for server requests, else nil
}

func (b *body) Read(p []byte) (n int, err error) {
	if b.closed {
		return 0, http.ErrBodyReadAfterClose
	}
	n, err = b.r.Read(p)
	if err == io.EOF && b.trailer != nil {
		b.copyTrailer()
		b.hdr = nil
	}
	return n, err
}

func (b *body) copyTrailer() {
	if b.trailer == nil {
		return
	}
	switch rr := b.hdr.(type) {
	case *http.Request:
		rr.Trailer = make(http.Header)
		copyHeader(rr.Trailer, b.trailer)
	case *http.Response:
		rr.Trailer = make(http.Header)
		copyHeader(rr.Trailer, b.trailer)
	}
	b.trailer = nil
}

func (b *body) Close() error {
	if b.closed {
		return nil
	}
	var err error
	switch {
	case b.hdr == nil:
		// no trailer. no point in reading to EOF.
	case false:
		// TODO(kr): request body limit as in net/http
	case b.r == eofReadCloser:
		// Nothing to read. No need to io.Copy from it.
	default:
		// Fully consume the body, which will also lead to us reading
		// the trailer headers after the body, if present.
		_, err = io.Copy(ioutil.Discard, b)
	}
	b.closed = true
	return err
}

// parseContentLength trims whitespace from cl and returns -1 if no value
// is set, or the value if it's >= 0.
func parseContentLength(cl string) (int64, error) {
	cl = strings.TrimSpace(cl)
	if cl == "" {
		return -1, nil
	}
	n, err := strconv.ParseInt(cl, 10, 64)
	if err != nil || n < 0 {
		return 0, &badStringError{"bad Content-Length", cl}
	}
	return n, nil

}

type badStringError struct {
	what string
	str  string
}

func (e *badStringError) Error() string { return fmt.Sprintf("%s %q", e.what, e.str) }

// eofReadCloser is a non-nil io.ReadCloser that always returns EOF.
var eofReadCloser = ioutil.NopCloser(strings.NewReader(""))
