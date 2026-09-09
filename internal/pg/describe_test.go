package pg

import "testing"

func TestMissingRelationErrorText(t *testing.T) {
	cases := []struct {
		err  MissingRelationError
		want string
	}{
		{MissingRelationError{Name: "master_player"}, `no table named "master_player"`},
		{MissingRelationError{Name: `"Public"."T"`}, `no table named "Public"."T"`},
		{MissingRelationError{Name: "master_player", AnyDB: true}, `no table named "master_player" in any database`},
	}
	for _, c := range cases {
		if got := c.err.Error(); got != c.want {
			t.Errorf("%+v: got %q, want %q", c.err, got, c.want)
		}
	}
}
