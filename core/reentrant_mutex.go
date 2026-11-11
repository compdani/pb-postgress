package core

import (
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
)

// reentrantMutex is a mutex that the goroutine holding it can lock again
// (every Lock needs its Unlock).
//
// It serializes the settings reloads: their hooks may save settings, which
// reloads them again from the same goroutine, while no other goroutine may
// run a reload (and its hooks) at the same time.
type reentrantMutex struct {
	mu    sync.Mutex
	owner atomic.Uint64 // id of the goroutine holding mu, 0 if none
	depth int           // only touched by the owner
}

func (m *reentrantMutex) Lock() {
	id := goroutineID()
	if m.owner.Load() == id {
		m.depth++
		return
	}

	m.mu.Lock()
	m.owner.Store(id)
	m.depth = 1
}

func (m *reentrantMutex) Unlock() {
	m.depth--
	if m.depth == 0 {
		m.owner.Store(0)
		m.mu.Unlock()
	}
}

// goroutineID returns the id of the calling goroutine (the runtime offers no
// direct API for it).
func goroutineID() uint64 {
	var buf [64]byte
	b := buf[:runtime.Stack(buf[:], false)]

	// "goroutine 123 [running]:..."
	const prefix = "goroutine "
	if len(b) < len(prefix) {
		return 0
	}
	b = b[len(prefix):]

	end := 0
	for end < len(b) && b[end] >= '0' && b[end] <= '9' {
		end++
	}

	id, _ := strconv.ParseUint(string(b[:end]), 10, 64)
	return id
}
