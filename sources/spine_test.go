package sources

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
)

type fakeSpine struct {
	out  string
	err  error
	args []string
}

func (f *fakeSpine) RunSpine(_ context.Context, args ...string) (string, error) {
	f.args = args
	return f.out, f.err
}

func fixtureSpine(t *testing.T) SpineStatus {
	b, err := os.ReadFile("../testdata/spine/bearings.json")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeSpine{out: string(b)}
	st := GetSpineStatus(context.Background(), f)
	if !reflect.DeepEqual(f.args, []string{"bearings", "--json"}) {
		t.Fatalf("args = %v", f.args)
	}
	return st
}

func TestSpineFixtureParsesAndCounts(t *testing.T) {
	st := fixtureSpine(t)
	if !st.Readable() {
		t.Fatalf("unreadable: %v", st.Err)
	}
	s := st.Snapshot
	if len(s.NeedsYou) != 1 || s.NeedsYou[0].Repo != "shop" || s.NeedsYou[0].Choices[2] != "later" {
		t.Errorf("needs_you = %+v", s.NeedsYou)
	}
	if len(s.Underway) != 2 || s.Underway[0].Now == "" || s.Underway[0].OutcomeCount != 5 {
		t.Errorf("underway = %+v", s.Underway)
	}
	if len(s.ChartedNext) != 1 || len(s.Landed) != 2 || s.Landed[1].URL == "" || len(s.Errors) != 1 {
		t.Errorf("sections wrong: %+v", s)
	}
	if st.NeedsYouCount() != 1 || st.UnderwayGoalCount() != 1 || st.UnderwayAgentCount() != 2 {
		t.Errorf("counts: %d %d %d", st.NeedsYouCount(), st.UnderwayGoalCount(), st.UnderwayAgentCount())
	}
	if st.UnderwaySpent() != 12.4 || st.UnderwayCap() != 30 {
		t.Errorf("spend %v/%v", st.UnderwaySpent(), st.UnderwayCap())
	}
}

func TestSpineFailuresAreUnreadableWithReason(t *testing.T) {
	cases := map[string]struct {
		f    *fakeSpine
		want string
	}{
		"not found": {&fakeSpine{err: ErrSpineNotFound}, "not found"},
		"exit":      {&fakeSpine{err: errors.New("no fleet here\nmore")}, "no fleet here"},
		"bad json":  {&fakeSpine{out: "not json"}, "not valid JSON"},
		"timeout":   {&fakeSpine{err: context.DeadlineExceeded}, "timed out"},
	}
	for name, c := range cases {
		st := GetSpineStatus(context.Background(), c.f)
		if st.Readable() || st.Err == nil || !strings.Contains(st.Err.Error(), c.want) {
			t.Errorf("%s: %+v", name, st)
		}
		if st.NeedsYouCount() != 0 || st.UnderwayAgentCount() != 0 {
			t.Errorf("%s: unreadable status has counts", name)
		}
	}
	if st := GetSpineStatus(context.Background(), nil); st.Readable() {
		t.Error("nil runner readable")
	}
}

func TestParseSpineSnapshotRules(t *testing.T) {
	if _, err := ParseSpineSnapshot(`{"needs_you":42}`); err == nil {
		t.Error("needs_you:42 should not parse")
	}
	if _, err := ParseSpineSnapshot("nope"); err == nil || !strings.Contains(err.Error(), "not valid JSON") {
		t.Errorf("err = %v", err)
	}
	if _, err := ParseSpineSnapshot(strings.Repeat(" ", spineOutputLimit+1)); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("err = %v", err)
	}
}
