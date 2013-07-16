package spdyframing

import (
	"errors"
	"sync"
)

type semaphore struct {
	n      int32
	c      sync.Cond
	m      sync.Mutex
	closed bool
	err    error
}

func (s *semaphore) Dec(n int32) (int32, error) {
	s.c.L.Lock()
	defer s.c.L.Unlock()
	for s.n < 1 && !s.closed {
		s.c.Wait()
	}
	if s.closed {
		return 0, s.err
	}
	if s.n < n {
		n = s.n
	}
	s.n -= n
	return n, nil
}

func (s *semaphore) Inc(n int32) error {
	s.c.L.Lock()
	defer s.c.L.Unlock()
	defer s.c.Signal()

	prev := s.n
	s.n += n
	wrapped := prev > 0 && s.n < 0
	if n < 1 || wrapped {
		return errors.New("bad increment")
	}
	return nil
}

func (s *semaphore) Close(err error) {
	s.c.L.Lock()
	defer s.c.L.Unlock()
	defer s.c.Signal()
	if !s.closed {
		s.closed = true
		s.err = err
	}
}
