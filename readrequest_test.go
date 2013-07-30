package spdy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

type reqTest struct {
	RawHeader  http.Header
	RawData    string
	RawTrailer http.Header
	Req        *http.Request
	Body       string
	Trailer    http.Header
	Error      string
}

var noError = ""
var noBody = ""
var noTrailer http.Header = nil

var reqTests = []reqTest{
	// Baseline test; All Request fields included for template use
	{
		http.Header{
			":scheme":          {"http"},
			":method":          {"GET"},
			":path":            {"/"},
			":host":            {"www.techcrunch.com"},
			":version":         {"HTTP/1.1"},
			":query":			{""},
			"User-Agent":       {"Fake"},
			"Accept":           {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
			"Accept-Language":  {"en-us,en;q=0.5"},
			"Accept-Encoding":  {"gzip,deflate"},
			"Accept-Charset":   {"ISO-8859-1,utf-8;q=0.7,*;q=0.7"},
			"Keep-Alive":       {"300"},
			"Content-Length":   {"7"},
			"Proxy-Connection": {"keep-alive"},
		},

		"abcdef\n",
		noTrailer,

		&http.Request{
			Method: "GET",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.techcrunch.com",
				Path:   "/",
				RawQuery: "",
			},
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header: http.Header{
				"Accept":           {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
				"Accept-Language":  {"en-us,en;q=0.5"},
				"Accept-Encoding":  {"gzip,deflate"},
				"Accept-Charset":   {"ISO-8859-1,utf-8;q=0.7,*;q=0.7"},
				"Keep-Alive":       {"300"},
				"Proxy-Connection": {"keep-alive"},
				"Content-Length":   {"7"},
				"User-Agent":       {"Fake"},
			},
			Close:         true,
			ContentLength: 7,
			Host:          "www.techcrunch.com",
			RequestURI:    "",
		},

		"abcdef\n",

		noTrailer,
		noError,
	},

	// GET request with no body (the normal case)
	{
		http.Header{
			":scheme":  {"http"},
			":method":  {"GET"},
			":path":    {"/"},
			":host":    {"foo.com"},
			":version": {"HTTP/1.1"},
			":query":	{""},
		},
		noBody,
		noTrailer,

		&http.Request{
			Method: "GET",
			URL: &url.URL{
				Scheme: "http",
				Host:   "foo.com",
				Path:   "/",
			},
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{},
			Close:         true,
			ContentLength: 0,
			Host:          "foo.com",
			RequestURI:    "",
		},

		noBody,
		noTrailer,
		noError,
	},

	// Tests a bogus abs_path on the Request-Line (RFC 2616 section 5.1.2)
	{
		http.Header{
			":scheme":  {"http"},
			":method":  {"GET"},
			":path":    {"../../../../etc/passwd"},
			":host":    {"test"},
			":version": {"HTTP/1.1"},
			":query":	{""},
		},
		noBody,
		noTrailer,
		nil,
		noBody,
		noTrailer,
		"invalid path: ../../../../etc/passwd",
	},

	// Tests missing :path:
	{
		http.Header{
			":scheme":  {"http"},
			":method":  {"GET"},
			":host":    {"test"},
			":version": {"HTTP/1.1"},
			":query":	{""},
		},
		noBody,
		noTrailer,
		nil,
		noBody,
		noTrailer,
		"missing path",
	},

	// Tests body with trailer:
	{
		http.Header{
			":scheme":  {"http"},
			":method":  {"POST"},
			":path":    {"/"},
			":host":    {"foo.com"},
			":version": {"HTTP/1.1"},
			":query":	{""},
		},
		"foobar",
		http.Header{
			"Trailer-Key": {"Trailer-Value"},
		},
		&http.Request{
			Method: "POST",
			URL: &url.URL{
				Scheme: "http",
				Host:   "foo.com",
				Path:   "/",
			},
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
			Host:          "foo.com",
			RequestURI:    "",
		},

		"foobar",
		http.Header{
			"Trailer-Key": {"Trailer-Value"},
		},
		noError,
	},

	// CONNECT request with domain name:
	{
		http.Header{
			":scheme":  {"http"},
			":method":  {"CONNECT"},
			":path":    {"/"},
			":host":    {"www.google.com:443"},
			":version": {"HTTP/1.1"},
			":query":	{""},
		},
		noBody,
		noTrailer,

		&http.Request{
			Method: "CONNECT",
			URL: &url.URL{
				Scheme: "http",
				Host:   "www.google.com:443",
				Path:   "/",
			},
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
			Host:          "www.google.com:443",
			RequestURI:    "",
		},

		noBody,
		noTrailer,
		noError,
	},

	// CONNECT request with IP address:
	{
		http.Header{
			":scheme":  {"http"},
			":method":  {"CONNECT"},
			":path":    {"/"},
			":host":    {"127.0.0.1:6060"},
			":version": {"HTTP/1.1"},
			":query":	{""},
		},
		noBody,
		noTrailer,

		&http.Request{
			Method: "CONNECT",
			URL: &url.URL{
				Scheme: "http",
				Host:   "127.0.0.1:6060",
				Path:   "/",
			},
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
			Host:          "127.0.0.1:6060",
			RequestURI:    "",
		},

		noBody,
		noTrailer,
		noError,
	},

	// CONNECT request for RPC:
	{
		http.Header{
			":scheme":  {"http"},
			":method":  {"CONNECT"},
			":path":    {"/_goRPC_"},
			":host":    {""},
			":version": {"HTTP/1.1"},
			":query":	{""},
		},
		noBody,
		noTrailer,

		&http.Request{
			Method: "CONNECT",
			URL: &url.URL{
				Scheme: "http",
				Path:   "/_goRPC_",
			},
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
			Host:          "",
			RequestURI:    "",
		},

		noBody,
		noTrailer,
		noError,
	},
}

func TestNewRequest(t *testing.T) {
	for i := range reqTests {
		tt := &reqTests[i]
		trailer := noTrailer
		if tt.RawTrailer != nil {
			trailer = make(http.Header)
		}
		req, err := ReadRequest(
			tt.RawHeader,
			trailer,
			strings.NewReader(tt.RawData),
		)
		if err != nil {
			if err.Error() != tt.Error {
				t.Errorf("#%d: error %q, want error %q", i, err.Error(), tt.Error)
			}
			continue
		}
		// Fill in the trailer after the request is created,
		// but before the body is finished reading.
		copyHeader(trailer, tt.RawTrailer)
		rbody := req.Body
		req.Body = nil
		diff(t, fmt.Sprintf("#%d Request", i), req, tt.Req)
		var bout bytes.Buffer
		if rbody != nil {
			_, err := io.Copy(&bout, rbody)
			if err != nil {
				t.Fatalf("#%d. copying body: %v", i, err)
			}
			rbody.Close()
		}
		body := bout.String()
		if body != tt.Body {
			t.Errorf("#%d: Body = %q want %q", i, body, tt.Body)
		}
		if !reflect.DeepEqual(tt.Trailer, req.Trailer) {
			t.Errorf("#%d. Trailers differ.\n got: %v\nwant: %v", i, req.Trailer, tt.Trailer)
		}
	}
}

func TestFramingHeader(t *testing.T) {
	for i, tt := range reqTests {
		if tt.Req != nil {
			g := RequestFramingHeader(tt.Req)
			w := tt.RawHeader
			if !reflect.DeepEqual(g, w) {
				t.Errorf("#%d: RawHeader = %v want %v", i, g, w)
			}
		}
	}
}

func diff(t *testing.T, prefix string, have, want interface{}) {
	hv := reflect.ValueOf(have).Elem()
	wv := reflect.ValueOf(want).Elem()
	if hv.Type() != wv.Type() {
		t.Errorf("%s: type mismatch %v want %v", prefix, hv.Type(), wv.Type())
	}
	for i := 0; i < hv.NumField(); i++ {
		hf := hv.Field(i).Interface()
		wf := wv.Field(i).Interface()
		if !reflect.DeepEqual(hf, wf) {
			t.Errorf("%s: %s = %v want %v", prefix, hv.Type().Field(i).Name, hf, wf)
		}
	}
}
