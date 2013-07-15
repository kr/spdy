package spdy

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

type respTest struct {
	RawHeader http.Header
	RawData   string
	Resp      http.Response
	Body      string
}

func dummyReq(method string) *http.Request {
	return &http.Request{Method: method}
}

var respTests = []respTest{
	// Unchunked response without Content-Length.
	{
		http.Header{
			":version": {"HTTP/1.0"},
			":status":  {"200 OK"},
		},
		"Body here\n",

		http.Response{
			Status:        "200 OK",
			StatusCode:    200,
			Proto:         "HTTP/1.0",
			ProtoMajor:    1,
			ProtoMinor:    0,
			Request:       dummyReq("GET"),
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
		},

		"Body here\n",
	},

	// Unchunked HTTP/1.1 response without Content-Length or
	// Connection headers.
	{
		http.Header{
			":version": {"HTTP/1.1"},
			":status":  {"200 OK"},
		},
		"Body here\n",

		http.Response{
			Status:        "200 OK",
			StatusCode:    200,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Request:       dummyReq("GET"),
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
		},

		"Body here\n",
	},

	// Unchunked HTTP/1.1 204 response without Content-Length.
	{
		http.Header{
			":version": {"HTTP/1.1"},
			":status":  {"204 No Content"},
		},
		"Body should not be read!\n",

		http.Response{
			Status:        "204 No Content",
			StatusCode:    204,
			Proto:         "HTTP/1.1",
			ProtoMajor:    1,
			ProtoMinor:    1,
			Request:       dummyReq("GET"),
			Header:        http.Header{},
			Close:         true,
			ContentLength: 0,
		},

		"",
	},

	// Unchunked response with Content-Length.
	{
		http.Header{
			":version":       {"HTTP/1.0"},
			":status":        {"200 OK"},
			"Content-Length": {"10"},
		},
		"Body here\n",

		http.Response{
			Status:     "200 OK",
			StatusCode: 200,
			Proto:      "HTTP/1.0",
			ProtoMajor: 1,
			ProtoMinor: 0,
			Request:    dummyReq("GET"),
			Header: http.Header{
				"Content-Length": {"10"},
			},
			Close:         true,
			ContentLength: 10,
		},

		"Body here\n",
	},

	// Content-Length in response to a HEAD request
	{
		http.Header{
			":version":       {"HTTP/1.0"},
			":status":        {"200 OK"},
			"Content-Length": {"256"},
		},
		"",

		http.Response{
			Status:           "200 OK",
			StatusCode:       200,
			Proto:            "HTTP/1.0",
			ProtoMajor:       1,
			ProtoMinor:       0,
			Request:          dummyReq("HEAD"),
			Header:           http.Header{"Content-Length": {"256"}},
			TransferEncoding: nil,
			Close:            true,
			ContentLength:    256,
		},

		"",
	},

	// Content-Length in response to a HEAD request with HTTP/1.1
	{
		http.Header{
			":version":       {"HTTP/1.1"},
			":status":        {"200 OK"},
			"Content-Length": {"256"},
		},
		"",

		http.Response{
			Status:           "200 OK",
			StatusCode:       200,
			Proto:            "HTTP/1.1",
			ProtoMajor:       1,
			ProtoMinor:       1,
			Request:          dummyReq("HEAD"),
			Header:           http.Header{"Content-Length": {"256"}},
			TransferEncoding: nil,
			Close:            true,
			ContentLength:    256,
		},

		"",
	},

	// No Content-Length or Chunked in response to a HEAD request
	{
		http.Header{
			":version": {"HTTP/1.0"},
			":status":  {"200 OK"},
		},
		"",

		http.Response{
			Status:           "200 OK",
			StatusCode:       200,
			Proto:            "HTTP/1.0",
			ProtoMajor:       1,
			ProtoMinor:       0,
			Request:          dummyReq("HEAD"),
			Header:           http.Header{},
			TransferEncoding: nil,
			Close:            true,
			ContentLength:    -1,
		},

		"",
	},

	// explicit Content-Length of 0.
	{
		http.Header{
			":version":       {"HTTP/1.1"},
			":status":        {"200 OK"},
			"Content-Length": {"0"},
		},
		"",

		http.Response{
			Status:     "200 OK",
			StatusCode: 200,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Request:    dummyReq("GET"),
			Header: http.Header{
				"Content-Length": {"0"},
			},
			Close:         true,
			ContentLength: 0,
		},

		"",
	},

	// Status line without a Reason-Phrase, but trailing space.
	// (permitted by RFC 2616)
	{
		http.Header{
			":version": {"HTTP/1.0"},
			":status":  {"303 "},
		},
		"",
		http.Response{
			Status:        "303 ",
			StatusCode:    303,
			Proto:         "HTTP/1.0",
			ProtoMajor:    1,
			ProtoMinor:    0,
			Request:       dummyReq("GET"),
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
		},

		"",
	},

	// Status line without a Reason-Phrase, and no trailing space.
	// (not permitted by RFC 2616, but we'll accept it anyway)
	{
		http.Header{
			":version": {"HTTP/1.0"},
			":status":  {"303"},
		},
		"",
		http.Response{
			Status:        "303 ",
			StatusCode:    303,
			Proto:         "HTTP/1.0",
			ProtoMajor:    1,
			ProtoMinor:    0,
			Request:       dummyReq("GET"),
			Header:        http.Header{},
			Close:         true,
			ContentLength: -1,
		},

		"",
	},

	// golang.org/issue/4767: don't special-case multipart/byteranges responses
	{
		http.Header{
			":version":     {"HTTP/1.1"},
			":status":      {"206 Partial Content"},
			"Content-Type": {"multipart/byteranges; boundary=18a75608c8f47cef"},
		},
		"some body",
		http.Response{
			Status:     "206 Partial Content",
			StatusCode: 206,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Request:    dummyReq("GET"),
			Header: http.Header{
				"Content-Type": []string{"multipart/byteranges; boundary=18a75608c8f47cef"},
			},
			Close:         true,
			ContentLength: -1,
		},

		"some body",
	},
}

func TestReadResponse(t *testing.T) {
	for i, tt := range respTests {
		resp, err := ReadResponse(
			tt.RawHeader,
			nil,
			strings.NewReader(tt.RawData),
			tt.Resp.Request,
		)
		if err != nil {
			t.Errorf("#%d: %v", i, err)
			continue
		}
		rbody := resp.Body
		resp.Body = nil
		diff(t, fmt.Sprintf("#%d Response", i), resp, &tt.Resp)
		var bout bytes.Buffer
		if rbody != nil {
			_, err = io.Copy(&bout, rbody)
			if err != nil {
				t.Errorf("#%d: %v", i, err)
				continue
			}
			rbody.Close()
		}
		body := bout.String()
		if body != tt.Body {
			t.Errorf("#%d: Body = %q want %q", i, body, tt.Body)
		}
	}
}

func TestResponseStatusStutter(t *testing.T) {
	r := &response{}
	h := r.framingHeader(123)
	if s := h.Get(":status"); strings.Contains(s, "123 123") {
		t.Errorf("stutter in status: %s", s)
	}
}

var invalidResponseHeaders = []http.Header{
	// bad version string
	http.Header{
		":version": {"SPDY"},
		":status":  {"200 OK"},
	},

	// bad status
	http.Header{
		":version": {"HTTP/1.1"},
		":status":  {"a"},
	},

	// bad content-length
	http.Header{
		":version":       {"HTTP/1.1"},
		":status":        {"200 OK"},
		"Content-Length": {"a"},
	},

	// response with Connection
	http.Header{
		":version":   {"HTTP/1.1"},
		":status":    {"200 OK"},
		"Connection": {"close"},
	},

	// response with Keep-Alive
	http.Header{
		":version":   {"HTTP/1.1"},
		":status":    {"200 OK"},
		"Keep-Alive": {"max=1"},
	},

	// response with Proxy-Connection
	http.Header{
		":version":         {"HTTP/1.1"},
		":status":          {"200 OK"},
		"Proxy-Connection": {"keep-alive"},
	},

	// response with Transfer-Encoding
	http.Header{
		":version":          {"HTTP/1.1"},
		":status":           {"200 OK"},
		"Transfer-Encoding": {"chunked"},
	},
}

func TestReadResponseError(t *testing.T) {
	for i, tt := range invalidResponseHeaders {
		resp, err := ReadResponse(tt, nil, nil, dummyReq("GET"))
		if err == nil {
			t.Errorf("#%d: expected error", i)
		}
		if resp != nil {
			t.Errorf("#%d: resp = %v want nil", i, resp)
		}
	}
}
