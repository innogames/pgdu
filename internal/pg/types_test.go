package pg

import "testing"

func TestPartKindString(t *testing.T) {
	cases := []struct {
		k    PartKind
		want string
	}{
		{PartHeap, "heap"},
		{PartToast, "toast"},
		{PartIndex, "index"},
		{PartKind(99), "?"},
	}
	for _, c := range cases {
		if got := c.k.String(); got != c.want {
			t.Errorf("PartKind(%d).String() = %q, want %q", c.k, got, c.want)
		}
	}
}

func TestTableQualified(t *testing.T) {
	tab := Table{Schema: "public", Name: "users"}
	if got := tab.Qualified(); got != "public.users" {
		t.Errorf("Qualified() = %q, want %q", got, "public.users")
	}
}

func TestQueryStatBlocksPerRow(t *testing.T) {
	cases := []struct {
		name            string
		hit, read, rows int64
		want            float64
		ok              bool
	}{
		{"ten blocks per row", 900, 100, 100, 10, true},
		{"no rows", 900, 100, 0, 0, false},
		{"no blocks", 0, 0, 5, 0, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			q := QueryStat{SharedBlksHit: c.hit, SharedBlksRead: c.read, Rows: c.rows}
			got, ok := q.BlocksPerRow()
			if ok != c.ok || got != c.want {
				t.Errorf("BlocksPerRow() = (%v, %v), want (%v, %v)", got, ok, c.want, c.ok)
			}
		})
	}
}

func TestTableBufferStatHitRatio(t *testing.T) {
	cases := []struct {
		name        string
		hits, reads int64
		want        float64
	}{
		{"all hits", 100, 0, 1},
		{"half", 50, 50, 0.5},
		{"all reads", 0, 10, 0},
		{"no io reported", 0, 0, -1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := TableBufferStat{Hits: c.hits, Reads: c.reads}
			if got := s.HitRatio(); got != c.want {
				t.Errorf("HitRatio() = %v, want %v", got, c.want)
			}
		})
	}
}
