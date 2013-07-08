package spdy

import (
	"crypto/tls"
	framing "github.com/kr/spdy/spdyframing"
	"log"
	"net"
	"net/http"
	"strconv"
)

type Server struct {
	http.Server
}

// ListenAndServeTLS is like http.ListenAndServeTLS,
// but serves both HTTP and SPDY.
func ListenAndServeTLS(addr, certFile, keyFile string, h http.Handler) error {
	s := &Server{Server: http.Server{Addr: addr, Handler: h}}
	return s.ListenAndServeTLS(certFile, keyFile)
}

// ListenAndServeTLS is like http.Server.ListenAndServeTLS,
// but serves both HTTP and SPDY.
func (s *Server) ListenAndServeTLS(certFile, keyFile string) error {
	s1 := *s
	s1.TLSConfig = new(tls.Config)
	if s.TLSConfig != nil {
		*s1.TLSConfig = *s.TLSConfig
	}
	if s1.TLSConfig.NextProtos == nil {
		s1.TLSConfig.NextProtos = []string{"spdy/3", "http/1.1"}
	}
	if s1.TLSNextProto == nil {
		s1.TLSNextProto = make(map[string]func(*http.Server, *tls.Conn, http.Handler))
	}
	if _, ok := s1.TLSNextProto["spdy/3"]; !ok {
		s1.TLSNextProto["spdy/3"] = s.serveConn
	}
	return s1.Server.ListenAndServeTLS(certFile, keyFile)
}

// Satisfy the signature of s.TLSNextProto.
func (s *Server) serveConn(hs *http.Server, c *tls.Conn, h http.Handler) {
	s1 := *s
	if hs != nil {
		s1.Server = *hs
	}
	if h != nil {
		s1.Server.Handler = h
	}
	err := s1.ServeConn(c)
	if err != nil {
		log.Println("spdy:", err)
	}
}

// ServeConn serves incoming SPDY requests on c.
// Most people don't need this; they should use
// ListenAndServeTLS instead.
func (s *Server) ServeConn(c net.Conn) error {
	return framing.NewSession(c).Run(true, func(st *framing.Stream) {
		s.serveStream(st, c)
	})
}

func (s *Server) serveStream(st *framing.Stream, c net.Conn) {
	// TODO(kr): recover
	// TODO(kr): buffered reader and writer
	w, err := readRequest(st)
	if err != nil {
		log.Println("spdy: read request failed:", err)
		st.Reply(http.Header{":status": {"400"}}, framing.ControlFlagFin)
		st.Reset(framing.RefusedStream)
		return
	}
	w.req.RemoteAddr = c.RemoteAddr().String()
	handler := s.Handler
	if handler == nil {
		handler = http.DefaultServeMux
	}
	handler.ServeHTTP(w, w.req)
	w.finishRequest()
}

// This is our http.ResponseWriter.
type response struct {
	stream      *framing.Stream
	req         *http.Request
	header      http.Header
	wroteHeader bool
	finished    bool
}

func readRequest(st *framing.Stream) (w *response, err error) {
	req, err := ReadRequest(
		st.Header(),
		nil,
		st, // TODO(kr): buffer
	)
	if err != nil {
		return nil, err
	}
	w = new(response)
	w.header = make(http.Header)
	w.stream = st
	w.req = req
	return w, nil
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
	codestring := strconv.Itoa(code)
	statusText := http.StatusText(code)
	if statusText == "" {
		statusText = "status code " + codestring
	}
	h.Set(":status", codestring+" "+statusText)
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
