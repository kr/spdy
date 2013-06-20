// Package spdy demultiplexes spdy streams.
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
	errReplied     = errors.New("already replied")
	errNotWritable = errors.New("not writable; must reply first")
)

type resetError RstStreamStatus

func (e resetError) Error() string {
	return fmt.Sprintf("stream was reset: %d", e)
}

// Session represents a session in the low-level SPDY framing layer.
type Session struct {
	Conn    io.ReadWriteCloser
	Handler func(*Stream)

	fr         *Framer
	isServer   bool
	streams    map[StreamId]*Stream
	w          chan Frame
	err        error
	initwnd    int32
	lastRecvId StreamId
	stopped    chan bool
}

func (s *Session) init(isServer bool) error {
	// TODO(kr): buffer s.Conn
	fr, err := NewFramer(s.Conn, s.Conn)
	if err != nil {
		return fmt.Errorf("NewFramer: %v", err)
	}
	s.fr = fr
	s.initwnd = defaultInitWnd
	s.streams = make(map[StreamId]*Stream)
	s.isServer = isServer
	s.w = make(chan Frame)
	s.stopped = make(chan bool)
	return nil
}

// Serve reads incoming frames on s and calls s.Handler in a
// separate goroutine for each incoming SPDY stream.
func (s *Session) Serve() error {
	if err := s.init(true); err != nil {
		return fmt.Errorf("spdy: init: %v", err)
	}
	defer s.Conn.Close()
	defer close(s.stopped)
	defer func() {
		for _, st := range s.streams {
			st.rclose(errClosed)
			st.wclose(errClosed)
		}
	}()

	r := make(chan Frame)
	errCh := make(chan error, 1)

	// TODO(kr): 2 goroutines per session seems like a lot
	go func() {
		for {
			f, err := s.fr.ReadFrame()
			if err != nil {
				errCh <- err
				return
			}
			select {
			case r <- f:
			case <-s.stopped:
				return
			}
		}
	}()

	var err error
	for {
		select {
		case f := <-r:
			err = s.handleRead(f)
		case f := <-s.w:
			err = s.writeFrame(f)
		case err = <-errCh:
		}

		if err != nil {
			// TODO(kr): send GOAWAY
			break
		}
	}
	if err == io.EOF {
		err = nil
	}
	return err
}

func (s *Session) handleRead(f Frame) error {
	switch f := f.(type) {
	case *SynStreamFrame:
		s.handleSynStream(f)
	//case *SynReplyFrame:
	//case *RstStreamFrame:
	case *SettingsFrame:
		s.handleSettings(f)
	case *PingFrame:
		return s.writeFrame(f)
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
	return nil
}

func (s *Session) handleSettings(f *SettingsFrame) {
	for _, v := range f.FlagIdValues {
		s.set(v.Id, v.Value)
	}
}

func (s *Session) set(id SettingsId, val uint32) {
	switch id {
	case SettingsInitialWindowSize:
		if val < 1<<31 {
			s.initwnd = int32(val)
		}
	}
}

func (s *Session) handleSynStream(f *SynStreamFrame) {
	fromServer := f.StreamId%2 == 0
	if s.isServer == fromServer || f.StreamId <= s.lastRecvId {
		s.resetStream(f.StreamId, ProtocolError)
	} else {
		s.lastRecvId = f.StreamId
		st := s.newStream(f.StreamId)
		st.Header = f.Headers
		s.streams[f.StreamId] = st
		if f.CFHeader.Flags&ControlFlagUnidirectional != 0 {
			st.wclose(errClosed)
		}
		if f.CFHeader.Flags&ControlFlagFin != 0 {
			st.rclose(io.EOF)
		}
		go s.Handler(st)
	}
}

func (s *Session) handleWindowUpdate(f *WindowUpdateFrame) {
	st := s.streams[f.StreamId]
	if st == nil {
		// Ignore WINDOW_UPDATE that comes after we send FLAG_FIN.
		// See SPDY/3 section 2.6.8.
		return
	}
	delta := int32(f.DeltaWindowSize)
	ok := true
	st.wszCond.L.Lock()
	prev := st.wndSize
	st.wndSize += delta
	if delta < 1 || (prev > 0 && st.wndSize < 0) {
		ok = false
	}
	st.wszCond.L.Unlock()
	st.wszCond.Signal()
	if !ok {
		s.resetStream(f.StreamId, FlowControlError)
	}
}

func (s *Session) handleData(f *DataFrame) {
	st := s.streams[f.StreamId]
	if st == nil {
		s.resetStream(f.StreamId, InvalidStream)
		return
	}
	if st.rclosed {
		s.resetStream(f.StreamId, StreamAlreadyClosed)
		return
	}
	st.bufCond.L.Lock()
	_, err := st.buf.Write(f.Data)
	st.bufCond.L.Unlock()
	st.bufCond.Signal()
	if f.Flags&DataFlagFin != 0 {
		st.rclose(io.EOF)
	}
	if err != nil {
		s.resetStream(f.StreamId, FlowControlError)
	}
}

func (s *Session) writeFrame(f Frame) error {
	var st *Stream
	fin := false
	switch f := f.(type) {
	case *SynStreamFrame:
		st = s.streams[f.StreamId]
		fin = f.CFHeader.Flags&ControlFlagFin != 0
	case *SynReplyFrame:
		st = s.streams[f.StreamId]
		fin = f.CFHeader.Flags&ControlFlagFin != 0
	case *RstStreamFrame:
		st = s.streams[f.StreamId]
		st.rclose(resetError(f.Status))
		st.wclose(resetError(f.Status))
	//case *SettingsFrame:
	//case *PingFrame:
	//case *GoAwayFrame:
	case *HeadersFrame:
		st = s.streams[f.StreamId]
		fin = f.CFHeader.Flags&ControlFlagFin != 0
	case *WindowUpdateFrame:
	//case *CredentialFrame:
	case *DataFrame:
		st = s.streams[f.StreamId]
		fin = f.Flags&DataFlagFin != 0
	}
	err := s.fr.WriteFrame(f)
	if err != nil {
		log.Println("spdy: write error:", err)
	}
	if st != nil {
		if fin {
			st.wclose(errClosed)
		}
		if st.rclosed && st.wclosed {
			delete(s.streams, st.id)
		}
	}
	return nil
}

func (s *Stream) rclose(err error) {
	if !s.rclosed {
		s.bufCond.L.Lock()
		s.rclosed = true
		s.rErr = err
		s.buf.Close()
		s.bufCond.L.Unlock()
		s.bufCond.Signal()
	}
}

func (s *Stream) wclose(err error) {
	if !s.wclosed {
		s.wszCond.L.Lock()
		s.wclosed = true
		s.wErr = err
		s.wszCond.L.Unlock()
		s.wszCond.Signal()
		close(s.wstop)
	}
}

func (s *Session) resetStream(id StreamId, status RstStreamStatus) error {
	return s.writeFrame(&RstStreamFrame{StreamId: id, Status: status})
}

func (sess *Session) newStream(id StreamId) *Stream {
	s := &Stream{
		id:      id,
		sess:    sess,
		buf:     buffer{buf: make([]byte, defaultInitWnd)},
		bufCond: sync.NewCond(new(sync.Mutex)),
		wndSize: sess.initwnd,
		wszCond: sync.NewCond(new(sync.Mutex)),
		wstop:   make(chan bool),
	}
	return s
}

// Stream represents a stream in the low-level SPDY framing layer.
type Stream struct {
	// Incoming header, from either SYN_STREAM or SYN_REPLY.
	Header http.Header

	// TODO(kr): unimplemented
	// Trailer will be filled in by HEADERS frames received during
	// the stream. Once the stream is closed or half-closed for
	// receiving, Trailer is complete and won't be written to
	// again.
	//Trailer http.Header

	id       StreamId
	sess     *Session
	buf      buffer // incoming data
	bufCond  *sync.Cond
	writable bool
	rclosed  bool
	wclosed  bool
	rErr     error
	wErr     error
	wndSize  int32 // send window size
	wszCond  *sync.Cond
	wstop    chan bool
}

// Reply sends SYN_REPLY with header fields from h.
// It is an error to call Reply twice.
// TODO(kr): also an error to call reply on a stream initiated by
// this side.
func (s *Stream) Reply(h http.Header, flag ControlFlags) error {
	if s.writable {
		return errReplied
	}
	s.writable = true
	f := &SynReplyFrame{
		StreamId: s.id,
		Headers:  h,
	}
	f.CFHeader.Flags = flag
	return s.writeFrame(f)
}

// Read reads the contents of DATA frames received on s.
func (s *Stream) Read(p []byte) (n int, err error) {
	s.bufCond.L.Lock()
	for s.buf.Len() == 0 && !s.buf.closed {
		s.bufCond.Wait()
	}
	n, err = s.buf.Read(p)
	s.bufCond.L.Unlock()
	s.updateWindow(n)
	if err == io.EOF {
		err = s.rErr
	}
	return
}

func (s *Stream) updateWindow(delta int) error {
	if delta < 1 || delta > 1<<31-1 {
		return fmt.Errorf("window delta out of range: %d", delta)
	}
	return s.writeFrame(&WindowUpdateFrame{
		StreamId:        s.id,
		DeltaWindowSize: uint32(delta),
	})
}

// Write writes p as the contents of one or more DATA frames.
// It is an error to call Write before calling Reply on a stream
// initiated by the remote endpoint.
func (s *Stream) Write(p []byte) (n int, err error) {
	var c int
	for n < len(p) && err == nil {
		c, err = s.writeOnce(p[n:])
		n += c
	}
	return n, err
}

// writeOnce writes bytes from p as the contents of a single DATA frame.
func (s *Stream) writeOnce(p []byte) (n int, err error) {
	if !s.writable {
		return 0, errNotWritable
	}
	s.wszCond.L.Lock()
	for s.wndSize <= 0 && !s.wclosed {
		s.wszCond.Wait()
	}
	if s.wclosed {
		s.wszCond.L.Unlock()
		return 0, s.wErr
	}
	if n := int(s.wndSize); n < len(p) {
		p = p[:n]
	}
	s.wndSize -= int32(len(p))
	s.wszCond.L.Unlock()

	err = s.writeFrame(&DataFrame{StreamId: s.id, Data: p})
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close sends an emtpy DATA frame with FLAG_FIN set.
// This shuts down the writing side of s.
// If s is already half-closed for sending, Close is a no-op.
// To close both sides, use Reset.
func (s *Stream) Close() error {
	return s.writeFrame(&DataFrame{
		StreamId: s.id,
		Flags:    DataFlagFin,
	})
}

// Reset sends RST_STREAM, closing the stream and indicating
// an error condition.
// If s is already fully closed, Reset is a no-op.
func (s *Stream) Reset(status RstStreamStatus) error {
	return s.writeFrame(&RstStreamFrame{
		StreamId: s.id,
		Status:   status,
	})
}

func (s *Stream) writeFrame(f Frame) error {
	select {
	case s.sess.w <- f:
		return nil
	case <-s.wstop:
		return s.wErr
	}
}
