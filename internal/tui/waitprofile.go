package tui

import (
	"time"

	"pgdu/internal/pg"
)

// waitCPUClass is the bucket for backends running with no wait event — time
// attributed to "on CPU" rather than any wait class.
const waitCPUClass = "CPU (running)"

// waitRingCap bounds the sample retention: 600 buckets is 5 minutes at the
// fastest 500ms cadence (longer at slower cadences). Fixed so a long-lived
// session can't grow the profile without bound.
const waitRingCap = 600

// waitBucket is one sampled snapshot: non-idle backend counts per wait class
// ("LWLock:WALWrite", "IO:DataFileRead", waitCPUClass, …) at one activity tick.
type waitBucket struct {
	at     time.Time
	counts map[string]int
	total  int
}

// waitRing is a fixed-capacity circular buffer of waitBucket.
type waitRing struct {
	buf  []waitBucket
	head int // next write position
	n    int // valid entries (≤ cap)
}

func newWaitRing() *waitRing { return &waitRing{buf: make([]waitBucket, waitRingCap)} }

func (r *waitRing) push(b waitBucket) {
	r.buf[r.head] = b
	r.head = (r.head + 1) % len(r.buf)
	if r.n < len(r.buf) {
		r.n++
	}
}

// ordered returns the retained buckets oldest→newest.
func (r *waitRing) ordered() []waitBucket {
	out := make([]waitBucket, 0, r.n)
	start := (r.head - r.n + len(r.buf)) % len(r.buf)
	for i := range r.n {
		out = append(out, r.buf[(start+i)%len(r.buf)])
	}
	return out
}

// classifyWait buckets one non-idle backend: running with no wait event is
// CPU time; everything else keys on wait_event_type:wait_event.
func classifyWait(r pg.ActivityRow) string {
	if r.WaitEventType == "" {
		return waitCPUClass
	}
	return r.WaitEventType + ":" + r.WaitEvent
}

// waitSampleRow reports whether an activity row belongs in the wait profile:
// non-idle backends only. Idle backends are parked on the client — counting
// them would drown the histogram in Client:ClientRead.
func waitSampleRow(r pg.ActivityRow) bool {
	switch r.State {
	case "", "idle", "idle in transaction", "idle in transaction (aborted)":
		return false
	}
	return true
}

// pushWaitBucket folds one activity snapshot into the Model's ring. Sampling
// is always on (the per-tick cost is one small map over ≤ a few hundred rows)
// so the profile already has history the first time W is pressed. An
// all-idle snapshot still pushes an empty bucket: elapsed window time with
// nothing running is real information, not a gap.
func (m *Model) pushWaitBucket(rows []pg.ActivityRow) {
	if m.waitRing == nil {
		m.waitRing = newWaitRing()
	}
	b := waitBucket{at: time.Now(), counts: make(map[string]int)}
	for _, r := range rows {
		if !waitSampleRow(r) {
			continue
		}
		b.counts[classifyWait(r)]++
		b.total++
	}
	m.waitRing.push(b)
}
