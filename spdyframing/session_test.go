package spdyframing

import (
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"reflect"
	"testing"
)

var sessionTests = []struct {
	handler     func(*testing.T, *Stream) error
	frames      []Frame // even index means client->server; odd the reverse
	wRunErr     error   // wanted return value from Session.Run
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
		handler: failHandler,
		frames: []Frame{
			&DataFrame{StreamId: 1, Flags: DataFlagFin},
			&RstStreamFrame{StreamId: 1, Status: InvalidStream},
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
	err := st.Reply(st.Header(), 0)
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

func TestSessionServer(t *testing.T) {
	for i, tt := range sessionTests {
		c, s := pipeConn()
		hErr := make(chan error, 100)
		handler := func(st *Stream) { hErr <- tt.handler(t, st) }
		sess := NewSession(s)
		errCh := make(chan error)
		go func() { errCh <- sess.Run(true, handler) }()

		fr := NewFramer(c, c)
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
		if err := <-errCh; err != tt.wRunErr {
			t.Errorf("#%d: Run err = %v want %v", i, err, tt.wRunErr)
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

func TestSessionClient(t *testing.T) {
	got := make(chan []Frame, 1)
	want := []Frame{
		&SynStreamFrame{StreamId: 1, Headers: http.Header{"X": {"y"}}},
		&DataFrame{StreamId: 1, Data: []byte("foo")},
		&DataFrame{StreamId: 1, Data: []byte{}, Flags: DataFlagFin},
		// The client also sends a WindowUpdateFrame here, but the test
		// harness server doesn't stick around long enough to read it.
	}

	reply := []Frame{
		&SynReplyFrame{StreamId: 1, Headers: http.Header{"X": {"y"}}},
		&DataFrame{StreamId: 1, Data: []byte("foo")},
		&DataFrame{StreamId: 1, Data: []byte{}, Flags: DataFlagFin},
	}

	cpipe, spipe := pipeConn()
	defer cpipe.Close()
	defer spipe.Close()
	fr := NewFramer(spipe, spipe)
	go func() {
		var fs []Frame
		f, err := fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		fs = append(fs, f)
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			fs = append(fs, f)
			if f, ok := f.(*DataFrame); ok && f.Flags&DataFlagFin != 0 {
				got <- fs
				break
			}
		}
		go io.Copy(ioutil.Discard, spipe)
		for _, f := range reply {
			err := fr.WriteFrame(f)
			if err != nil {
				t.Fatal(err)
			}
		}
	}()
	sess := NewSession(cpipe)
	go sess.Run(false, func(st *Stream) { failHandler(t, st) })
	h := http.Header{"X": {"y"}}
	st, err := sess.Open(h, 0)
	if err != nil {
		t.Fatal(err)
	}
	const p = "foo"
	_, err = io.WriteString(st, p)
	if err != nil {
		t.Fatal(err)
	}
	err = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	if gh := st.Header(); !reflect.DeepEqual(gh, h) {
		t.Fatalf("gh = %+v want %+v", gh, h)
	}
	b, err := ioutil.ReadAll(st)
	if err != nil {
		t.Fatal(err)
	}
	if p != string(b) {
		t.Fatalf("b = %q want %q", string(b), p)
	}
	gfs := <-got
	if len(gfs) != len(want) {
		t.Fatalf("frames = %+v want %+v", gfs, want)
	}
	for i := range gfs {
		pubdiff(t, "", gfs[i], want[i])
	}
}

func TestSessionUnidirectional(t *testing.T) {
	var flags ControlFlags = ControlFlagUnidirectional
	got := make(chan []Frame, 1)
	want := []Frame{
		&SynStreamFrame{
			StreamId: 1,
			CFHeader: ControlFrameHeader{Flags: flags},
			Headers:  http.Header{"X": {"y"}},
		},
		&DataFrame{StreamId: 1, Data: []byte("foo")},
		&DataFrame{StreamId: 1, Data: []byte{}, Flags: DataFlagFin},
	}
	cpipe, spipe := pipeConn()
	defer cpipe.Close()
	defer spipe.Close()
	fr := NewFramer(spipe, spipe)
	go func() {
		var fs []Frame
		f, err := fr.ReadFrame()
		if err != nil {
			t.Fatal(err)
		}
		fs = append(fs, f)
		for {
			f, err := fr.ReadFrame()
			if err != nil {
				t.Fatal(err)
			}
			fs = append(fs, f)
			if f, ok := f.(*DataFrame); ok && f.Flags&DataFlagFin != 0 {
				got <- fs
				break
			}
		}
		io.Copy(ioutil.Discard, spipe)
	}()
	sess := NewSession(cpipe)
	go sess.Run(false, func(st *Stream) { failHandler(t, st) })
	st, err := sess.Open(http.Header{"X": {"y"}}, flags)
	if err != nil {
		t.Fatal(err)
	}
	if gh := st.Header(); gh != nil {
		t.Fatalf("Header = %+v want nil", gh)
	}
	const p = "foo"
	_, err = io.WriteString(st, p)
	if err != nil {
		t.Fatal(err)
	}
	err = st.Close()
	if err != nil {
		t.Fatal(err)
	}
	_, err = ioutil.ReadAll(st)
	if err == nil {
		t.Fatal("err = nil want not readable")
	}
	gfs := <-got
	if len(gfs) != len(want) {
		t.Fatalf("frames = %+v want %+v", gfs, want)
	}
	for i := range gfs {
		pubdiff(t, "", gfs[i], want[i])
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
	*io.PipeReader
	*io.PipeWriter
}

func (s side) Close() error {
	s.PipeWriter.CloseWithError(io.EOF)
	return nil
}

// pipeConn provides a synchronous, in-memory, two-way data channel.
func pipeConn() (c, s io.ReadWriteCloser) {
	cr, sw := io.Pipe()
	sr, cw := io.Pipe()
	return side{cr, cw}, side{sr, sw}
}
