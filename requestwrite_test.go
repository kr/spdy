// Copyright 2010 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package spdy

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	framing "github.com/kr/spdy/spdyframing"
)

import "github.com/kr/pretty"

type reqWriteTest struct {
	Req  http.Request
	Body interface{}

	// Either of these may be empty to skip that test.
	WantHeader http.Header
	WantFlag   framing.ControlFlags
	WantError  error
}

var reqWriteTests = []reqWriteTest{
	// HTTP/1.1 => chunked coding; no body; no trailer
	{
		Req: http.Request{
			Method: "GET",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.techcrunch.com",
				Path:   "/",
			},
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header: http.Header{
				"Accept":           {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
				"Accept-Charset":   {"ISO-8859-1,utf-8;q=0.7,*;q=0.7"},
				"Accept-Encoding":  {"gzip,deflate"},
				"Accept-Language":  {"en-us,en;q=0.5"},
				"Keep-Alive":       {"300"},
				"Proxy-Connection": {"keep-alive"},
				"User-Agent":       {"Fake"},
			},
			Body:  nil,
			Close: false,
			Host:  "www.techcrunch.com",
			Form:  map[string][]string{},
		},

		WantFlag: framing.ControlFlagFin,
		WantHeader: http.Header{
			":scheme":         {"http"},
			":method":         {"GET"},
			":path":           {"/"},
			":version":        {"HTTP/1.1"},
			":host":           {"www.techcrunch.com"},
			"User-Agent":      {"Fake"},
			"Accept":          {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
			"Accept-Charset":  {"ISO-8859-1,utf-8;q=0.7,*;q=0.7"},
			"Accept-Encoding": {"gzip,deflate"},
			"Accept-Language": {"en-us,en;q=0.5"},
		},
	},
	// HTTP/1.1 => chunked coding; body; empty trailer
	{
		Req: http.Request{
			Method: "GET",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.google.com",
				Path:   "/search",
			},
			ProtoMajor:       1,
			ProtoMinor:       1,
			Header:           http.Header{},
			TransferEncoding: []string{"chunked"},
		},

		Body: []byte("abcdef"),

		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"GET"},
			":path":      {"/search"},
			":version":   {"HTTP/1.1"},
			":host":      {"www.google.com"},
			"User-Agent": {"github.com/kr/spdy"},
		},
	},
	// HTTP/1.1 POST => chunked coding; body; empty trailer
	{
		Req: http.Request{
			Method: "POST",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.google.com",
				Path:   "/search",
			},
			ProtoMajor:       1,
			ProtoMinor:       1,
			Header:           http.Header{},
			Close:            true,
			TransferEncoding: []string{"chunked"},
		},

		Body: []byte("abcdef"),

		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"POST"},
			":path":      {"/search"},
			":version":   {"HTTP/1.1"},
			":host":      {"www.google.com"},
			"User-Agent": {"github.com/kr/spdy"},
		},
	},

	// HTTP/1.1 POST with Content-Length, no chunking
	{
		Req: http.Request{
			Method: "POST",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.google.com",
				Path:   "/search",
			},
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{},
			Close:         true,
			ContentLength: 6,
		},

		Body: []byte("abcdef"),

		WantHeader: http.Header{
			":scheme":        {"http"},
			":method":        {"POST"},
			":path":          {"/search"},
			":version":       {"HTTP/1.1"},
			":host":          {"www.google.com"},
			"User-Agent":     {"github.com/kr/spdy"},
			"Content-Length": {"6"},
		},
	},

	// HTTP/1.1 POST with Content-Length in headers
	{
		Req: http.Request{
			Method: "POST",
			URL:    mustParseURL("http://example.com/"),
			Host:   "example.com",
			Header: http.Header{
				"Content-Length": []string{"10"}, // ignored
			},
			ContentLength: 6,
		},

		Body: []byte("abcdef"),

		WantHeader: http.Header{
			":scheme":        {"http"},
			":method":        {"POST"},
			":path":          {"/"},
			":version":       {"HTTP/1.1"},
			":host":          {"example.com"},
			"User-Agent":     {"github.com/kr/spdy"},
			"Content-Length": {"6"},
		},
	},

	// default to HTTP/1.1
	{
		Req: http.Request{
			Method: "GET",
			URL:    mustParseURL("/search"),
			Host:   "www.google.com",
		},

		WantFlag: framing.ControlFlagFin,
		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"GET"},
			":path":      {"/search"},
			":version":   {"HTTP/1.1"},
			":host":      {"www.google.com"},
			"User-Agent": {"github.com/kr/spdy"},
		},
	},

	// Request with a 0 ContentLength and a 1 byte body.
	{
		Req: http.Request{
			Method:        "POST",
			URL:           mustParseURL("/"),
			Host:          "example.com",
			ProtoMajor:    1,
			ProtoMinor:    1,
			ContentLength: 0, // as if unset by user
		},

		Body: func() io.ReadCloser { return ioutil.NopCloser(io.LimitReader(strings.NewReader("xx"), 1)) },

		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"POST"},
			":path":      {"/"},
			":version":   {"HTTP/1.1"},
			":host":      {"example.com"},
			"User-Agent": {"github.com/kr/spdy"},
		},
	},

	// Request with a 5 ContentLength and nil body.
	{
		Req: http.Request{
			Method:        "POST",
			URL:           mustParseURL("/"),
			Host:          "example.com",
			ProtoMajor:    1,
			ProtoMinor:    1,
			ContentLength: 5, // but we'll omit the body
		},
		WantError: errors.New("http: Request.ContentLength=5 with nil Body"),
	},

	// Verify that DumpRequest preserves the HTTP version number, doesn't add a Host,
	// and doesn't add a User-Agent.
	{
		Req: http.Request{
			Method:     "GET",
			URL:        mustParseURL("/foo"),
			ProtoMajor: 1,
			ProtoMinor: 0,
			Header: http.Header{
				"X-Foo": []string{"X-Bar"},
			},
		},

		WantFlag: framing.ControlFlagFin,
		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"GET"},
			":path":      {"/foo"},
			":version":   {"HTTP/1.1"},
			":host":      {""},
			"User-Agent": {"github.com/kr/spdy"},
			"X-Foo":      {"X-Bar"},
		},
	},

	// If no Request.Host and no Request.URL.Host, we send
	// an empty Host header, and don't use
	// Request.Header["Host"]. This is just testing that
	// we don't change Go 1.0 behavior.
	{
		Req: http.Request{
			Method: "GET",
			Host:   "",
			URL: &url.URL{
				Scheme: "http",
				Host:   "",
				Path:   "/search",
			},
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header: http.Header{
				"Host": []string{"bad.example.com"},
			},
		},

		WantFlag: framing.ControlFlagFin,
		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"GET"},
			":path":      {"/search"},
			":version":   {"HTTP/1.1"},
			":host":      {""},
			"User-Agent": {"github.com/kr/spdy"},
		},
	},

	// Opaque test #1 from golang.org/issue/4860
	{
		Req: http.Request{
			Method: "GET",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.google.com",
				Opaque: "/%2F/%2F/",
			},
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     http.Header{},
		},

		WantFlag: framing.ControlFlagFin,
		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"GET"},
			":path":      {"/%2F/%2F/"},
			":version":   {"HTTP/1.1"},
			":host":      {"www.google.com"},
			"User-Agent": {"github.com/kr/spdy"},
		},
	},

	// Opaque test #2 from golang.org/issue/4860
	{
		Req: http.Request{
			Method: "GET",
			URL: &url.URL{
				Scheme: "http",
				Host:   "x.google.com",
				Opaque: "//y.google.com/%2F/%2F/",
			},
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     http.Header{},
		},

		WantFlag: framing.ControlFlagFin,
		WantHeader: http.Header{
			":scheme":    {"http"},
			":method":    {"GET"},
			":path":      {"http://y.google.com/%2F/%2F/"},
			":version":   {"HTTP/1.1"},
			":host":      {"x.google.com"},
			"User-Agent": {"github.com/kr/spdy"},
		},
	},

	// Testing custom case in header keys. Issue 5022.
	{
		Req: http.Request{
			Method: "GET",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.google.com",
				Path:   "/",
			},
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header: http.Header{
				"ALL-CAPS": {"x"},
			},
		},

		WantFlag: framing.ControlFlagFin,
		WantHeader: http.Header{
			":scheme":    {"http"},
			":host":      {"www.google.com"},
			":method":    {"GET"},
			":path":      {"/"},
			":version":   {"HTTP/1.1"},
			"User-Agent": {"github.com/kr/spdy"},
			"ALL-CAPS":   {"x"},
		},
	},
}

func TestRequestWrite(t *testing.T) {
	for i, tt := range reqWriteTests {
		switch b := tt.Body.(type) {
		case []byte:
			tt.Req.Body = ioutil.NopCloser(bytes.NewBuffer(b))
		case func() io.ReadCloser:
			tt.Req.Body = b()
		}
		g, gflag, err := RequestFramingHeader(&tt.Req)
		w := tt.WantHeader
		if w != nil && !reflect.DeepEqual(g, w) {
			t.Errorf("#%d", i)
			t.Log(pretty.Sprintf("g = %# v", g))
			t.Log(pretty.Sprintf("w = %# v", w))
		}
		if tt.WantFlag != gflag {
			t.Errorf("#%d: flag = %v want %v", i, gflag, tt.WantFlag)
		}
		if g, w := fmt.Sprintf("%v", err), fmt.Sprintf("%v", tt.WantError); g != w {
			t.Errorf("#%d: err = %v want %v", i, g, w)
			continue
		}
	}
}

func mustParseURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(fmt.Sprintf("Error parsing URL %q: %v", s, err))
	}
	return u
}
