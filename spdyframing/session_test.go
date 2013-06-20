package spdyframing

import (
	"fmt"
	"io"
	"net/http"
	"reflect"
	"testing"
)

var sessionTests = []struct {
	handler func(*testing.T, *Stream)
	frames  []Frame // even index means client->server; odd the reverse
	werr    error   // wanted return value from Session.Serve
}{
	{
		handler: failHandler,
		frames:  []Frame{},
		werr:    nil,
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
		werr: nil,
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
		werr: nil,
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
		werr: nil,
	},
}

func failHandler(t *testing.T, st *Stream) {
	t.Error("handler called")
}

func echoHandler(t *testing.T, st *Stream) {
	err := st.Reply(st.Header, 0)
	if err != nil {
		t.Error("Reply:", err)
		return
	}
	_, err = io.Copy(st, st)
	if err != nil {
		t.Error("Copy:", err)
		return
	}
	err = st.Close()
	if err != nil {
		t.Error("Close:", err)
	}
}

func TestSessionServe(t *testing.T) {
	for i, tt := range sessionTests {
		c, s := pipeConn()
		sess := &Session{
			Conn:    s,
			Handler: func(st *Stream) { tt.handler(t, st) },
		}
		errCh := make(chan error, 1)
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
		if err := <-errCh; err != tt.werr {
			t.Errorf("#%d: Serve err = %v want %v", i, err, tt.werr)
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
