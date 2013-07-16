package spdyframing

import (
	"errors"
	"testing"
)

func TestSemaphoreClose(t *testing.T) {
	var s semaphore
	s.n = 1
	s.c.L = &s.m
	a := errors.New("a")
	b := errors.New("b")
	s.Close(a)
	s.Close(b)
	_, err := s.Dec(1)
	if err != a {
		t.Errorf("err = %v want %v", err, a)
	}
}
