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
// forever), and a Ring makes that read double as the output a later view
// (admin-3rgp.65) renders — one drain, not two.
//
// The API is deliberately small: admin-3rgp.62 through .66 build on top of
// this exact shape, so it stays a Write sink plus a read-only snapshot and
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

// bytesLocked is Bytes' body, callable by another method already holding
// r.mu (Since below) so it doesn't have to re-lock or duplicate the
// wrap-around copy.
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

// Since returns the bytes written after off — a position in the ring's
// total-written-byte count, as returned by a previous Since or by Total —
// plus the offset to pass on the next call. This is the terminal view's
// (admin-3rgp.65) incremental read: attach records Total() so nothing
// written before it is ever replayed, and each poll calls Since(lastOff) to
// feed the emulator only what is new, never the whole ring, and never the
// same bytes twice.
//
// off is clamped up to the oldest byte the ring still holds when the ring
// has lapped it since the caller last read — the returned next reports that
// clamp (it jumps past whatever off asked for) rather than erroring, since a
// dropped span of output is a visible gap in the emulator's screen, not a
// failure of the read itself.
func (r *Ring) Since(off int64) (b []byte, next int64) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if off < 0 {
		off = 0
	}
	if off > r.total {
		off = r.total
	}
	oldest := r.total - int64(r.len)
	if off < oldest {
		off = oldest
	}
	if off == r.total {
		return nil, r.total
	}

	all := r.bytesLocked()
	skip := int(off - oldest)
	if skip < 0 {
		skip = 0
	}
	if skip > len(all) {
		skip = len(all)
	}
	out := make([]byte, len(all)-skip)
	copy(out, all[skip:])
	return out, r.total
}

// Total returns the number of bytes ever written to the ring, monotonic and
// never reset — the terminal view's attach-time replay offset (see Since).
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
