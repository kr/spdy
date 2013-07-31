package spdy

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func echoHandler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		copyHeader(w.Header(), r.Header)
		_, err := io.Copy(w, r.Body)
		if err != nil {
			t.Error("echo handler unexpected err", err)
		}
	}
}

func serveConn(t *testing.T, h http.Handler, c net.Conn) {
	var s Server
	s.Handler = h
	err := s.ServeConn(c)
	if err != nil {
		t.Error("server unexpected err", err)
	}
}

func TestConnGet(t *testing.T) {
	cconn, sconn := pipeConn()
	go serveConn(t, echoHandler(t), sconn)

	conn := &Conn{Conn: cconn}
	client := &http.Client{Transport: conn}
	resp, err := client.Get("http://example.com/")
	if err != nil {
		t.Fatal("unexpected err", err)
	}
	respBody := resp.Body
	resp.Body = nil
	resp.Request = nil
	wantResp := &http.Response{
		Status:        "200 OK",
		StatusCode:    200,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Close:         true,
		ContentLength: -1,
		Header: http.Header{
			"Content-Type": {"text/plain"},
			"User-Agent":   {"github.com/kr/spdy"},
		},
	}
	diff(t, "Response", resp, wantResp)
	var bout bytes.Buffer
	if respBody != nil {
		_, err := io.Copy(&bout, respBody)
		if err != nil {
			t.Fatalf("#%d. copying body: %v", err)
		}
		respBody.Close()
	}
	const wantBody = ""
	gotBody := bout.String()
	if gotBody != wantBody {
		t.Errorf("Body = %q want %q", gotBody, wantBody)
	}
}

func testConnPostSize(t *testing.T, size int) {
	cconn, sconn := pipeConn()
	go serveConn(t, echoHandler(t), sconn)

	conn := &Conn{Conn: cconn}
	var buf = make([]byte, size)
	for i := range buf {
		buf[i] = 'a'
	}
	client := &http.Client{Transport: conn}
	resp, err := client.Post("http://example.com/", "text/plain", bytes.NewBuffer(buf))
	if err != nil {
		t.Fatal("unexpected err", err)
	}
	var bout bytes.Buffer
	if resp.Body != nil {
		_, err := io.Copy(&bout, resp.Body)
		if err != nil {
			t.Fatalf("#%d. copying body: %v", err)
		}
		resp.Body.Close()
	}
	resp.Body = nil
	resp.Request = nil
	wantResp := &http.Response{
		Status:        "200 OK",
		StatusCode:    200,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Close:         true,
		ContentLength: int64(size),
		Header: http.Header{
			"Content-Type": {"text/plain"},
			"User-Agent":   {"github.com/kr/spdy"},
		},
	}
	if size > 0 {
		wantResp.Header["Content-Length"] = []string{strconv.Itoa(size)}
	} else {
		wantResp.ContentLength = -1
	}
	diff(t, "Response", resp, wantResp)
	wantBody := string(buf)
	gotBody := bout.String()
	if gotBody != wantBody {
		t.Errorf("Body = %q want %q", gotBody, wantBody)
	}
}

func TestConnPostSizes(t *testing.T) {
	for i := 0; i < 128*1024; i += i/2 + 1 {
		t.Log("size", i)
		testConnPostSize(t, i)
	}
}

func TestConnGetBodyUnknownLen(t *testing.T) {
	cconn, sconn := pipeConn()
	go serveConn(t, echoHandler(t), sconn)

	conn := &Conn{Conn: cconn}
	var buf = make([]byte, 100)
	for i := range buf {
		buf[i] = 'a'
	}
	client := &http.Client{Transport: conn}
	req, err := http.NewRequest("GET", "http://example.com/", bytes.NewBuffer(buf))
	req.Header.Set("Content-Type", "text/plain")

	// for outgoing request, 0 means unknown len if Body is not nil
	req.ContentLength = 0

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal("unexpected err", err)
	}
	var bout bytes.Buffer
	if resp.Body != nil {
		_, err := io.Copy(&bout, resp.Body)
		if err != nil {
			t.Fatalf("#%d. copying body: %v", err)
		}
		resp.Body.Close()
	}
	resp.Body = nil
	resp.Request = nil
	wantResp := &http.Response{
		Status:        "200 OK",
		StatusCode:    200,
		Proto:         "HTTP/1.1",
		ProtoMajor:    1,
		ProtoMinor:    1,
		Close:         true,
		ContentLength: -1,
		Header: http.Header{
			"Content-Type": {"text/plain"},
			"User-Agent":   {"github.com/kr/spdy"},
		},
	}
	diff(t, "Response", resp, wantResp)
	wantBody := string(buf)
	gotBody := bout.String()
	if gotBody != wantBody {
		t.Errorf("Body = %q want %q", gotBody, wantBody)
	}
}

type side struct {
	*io.PipeReader
	*io.PipeWriter
}

func (s side) Close() error {
	return s.PipeWriter.CloseWithError(io.EOF)
}

func (s side) LocalAddr() net.Addr  { return stringAddr("|") }
func (s side) RemoteAddr() net.Addr { return stringAddr("|") }

func (s side) SetDeadline(t time.Time) error      { panic("unimplemented") }
func (s side) SetReadDeadline(t time.Time) error  { panic("unimplemented") }
func (s side) SetWriteDeadline(t time.Time) error { panic("unimplemented") }

type stringAddr string

func (s stringAddr) Network() string { return string(s) }
func (s stringAddr) String() string  { return string(s) }

// pipeConn provides a synchronous, in-memory, two-way data channel.
func pipeConn() (c, s net.Conn) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	return side{cr, cw}, side{sr, sw}
}
