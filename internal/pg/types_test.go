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
