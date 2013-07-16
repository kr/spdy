package spdy

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"strings"
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

	conn := NewConn(cconn)
	conn.once.Do(func() {
		go func() {
			err := conn.s.Run(false, nil)
			if err != nil {
				t.Error("client unexpected err", err)
			}
		}()
	})
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

const shortBody = "hello"

func TestConnPostShortBody(t *testing.T) {
	cconn, sconn := pipeConn()
	go serveConn(t, echoHandler(t), sconn)

	conn := NewConn(cconn)
	conn.once.Do(func() {
		go func() {
			err := conn.s.Run(false, nil)
			if err != nil {
				t.Error("client unexpected err", err)
			}
		}()
	})
	client := &http.Client{Transport: conn}
	resp, err := client.Post("http://example.com/", "text/plain", strings.NewReader(shortBody))
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
	const wantBody = shortBody
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
