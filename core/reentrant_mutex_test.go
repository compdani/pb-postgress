package core

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReentrantMutexSameGoroutineReenters(t *testing.T) {
	var m reentrantMutex

	done := make(chan struct{})
	go func() {
		defer close(done)
		m.Lock()
		m.Lock() // would deadlock with a plain mutex
		m.Unlock()
		m.Unlock()
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("re-entering the mutex on the same goroutine deadlocked")
	}
}

func TestReentrantMutexExcludesOtherGoroutines(t *testing.T) {
	var m reentrantMutex
	var inside atomic.Int32
	var max atomic.Int32

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				m.Lock()
				m.Lock()
				n := inside.Add(1)
				if n > max.Load() {
					max.Store(n)
				}
				time.Sleep(10 * time.Microsecond)
				inside.Add(-1)
				m.Unlock()
				m.Unlock()
			}
		}()
	}
	wg.Wait()

	if max.Load() != 1 {
		t.Fatalf("goroutines overlapped inside the mutex: %d", max.Load())
	}
}
