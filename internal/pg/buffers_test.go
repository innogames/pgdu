package pg

import "testing"

func TestRollUpBufferStats(t *testing.T) {
	rel := func(tab uint32, name string, kind PartKind, buffered, total, hits, reads, dirty int64, usage float64) TableBufferStat {
		return TableBufferStat{
			DB: "shop", Schema: "app", Name: name, OID: tab,
			BufferedBytes: buffered, TotalBytes: total, Hits: hits, Reads: reads, DirtyBytes: dirty, UsageAvg: usage,
			Rel: &BufferRel{Kind: kind, OID: tab*10 + uint32(kind)},
		}
	}
	got := RollUpBufferStats([]TableBufferStat{
		rel(2, "items", PartHeap, 0, 10, 0, 0, 0, 0),
		rel(1, "orders", PartHeap, 300, 400, 30, 10, 100, 5),
		rel(1, "orders", PartIndex, 100, 200, 10, 0, 0, 1),
		rel(1, "orders", PartToast, 0, 50, 0, 5, 0, 0),
	})
	if len(got) != 2 || got[0].OID != 2 || got[1].OID != 1 {
		t.Fatalf("roll-up = %+v, want items then orders (first-seen order)", got)
	}
	o := got[1]
	if o.Rel != nil || o.Name != "orders" || o.Schema != "app" || o.DB != "shop" {
		t.Errorf("roll-up identity = %+v, want the table with no Rel", o)
	}
	if o.BufferedBytes != 400 || o.TotalBytes != 650 || o.Hits != 40 || o.Reads != 15 || o.DirtyBytes != 100 {
		t.Errorf("roll-up sums = %+v", o)
	}
	// Weighted by buffered bytes: (5·300 + 1·100) / 400 = 4, not the row mean 2.
	if o.UsageAvg != 4 {
		t.Errorf("UsageAvg = %v, want 4", o.UsageAvg)
	}
	if got[0].UsageAvg != 0 {
		t.Errorf("unbuffered table UsageAvg = %v, want 0", got[0].UsageAvg)
	}
}

func TestTableBufferStatKey(t *testing.T) {
	if k := (TableBufferStat{OID: 7}).Key(); k != 7 {
		t.Errorf("table Key = %d, want 7", k)
	}
	if k := (TableBufferStat{OID: 7, Rel: &BufferRel{OID: 9}}).Key(); k != 9 {
		t.Errorf("relation Key = %d, want 9", k)
	}
}
