package spdyframing

import (
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
)

// See SPDY/3 section 2.6.8.
const defaultInitWnd = 64 * 1024

var (
	errClosed      = errors.New("closed")
	errNotReadable = errors.New("not readable")
	errCannotReply = errors.New("cannot reply")
	errNotWritable = errors.New("not writable; must reply first")
	errFlowControl = errors.New("flow control")
)

type resetError RstStreamStatus

func (e resetError) Error() string {
	return fmt.Sprintf("stream was reset: %d", e)
}

// Session represents a session in the low-level SPDY framing layer.
type Session struct {
	fr     *Framer
	wmu    sync.Mutex
	openMu sync.Mutex // interlock stream id allocation and SYN_STREAM

	rstreams  map[StreamId]*Stream
	nextSynId StreamId
	initwnd   int32
	closing   bool
	mu        sync.RWMutex

	// accessed only by read goroutine
	lastRecvId StreamId
	err        error

	// not modified
	isServer bool
	handle   func(s *Stream)
	done     chan bool
}

// Start runs a new session on fr.
// If server is true, the session will initiate even-numbered
// streams and expect odd-numbered streams from the remote
// endpoint; otherwise the reverse. Func handle is called in
// a separate goroutine for every incoming stream.
func Start(fr *Framer, server bool, handle func(*Stream)) *Session {
	s := &Session{
		fr:       fr,
		isServer: server,
		initwnd:  defaultInitWnd,
		rstreams: make(map[StreamId]*Stream),
		handle:   handle,
		done:     make(chan bool),
	}
	if server {
		s.nextSynId = 2
	} else {
		s.nextSynId = 1
	}
	go s.read()
	return s
}

// Wait waits until s stops and returns the error, if any.
func (s *Session) Wait() error {
	<-s.done
	return s.err
}

func (s *Session) set(id SettingsId, val uint32) {
	switch id {
	case SettingsInitialWindowSize:
		if val < 1<<31 {
			s.initwnd = int32(val)
		}
	}
}

// if st.id is 0, add will allocate an outgoing id and set it.
func (s *Session) add(st *Stream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing {
		return errors.New("closing")
	}
	if st.id == 0 {
		st.id = s.nextSynId
		s.nextSynId += 2
	}
	s.rstreams[st.id] = st
	return nil
}

func (s *Session) maybeRemove(st *Stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.rclosed && st.wclosed {
		if st1 := s.rstreams[st.id]; st1 == st {
			delete(s.rstreams, st.id)
		}
	}
}

func (s *Session) get(id StreamId) *Stream {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.rstreams[id]
}

// Run reads and writes frames on s.
func (s *Session) read() {
	defer close(s.done)
	defer func() {
		s.mu.Lock()
		s.closing = true
		a := make(map[StreamId]*Stream)
		for id, st := range s.rstreams {
			a[id] = st
		}
		s.mu.Unlock()
		for _, st := range a {
			st.rclose(errClosed)
			st.wnd.Close(errClosed)
			select {
			case st.reply <- nil:
			default:
			}
		}
	}()
	for {
		f, err := s.fr.ReadFrame()
		if err != nil {
			s.err = err
			return
		}
		s.handleRead(f)
	}
}

func (s *Session) handleRead(f Frame) {
	switch f := f.(type) {
	case *SynStreamFrame:
		s.handleSynStream(f)
	case *SynReplyFrame:
		s.handleSynReply(f)
	//case *RstStreamFrame:
	case *SettingsFrame:
		s.handleSettings(f)
	case *PingFrame:
		go s.writeFrame(f)
	//case *GoAwayFrame:
	//case *HeadersFrame:
	case *WindowUpdateFrame:
		s.handleWindowUpdate(f)
	//case *CredentialFrame:
	case *DataFrame:
		s.handleData(f)
	default:
		log.Println("spdy: ignoring unhandled frame:", f)
	}
}

func (s *Session) handleSynStream(f *SynStreamFrame) {
	fromServer := f.StreamId%2 == 0
	if s.isServer == fromServer || f.StreamId <= s.lastRecvId {
		go s.reset(f.StreamId, ProtocolError)
	} else {
		s.lastRecvId = f.StreamId
		st := newStream(s)
		st.id = f.StreamId
		st.header = f.Headers
		err := s.add(st)
		if err != nil {
			return
		}
		if f.CFHeader.Flags&ControlFlagUnidirectional != 0 {
			st.wclose(errClosed)
		}
		if f.CFHeader.Flags&ControlFlagFin != 0 {
			st.rclose(io.EOF)
		}
		go s.handle(st)
	}
}

func (s *Session) handleSynReply(f *SynReplyFrame) {
	st := s.get(f.StreamId)
	if st == nil {
		go s.reset(f.StreamId, InvalidStream)
		return
	}
	select {
	case st.reply <- f.Headers:
	default:
		go s.reset(f.StreamId, InvalidStream)
		return
	}
	if f.CFHeader.Flags&ControlFlagFin != 0 {
		st.rclose(io.EOF)
	}
}

func (s *Session) handleSettings(f *SettingsFrame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, v := range f.FlagIdValues {
		s.set(v.Id, v.Value)
	}
}

func (s *Session) handleWindowUpdate(f *WindowUpdateFrame) {
	if st := s.get(f.StreamId); st != nil {
		st.handleWindowUpdate(int32(f.DeltaWindowSize))
	}
	// Ignore WINDOW_UPDATE that comes after we send FLAG_FIN
	// or any other invalid stream id. See SPDY/3 section 2.6.8.
}

func (s *Session) handleData(f *DataFrame) {
	if st := s.get(f.StreamId); st != nil {
		st.handleData(f.Data, f.Flags)
		return
	}
	go s.reset(f.StreamId, InvalidStream)
}

func (s *Session) writeFrame(f Frame) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return s.fr.WriteFrame(f)
}

func (s *Session) reset(id StreamId, status RstStreamStatus) error {
	return s.writeFrame(&RstStreamFrame{StreamId: id, Status: status})
}

// Open initiates a new SPDY stream with SYN_STREAM.
// Flags invalid for SYN_STREAM will be silently ignored.
func (s *Session) Open(h http.Header, flag ControlFlags) (*Stream, error) {
	st := newStream(s)
	st.wready = true

	// Avoid a race between calls to writeFrame, below.
	// Once add returns, we've assigned the stream id,
	// so don't send them out of order.
	s.openMu.Lock()
	defer s.openMu.Unlock()

	err := s.add(st) // sets st.id
	if err != nil {
		return nil, err
	}
	if flag&ControlFlagUnidirectional != 0 {
		st.rclose(errNotReadable)
	} else {
		st.reply = make(chan http.Header, 1)
	}
	if flag&ControlFlagFin != 0 {
		st.wclose(errNotWritable)
	}
	f := &SynStreamFrame{StreamId: st.id, Headers: h}
	f.CFHeader.Flags = flag & (ControlFlagUnidirectional | ControlFlagFin)
	err = s.writeFrame(f)
	if err != nil {
		st.rclose(err)
		st.wclose(err)
		return nil, err
	}
	return st, nil
}

// Stream represents a stream in the low-level SPDY framing layer.
// It is okay to call Read concurrently with the other methods.
type Stream struct {
	id   StreamId
	sess *Session

	pipe    pipe // incoming data
	rclosed bool

	wready  bool
	wnd     semaphore // send window size
	wclosed bool
	header  http.Header // incoming header (SYN_STREAM or SYN_REPLY)
	reply   chan http.Header

	// TODO(kr): unimplemented
	// Trailer will be filled in by HEADERS frames received during
	// the stream. Once the stream is closed for receiving, Trailer
	// is complete and won't be written to again.
	//Trailer http.Header
}

func newStream(sess *Session) *Stream {
	s := &Stream{sess: sess}
	s.pipe.b.buf = make([]byte, defaultInitWnd)
	s.pipe.c.L = &s.pipe.m
	sess.mu.RLock()
	s.wnd.n = sess.initwnd
	sess.mu.RUnlock()
	s.wnd.c.L = &s.wnd.m
	return s
}

// Incoming header, from either SYN_STREAM or SYN_REPLY.
// Returns nil if there is no incoming direction (either
// because s is unidirectional, or because of an error).
func (s *Stream) Header() http.Header {
	if s.reply != nil {
		s.header = <-s.reply
		s.reply = nil
	}
	return s.header
}

// Reply sends SYN_REPLY with header fields from h.
// It is an error to call Reply twice or to call
// Reply on a stream initiated by the local endpoint.
func (s *Stream) Reply(h http.Header, flag ControlFlags) error {
	if s.wready {
		return errCannotReply
	}
	s.wready = true
	if flag&ControlFlagFin != 0 {
		defer s.wclose(errClosed)
	}
	f := &SynReplyFrame{StreamId: s.id, Headers: h}
	f.CFHeader.Flags = flag
	return s.sess.writeFrame(f)
}

// Read reads the contents of DATA frames received on s.
func (s *Stream) Read(p []byte) (n int, err error) {
	n, err = s.pipe.Read(p)
	s.updateWindow(uint32(n))
	return n, err
}

func (s *Stream) updateWindow(delta uint32) error {
	if delta < 1 || delta > 1<<31-1 {
		return fmt.Errorf("window delta out of range: %d", delta)
	}
	return s.sess.writeFrame(&WindowUpdateFrame{
		StreamId:        s.id,
		DeltaWindowSize: delta,
	})
}

// Write writes p as the contents of one or more DATA frames.
// It is an error to call Write before calling Reply on a stream
// initiated by the remote endpoint.
func (s *Stream) Write(p []byte) (n int, err error) {
	for n < len(p) && err == nil {
		var c int
		c, err = s.writeData(p[n:])
		n += c
	}
	return n, err
}

// writeData writes a single DATA frame containing bytes from p.
func (s *Stream) writeData(p []byte) (int, error) {
	if s.wclosed {
		return 0, errClosed
	}
	if !s.wready {
		return 0, errNotWritable
	}
	n, err := s.wnd.Dec(int32(len(p)))
	if err != nil {
		s.Reset(InternalError)
		return 0, err
	}
	err = s.sess.writeFrame(&DataFrame{StreamId: s.id, Data: p[:n]})
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// Close sends an emtpy DATA or SYN_REPLY frame with FLAG_FIN set.
// This shuts down the writing side of s.
// To close both sides, use Reset.
// It is an error to call Close before calling Reply on a stream
// initiated by the remote endpoint.
func (s *Stream) Close() error {
	if s.wclosed {
		return errClosed
	}
	if !s.wready {
		return errNotWritable
	}
	defer s.wclose(errClosed)
	return s.sess.writeFrame(&DataFrame{StreamId: s.id, Flags: DataFlagFin})
}

// Reset sends RST_STREAM, closing the stream and indicating
// an error condition.
func (s *Stream) Reset(status RstStreamStatus) error {
	defer s.wclose(resetError(status))
	defer s.rclose(resetError(status))
	return s.sess.reset(s.id, status)
}

func (s *Stream) handleWindowUpdate(delta int32) {
	if err := s.wnd.Inc(delta); err != nil {
		s.sess.reset(s.id, FlowControlError)
		s.wnd.Close(errFlowControl)
		s.rclose(errFlowControl)
	}
}

func (s *Stream) handleData(p []byte, flag DataFlags) {
	if s.rclosed {
		go s.sess.reset(s.id, StreamAlreadyClosed)
		return
	}
	switch _, err := s.pipe.Write(p); {
	case err != nil:
		s.wnd.Close(errFlowControl)
		s.rclose(errFlowControl)
		s.sess.reset(s.id, FlowControlError)
	case flag&DataFlagFin != 0:
		s.rclose(io.EOF)
	}
}

func (s *Stream) rclose(err error) {
	s.rclosed = true
	s.pipe.Close(err)
	s.sess.maybeRemove(s)
}

func (s *Stream) wclose(err error) {
	s.wclosed = true
	s.wnd.Close(err)
	s.sess.maybeRemove(s)
}
