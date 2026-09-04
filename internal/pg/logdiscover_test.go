package pg

import (
	"strings"
	"testing"

	"pgdu/internal/pglog"
)

func TestRotationOrdering(t *testing.T) {
	c := []LogCandidate{
		{Info: pglog.SourceInfo{Kind: "gz", Path: "/var/log/postgresql/postgresql-17-main.log.2.gz", Rotated: true}},
		{Info: pglog.SourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log", Current: true}},
		{Info: pglog.SourceInfo{Kind: "local", Path: "/var/log/postgresql/postgresql-17-main.log.1", Rotated: true}},
		{Info: pglog.SourceInfo{Kind: "gz", Path: "/var/log/postgresql/postgresql-17-main.log.10.gz", Rotated: true}},
	}
	sortCandidates(c)
	want := []string{".log", ".log.1", ".log.2.gz", ".log.10.gz"}
	for i, w := range want {
		if !strings.HasSuffix(c[i].Info.Path, w) {
			t.Errorf("position %d: %s, want suffix %s", i, c[i].Info.Path, w)
		}
	}
	if !pglog.IsRotatedName("x.log.3.gz") || pglog.IsRotatedName("x.log") || pglog.RotationIndex("x.log.10.gz") != 10 {
		t.Error("rotation name helpers")
	}
}
