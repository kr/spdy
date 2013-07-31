package spdy

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"strconv"
	"strings"
)

// SPDY 3 prohibits these fields.
// must be in canonicalized form
var (
	badRespHeaderFields = []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Connection",
		"Transfer-Encoding",
	}
	badReqHeaderFields = []string{
		"Connection",
		"Host",
		"Keep-Alive",
		"Proxy-Connection",
		"Transfer-Encoding",
	}
)

func noBodyExpected(requestMethod string) bool {
	return requestMethod == "HEAD"
}

// Determine the expected body length, using RFC 2616 Section 4.4. This
// function is not a method, because ultimately it should be shared by
// ReadResponse and ReadRequest.
func fixLength(isResponse bool, status int, requestMethod string, h http.Header) (int64, error) {

	// Logic based on response type or status
	if noBodyExpected(requestMethod) {
		return 0, nil
	}
	if status/100 == 1 {
		return 0, nil
	}
	switch status {
	case 204, 304:
		return 0, nil
	}

	// Logic based on Content-Length
	cl := strings.TrimSpace(h.Get("Content-Length"))
	if cl != "" {
		n, err := parseContentLength(cl)
		if err != nil {
			return -1, err
		}
		return n, nil
	} else {
		h.Del("Content-Length")
	}

	if !isResponse && requestMethod == "GET" {
		// RFC 2616 doesn't explicitly permit nor forbid an
		// entity-body on a GET request so we permit one if
		// declared, but we default to 0 here (not -1 below)
		// if there's no mention of a body.
		return 0, nil
	}

	// Body-EOF logic based on other methods (like closing, or chunked coding)
	return -1, nil
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
	case b.r == eofReader:
		// Nothing to read. No need to io.Copy from it.
	default:
		// Fully consume the body, which will also lead to us reading
		// the trailer headers after the body, if present.
		_, err = io.Copy(ioutil.Discard, b)
	}
	b.closed = true
	return err
}

type badStringError struct {
	what string
	str  string
}

func (e *badStringError) Error() string { return fmt.Sprintf("%s %q", e.what, e.str) }

// eofReader is an io.Reader that always returns EOF.
var eofReader = strings.NewReader("")

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
