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
	Conn net.Conn
	s    *framing.Session
	once sync.Once
}

// RoundTrip implements interface http.RoundTripper.
func (c *Conn) RoundTrip(r *http.Request) (*http.Response, error) {
	c.once.Do(func() {
		fr := framing.NewFramer(c.Conn, c.Conn)
		c.s = framing.Start(fr, false, func(s *framing.Stream) {
			// TODO(kr): Make each stream available
			//           to its associated request.
			s.Reset(framing.RefusedStream)
		})
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
	if body != nil {
		go func() {
			// TODO(kr): handle errors
			_, err := io.Copy(st, body)
			if err != nil {
				return
			}
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
