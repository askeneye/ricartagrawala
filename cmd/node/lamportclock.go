package main

import "sync"

type LamportClock struct {
	mu   sync.Mutex
	time int64
}

// Called when the node does something locally like sending a message
func (lc *LamportClock) Tick() int64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	lc.time++
	return lc.time
}

// Receive updates
// Called when a node receives a message that has a timestamp ts
// The rule: time = max(time, ts) + 1
func (lc *LamportClock) Receive(ts int64) int64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	if ts > lc.time {
		lc.time = ts
	}
	lc.time++
	return lc.time
}

// Read returns the current clock value
func (lc *LamportClock) Read() int64 {
	lc.mu.Lock()
	defer lc.mu.Unlock()
	return lc.time
}
