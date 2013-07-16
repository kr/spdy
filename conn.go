package spdy

import (
	framing "github.com/kr/spdy/spdyframing"
	"io"
	"net"
	"net/http"
	"sync"
)

// Conn represents a SPDY client connection.
// It implements http.RoundTripper for making HTTP requests.
type Conn struct {
	c    net.Conn
	s    *framing.Session
	once sync.Once
}

// NewConn returns a new connection for making requests on c.
func NewConn(c net.Conn) *Conn {
	return &Conn{c: c, s: framing.NewSession(c)}
}

// Will be called in a separate goroutine for each incoming
// server-push stream.
func (c *Conn) handlePush(st *framing.Stream) {
	// TODO(kr):  Associate st with its request and
	//            make it available to the user.
	st.Reset(framing.RefusedStream)
}

// RoundTrip implements interface http.RoundTripper.
func (c *Conn) RoundTrip(r *http.Request) (*http.Response, error) {
	c.once.Do(func() {
		go c.s.Run(false, c.handlePush) // TODO(kr): handle errors
	})
	body := r.Body
	r.Body = nil
	var flag framing.ControlFlags
	if r.ContentLength == 0 {
		flag |= framing.ControlFlagFin
	}
	st, err := c.s.Open(RequestFramingHeader(r), flag)
	if err != nil {
		return nil, err
	}
	if r.ContentLength > 0 && body != nil {
		go func() {
			io.Copy(st, body) // TODO(kr): handle errors
			st.Close()
		}()
	}
	h := st.Header() // waits for SYN_REPLY
	resp, err := ReadResponse(h, nil, st, r)
	if err != nil {
		st.Reset(framing.ProtocolError)
		return nil, err
	}
	resp.Request = r
	return resp, nil
}
