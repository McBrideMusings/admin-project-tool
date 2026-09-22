package livework

import "sync"

// defaultRingLimit bounds a Ring created without an explicit size — enough
// to hold a build's tail output without the registry growing unboundedly
// for a long-lived hosted pty.
const defaultRingLimit = 64 * 1024

// Ring is a bounded, concurrency-safe byte buffer that overwrites its oldest
// bytes once full. It is the drain target for a hosted pty's master fd
// (admin-3rgp.61): reading the master is what lets the child's tty close at
// exit complete on macOS (nothing reads, nothing drains, the child blocks
// forever), and a Ring makes that read double as the Work band's text tail
// — one drain, not two. The attached terminal view renders the entry's
// Screen instead, fed by the same drain.
//
// The API is deliberately small: a Write sink plus a read-only snapshot and
// nothing else — no growth, no seeking, no per-entry configuration.
type Ring struct {
	mu    sync.Mutex
	buf   []byte
	next  int   // index the next Write starts overwriting once full
	len   int   // bytes currently held, <= cap(buf)
	full  bool  // buf has wrapped at least once
	total int64 // monotonic count of every byte ever written, never reset
}

// NewRing returns an empty Ring that holds at most limit bytes. limit <= 0
// falls back to defaultRingLimit.
func NewRing(limit int) *Ring {
	if limit <= 0 {
		limit = defaultRingLimit
	}
	return &Ring{buf: make([]byte, limit)}
}

// Write appends p to the ring, overwriting the oldest bytes first once the
// ring is full. It never fails — Ring satisfies io.Writer so a drain
// goroutine can be exactly io.Copy(ring, ptmx) — and always reports len(p)
// written.
func (r *Ring) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	r.total += int64(n)
	limit := len(r.buf)
	if limit == 0 {
		return n, nil
	}

	// A write longer than the whole ring only leaves its own tail behind —
	// everything before that tail would be immediately overwritten anyway.
	if n > limit {
		p = p[n-limit:]
	}

	for _, b := range p {
		r.buf[r.next] = b
		r.next = (r.next + 1) % limit
		if !r.full {
			r.len++
			if r.len == limit {
				r.full = true
			}
		}
	}
	return n, nil
}

// Bytes returns a copy of the ring's current contents, oldest byte first.
// The returned slice is independent of the ring — safe to hold and read
// after further Writes.
func (r *Ring) Bytes() []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytesLocked()
}

// bytesLocked is Bytes' body, run with r.mu held.
func (r *Ring) bytesLocked() []byte {
	out := make([]byte, r.len)
	if r.len == 0 {
		return out
	}
	if !r.full {
		copy(out, r.buf[:r.len])
		return out
	}
	// Full: oldest byte is at r.next (the next slot Write will clobber).
	n := copy(out, r.buf[r.next:])
	copy(out[n:], r.buf[:r.next])
	return out
}

// Total returns the number of bytes ever written to the ring, monotonic and
// never reset.
func (r *Ring) Total() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.total
}

// Len returns the number of bytes currently held (<= the ring's limit).
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.len
}
