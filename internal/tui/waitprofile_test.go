package tui

import (
	"fmt"
	"testing"
	"time"

	"pgdu/internal/pg"
)

func TestWaitRingWraparound(t *testing.T) {
	r := newWaitRing()
	for i := range waitRingCap + 5 {
		r.push(waitBucket{at: time.Unix(int64(i), 0), total: i})
	}
	got := r.ordered()
	if len(got) != waitRingCap {
		t.Fatalf("ordered() returned %d buckets, want %d", len(got), waitRingCap)
	}
	// Oldest entries evicted: the window starts at push #5 and stays in order.
	for i, b := range got {
		if want := i + 5; b.total != want {
			t.Fatalf("bucket %d out of order: total %d, want %d", i, b.total, want)
		}
	}
}

func TestClassifyWait(t *testing.T) {
	if got := classifyWait(pg.ActivityRow{State: "active"}); got != waitCPUClass {
		t.Errorf("active without wait = %q, want %q", got, waitCPUClass)
	}
	row := pg.ActivityRow{State: "active", WaitEventType: "LWLock", WaitEvent: "WALWrite"}
	if got := classifyWait(row); got != "LWLock:WALWrite" {
		t.Errorf("classifyWait = %q, want LWLock:WALWrite", got)
	}
}

func TestWaitSampleRow(t *testing.T) {
	for _, state := range []string{"", "idle", "idle in transaction", "idle in transaction (aborted)"} {
		if waitSampleRow(pg.ActivityRow{State: state}) {
			t.Errorf("state %q should be excluded from sampling", state)
		}
	}
	if !waitSampleRow(pg.ActivityRow{State: "active"}) {
		t.Errorf("active backends must be sampled")
	}
}

func TestPushWaitBucket(t *testing.T) {
	m := &Model{}
	m.pushWaitBucket([]pg.ActivityRow{
		{State: "active"},
		{State: "active", WaitEventType: "IO", WaitEvent: "DataFileRead"},
		{State: "idle"},
	})
	if m.waitRing == nil || m.waitRing.n != 1 {
		t.Fatalf("expected one bucket after first push")
	}
	b := m.waitRing.ordered()[0]
	if b.total != 2 {
		t.Errorf("bucket total = %d, want 2 (idle excluded)", b.total)
	}
	if b.counts[waitCPUClass] != 1 || b.counts["IO:DataFileRead"] != 1 {
		t.Errorf("unexpected counts: %v", b.counts)
	}
}

func TestAggregateWaitClasses(t *testing.T) {
	buckets := []waitBucket{
		{counts: map[string]int{"CPU (running)": 3, "IO:DataFileRead": 1}, total: 4},
		{counts: map[string]int{"IO:DataFileRead": 3, "Lock:tuple": 1}, total: 4},
	}
	classes, total := aggregateWaitClasses(buckets)
	if total != 8 {
		t.Fatalf("total = %d, want 8", total)
	}
	if len(classes) != 3 {
		t.Fatalf("got %d classes, want 3", len(classes))
	}
	// Ranked by count: DataFileRead(4), CPU(3), tuple(1).
	if classes[0].name != "IO:DataFileRead" || classes[0].count != 4 {
		t.Errorf("rank 0 = %s/%d, want IO:DataFileRead/4", classes[0].name, classes[0].count)
	}
	// Series carries per-bucket shares oldest→newest.
	if classes[0].series[0] != 0.25 || classes[0].series[1] != 0.75 {
		t.Errorf("DataFileRead series = %v, want [0.25 0.75]", classes[0].series)
	}
}

func TestAggregateWaitClassesFoldsOther(t *testing.T) {
	counts := make(map[string]int, waitProfileMaxClasses+3)
	for i := range waitProfileMaxClasses + 3 {
		counts[fmt.Sprintf("Lock:class%02d", i)] = 100 - i
	}
	classes, _ := aggregateWaitClasses([]waitBucket{{counts: counts, total: 100}})
	if len(classes) != waitProfileMaxClasses+1 {
		t.Fatalf("got %d classes, want %d + other", len(classes), waitProfileMaxClasses)
	}
	last := classes[len(classes)-1]
	if last.name != "other" {
		t.Fatalf("last class = %q, want other", last.name)
	}
	wantOther := (100 - waitProfileMaxClasses) + (100 - waitProfileMaxClasses - 1) + (100 - waitProfileMaxClasses - 2)
	if last.count != wantOther {
		t.Errorf("other count = %d, want %d", last.count, wantOther)
	}
}
