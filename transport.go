package spdy

import (
	"net/http"
	"sync"
)

// Transport is an implementation of http.RoundTripper that supports
// SPDY/3 and fallback to another RoundTripper. For https requests,
// it attempts to negotiate a TLS next protocol of "spdy/3", and then
// performs the request.
//
//   http.DefaultTransport = &spdy.Transport{Transport: http.DefaultTransport}
//   http.Get("https://www.google.com/") // SPDY/3 request
//   http.Get("http://www.google.com/") // HTTP/1.1 request
type Transport struct {
	tab map[key]*poolConn
	mu  sync.Mutex

	// Dial specifies the dial function for creating TCP connections.
	// If Dial is nil, net.Dial is used.
	Dial func(network, addr string) (net.Conn, error)

	// TLSClientConfig specifies the TLS configuration to use with
	// tls.Client. If nil, the default configuration is used
	TLSClientConfig *tls.Config

	// Transport is used for https requests if protocol negotiation
	// isn't possible, as well as for all other request schemes.
	// If nil, a default RoundTripper is used.
	Transport http.RoundTripper
}

func (t *Transport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" {
		return t.fallback(r)
	}
	k := requestKey(r)
	switch c := t.getConn(k, r); c.err {
	case nil:
		return c.c.RoundTrip(r)
	case errNPNFailed:
		return t.fallback(r)
	default:
		return nil, c.err
	}
}

type poolConn struct {
	c     *Conn
	err   error
	ready chan bool
}

type key struct {
	scheme, addr string
}

func requestKey(r *http.Request) key {
	return key{r.URL.Scheme, r.URL.Host}
}

func (t *Transport) getConn(k key, r *http.Request) *poolConn {
	t.mu.Lock()
	if t.tab == nil {
		t.tab = make(map[key]*poolConn)
	}
	c, ok := t.tab[k]
	// TODO(kr): if c is closed, remove it
	if ok {
		t.mu.Unlock()
		<-c.ready
		return c
	}
	c = &poolConn{ready: make(chan bool)}
	t.tab[k] = c
	t.mu.Unlock()
	c.c, c.err = t.dialConn(r)
	if c.err != nil {
		t.removeConn(k, c)
	}
	close(c.ready)
	return c
}

// removeConn removes c1 from the pool if present
func (t *Transport) removeConn(k key, c1 *poolConn) {
	t.mu.Lock()
	defer t.mu.Unlock()
	c, ok := t.tab[k]
	if ok && c == c1 {
		delete(t.tab, k)
	}
}

func (t *Transport) dial(network, addr string) (net.Conn, error) {
	if t.Dial != nil {
		return t.Dial(network, addr)
	}
	return net.Dial(network, addr)
}

var errNPNFailed = errors.New("next protocol negotiation failed")

func (t *Transport) dialConn(r *http.Request) (*Conn, error) {
	config := new(*tls.Config)
	if t.TLSClientConfig != nil {
		*config = *t.TLSClientConfig
	}
	config.NextProtos = append(config.NextProtos, "spdy/3")
	c, err := t.dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	c = tls.Client(c, config)
	if err = c.(*tls.Conn).Handshake(); err != nil {
		return nil, err
	}
	if c.(*tls.Conn).ConnectionState().NegotiatedProtocol != "spdy/3" {
		// TODO(kr): find a way to reuse c as vanilla https
		c.Close()
		return nil, errNPNFailed
	}
	return &Conn{Conn: c}, nil
}

func (t *Transport) fallback(r http.Request) (*http.Response, error) {
	transport := t.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	return transport.RoundTrip(r)
}
