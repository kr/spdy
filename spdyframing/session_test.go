package spdyframing

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
)

var sessionTests = []struct {
	handler     func(*testing.T, *Stream) error
	frames      []Frame // even index means client->server; odd the reverse
	wServeErr   error   // wanted return value from Session.Serve
	wHandlerErr []bool  // wanted errors from handler
}{
	{
		handler: failHandler,
		frames:  []Frame{},
	},
	{
		handler: echoHandler,
		frames: []Frame{
			&SynStreamFrame{
				StreamId: 1,
				CFHeader: ControlFrameHeader{Flags: ControlFlagFin},
				Headers:  http.Header{"X": {"y"}},
			},
			&SynReplyFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			nil,
			&DataFrame{
				StreamId: 1,
				Flags:    DataFlagFin,
				Data:     []byte{},
			},
		},
		wHandlerErr: []bool{false},
	},
	{
		handler: echoHandler,
		frames: []Frame{
			&SynStreamFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&SynReplyFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&DataFrame{
				StreamId: 1,
				Flags:    DataFlagFin,
				Data:     []byte{0, 1, 2},
			},
			&WindowUpdateFrame{
				StreamId:        1,
				DeltaWindowSize: 3,
			},
			nil,
			&DataFrame{
				StreamId: 1,
				Data:     []byte{0, 1, 2},
			},
			nil,
			&DataFrame{
				StreamId: 1,
				Flags:    DataFlagFin,
				Data:     []byte{},
			},
		},
		wHandlerErr: []bool{false},
	},
	{
		handler: echoHandler,
		frames: []Frame{
			&SettingsFrame{
				FlagIdValues: []SettingsFlagIdValue{
					{0, SettingsInitialWindowSize, 1},
				},
			},
			nil,

			&SynStreamFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&SynReplyFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&DataFrame{
				StreamId: 1,
				Flags:    DataFlagFin,
				Data:     []byte{0, 1, 2},
			},
			&WindowUpdateFrame{
				StreamId:        1,
				DeltaWindowSize: 3,
			},
			nil,
			&DataFrame{
				StreamId: 1,
				Data:     []byte{0},
			},
			&WindowUpdateFrame{
				StreamId:        1,
				DeltaWindowSize: 1,
			},
			&DataFrame{
				StreamId: 1,
				Data:     []byte{1},
			},
			&WindowUpdateFrame{
				StreamId:        1,
				DeltaWindowSize: 1,
			},
			&DataFrame{
				StreamId: 1,
				Data:     []byte{2},
			},
			nil,
			&DataFrame{
				StreamId: 1,
				Flags:    DataFlagFin,
				Data:     []byte{},
			},
		},
		wHandlerErr: []bool{false},
	},
	{
		handler: failHandler,
		frames: []Frame{
			&PingFrame{Id: 1},
			&PingFrame{Id: 1},
		},
	},
	{
		handler: echoHandler,
		frames: []Frame{
			&SynStreamFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&SynReplyFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&WindowUpdateFrame{
				StreamId:        1,
				DeltaWindowSize: 1<<31 + 1, // invalid
			},
			&RstStreamFrame{
				StreamId: 1,
				Status:   FlowControlError,
			},
		},
		wHandlerErr: []bool{true},
	},
	{
		handler: echoHandler,
		frames: []Frame{
			&SynStreamFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&SynReplyFrame{
				StreamId: 1,
				Headers:  http.Header{"X": {"y"}},
			},
			&WindowUpdateFrame{
				StreamId:        1,
				DeltaWindowSize: 1<<31 - 1, // valid
			},
			nil,
			&WindowUpdateFrame{
				StreamId:        1,
				DeltaWindowSize: 1<<31 - 1, // valid, but total invalid
			},
			&RstStreamFrame{
				StreamId: 1,
				Status:   FlowControlError,
			},
		},
		wHandlerErr: []bool{true},
	},
}

func failHandler(t *testing.T, st *Stream) error {
	t.Fatal("handler called")
	return nil
}

func echoHandler(t *testing.T, st *Stream) error {
	err := st.Reply(st.Header, 0)
	if err != nil {
		return fmt.Errorf("Reply: %v", err)
	}
	_, err = io.Copy(st, st)
	if err != nil {
		return fmt.Errorf("Copy: %v", err)
	}
	err = st.Close()
	if err != nil {
		return fmt.Errorf("Close: %v", err)
	}
	return nil
}

func TestSessionServe(t *testing.T) {
	for i, tt := range sessionTests {
		c, s := pipeConn()
		hErr := make(chan error, 100)
		sess := &Session{
			Conn:    s,
			Handler: func(st *Stream) { hErr <- tt.handler(t, st) },
		}
		errCh := make(chan error)
		go func() { errCh <- sess.Serve() }()

		fr, err := NewFramer(c, c)
		if err != nil {
			t.Errorf("#%d: NewFramer: %v", i, err)
			return
		}
		for j, f := range tt.frames {
			if f == nil {
				continue
			}
			if j%2 == 0 {
				err := fr.WriteFrame(f)
				if err != nil {
					t.Errorf("#%d: write frame: %v", i, err)
					return
				}
			} else {
				gf, err := fr.ReadFrame()
				if err != nil {
					t.Errorf("#%d: read frame %d: %v", i, j, err)
					break
				}
				pubdiff(t, fmt.Sprintf("#%d pkt %d", i, j), f, gf)
			}
		}
		c.Close()
		if err := <-errCh; err != tt.wServeErr {
			t.Errorf("#%d: Serve err = %v want %v", i, err, tt.wServeErr)
		}
		for j, w := range tt.wHandlerErr {
			if g := <-hErr; (g != nil) != w {
				s := "nil"
				if w {
					s = "err"
				}
				t.Errorf("#%d: handler err %d = %v want %v", i, j, g, s)
			}
		}
	}
}

func pubdiff(t *testing.T, prefix string, have, want interface{}) {
	hv := reflect.Indirect(reflect.ValueOf(have))
	wv := reflect.Indirect(reflect.ValueOf(want))
	if hv.Type() != wv.Type() {
		t.Errorf("%s: type = %v want %v", prefix, hv.Type(), wv.Type())
	}
	switch hv.Kind() {
	case reflect.Struct:
		for i := 0; i < hv.NumField(); i++ {
			hf := hv.Field(i)
			wf := wv.Field(i)
			if hf.CanInterface() {
				name := hv.Type().Field(i).Name
				pubdiff(t, prefix+" "+name, hf.Interface(), wf.Interface())
			}
		}
	default:
		if !reflect.DeepEqual(have, want) {
			t.Errorf("%s: %v want %v", prefix, have, want)
		}
	}
}

type side struct {
	io.ReadCloser
	io.WriteCloser
}

func (s side) Close() error {
	s.ReadCloser.Close()
	s.WriteCloser.Close()
	return nil
}

// pipeConn provides a synchronous, in-memory, two-way data channel.
func pipeConn() (c, s io.ReadWriteCloser) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	return side{cr, cw}, side{sr, sw}
}
