package spdy

import (
	"crypto/tls"
	framing "github.com/kr/spdy/spdyframing"
	"log"
	"net/http"
	"strconv"
)

// ListenAndServeTLS is like http.ListenAndServeTLS,
// but serves both HTTP and SPDY.
func ListenAndServeTLS(addr, certFile, keyFile string, h http.Handler) error {
	s := &http.Server{
		Addr:    addr,
		Handler: h,
		TLSConfig: &tls.Config{
			NextProtos: []string{"spdy/3"},
		},
		TLSNextProto: map[string]func(*http.Server, *tls.Conn, http.Handler){
			"spdy/3": ServeConn,
		},
	}
	return s.ListenAndServeTLS(certFile, keyFile)
}

// ServeConn is for http.Server.TLSNextProto. It serves SPDY
// requests on c. If h is nil, http.DefaultHandler is used.
// Most people don't need this; they should use ListenAndServeTLS
// instead.
func ServeConn(s *http.Server, c *tls.Conn, h http.Handler) {
	f := func(st *framing.Stream) {
		c := &request{
			remoteAddr: c.RemoteAddr().String(),
			handler:    h,
			stream:     st,
		}
		c.serve()
	}
	err := framing.NewSession(c, true).Run(f)
	if err != nil {
		log.Println("spdy:", err)
	}
}

type request struct {
	remoteAddr string
	handler    http.Handler
	stream     *framing.Stream
}

// The server side of an http-over-spdy request.
func (c *request) serve() {
	// TODO(kr): recover
	// TODO(kr): buffered reader and writer
	w, err := c.readRequest()
	if err != nil {
		log.Println("spdy: read request failed:", err)
		c.stream.Reply(http.Header{":status": {"400"}}, framing.ControlFlagFin)
		c.stream.Reset(framing.RefusedStream)
		return
	}

	handler := c.handler
	if handler == nil {
		handler = http.DefaultServeMux
	}
	handler.ServeHTTP(w, w.req)
	w.finishRequest()
}

func (c *request) readRequest() (w *response, err error) {
	req, err := ReadRequest(
		c.stream.Header(),
		nil,
		c.stream, // TODO(kr): buffer
	)
	if err != nil {
		return nil, err
	}
	req.RemoteAddr = c.remoteAddr
	w = new(response)
	w.header = make(http.Header)
	w.stream = c.stream
	w.req = req
	return w, nil
}

type response struct {
	stream      *framing.Stream
	req         *http.Request
	header      http.Header
	wroteHeader bool
	finished    bool
}

func (w *response) Write(p []byte) (int, error) {
	if !w.wroteHeader {
		w.WriteHeader(http.StatusOK)
	}
	// TODO(kr): sniff
	return w.stream.Write(p)
}

func (w *response) WriteHeader(code int) {
	// There can be body bytes after the header, so don't set
	// FLAG_FIN. Worst case, we'll send an empty-payload data
	// frame later.
	w.writeHeader(code, false)
}

func (w *response) writeHeader(code int, fin bool) {
	if w.wroteHeader {
		log.Print("spdy: multiple response.WriteHeader calls")
		return
	}
	w.wroteHeader = true
	// TODO(kr): enforce correct Content-Length
	// TODO(kr): set FLAG_FIN if Content-Length is 0
	if conn := w.header.Get("Connection"); conn != "" && conn != "close" {
		log.Printf("spdy: invalid Connection set")
	}
	w.header.Del("Connection")
	// TODO(kr): delete other spdy-prohibited header fields

	if code == http.StatusNotModified {
		// Must not have body.
		// TODO(kr): enforce this
	} else {
		// TODO(kr): sniff
		if ctyp := w.header.Get("Content-Type"); ctyp == "" {
			w.header.Set("Content-Type", "text/plain")
		}
	}

	// TODO(kr): set Date

	h := make(http.Header)
	copyHeader(h, w.header)
	h.Set(":status", strconv.Itoa(code))
	h.Set(":version", "HTTP/1.1")
	var flag framing.ControlFlags
	if fin {
		flag |= framing.ControlFlagFin
	}
	err := w.stream.Reply(h, flag)
	if err != nil {
		log.Println("spdy:", err)
		w.stream.Reset(framing.InternalError)
	}
}

func (w *response) Header() http.Header {
	return w.header
}

func (w *response) finishRequest() {
	if !w.wroteHeader {
		// If the user never wrote the header, they also wrote no
		// body bytes, so we can set FLAG_FIN immediately and
		// we're done.
		w.writeHeader(http.StatusOK, true)
		return
	}
	// TODO(kr): sniff
	err := w.stream.Close()
	if err != nil {
		log.Println("spdy:", err)
	}
}

// TODO(kr): func (w *response) Push() http.ResponseWriter

func copyHeader(dst, src http.Header) {
	for k, vv := range src {
		if len(k) > 0 && k[0] != ':' {
			for _, v := range vv {
				dst.Add(k, v)
			}
		}
	}
}
