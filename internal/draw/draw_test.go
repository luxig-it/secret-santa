package draw

import (
	"errors"
	"math/rand/v2"
	"testing"
)

func testRand(seed uint64) *rand.Rand {
	var s [32]byte
	for i := range s {
		s[i] = byte(seed >> (uint(i%8) * 8))
	}
	return rand.New(rand.NewChaCha8(s))
}

// checkCycle asserts the result is one closed cycle covering every id exactly
// once, which also proves it is a permutation and that nobody drew themselves.
func checkCycle(t *testing.T, ids []int64, got map[int64]int64) {
	t.Helper()
	if len(got) != len(ids) {
		t.Fatalf("got %d assignments, want %d", len(got), len(ids))
	}
	receivers := map[int64]int{}
	for giver, receiver := range got {
		if giver == receiver {
			t.Errorf("participant %d drew themselves", giver)
		}
		receivers[receiver]++
	}
	for _, id := range ids {
		if receivers[id] != 1 {
			t.Errorf("participant %d is gifted %d times, want exactly 1", id, receivers[id])
		}
	}

	seen := map[int64]bool{}
	cur := ids[0]
	for range ids {
		if seen[cur] {
			t.Fatalf("cycle closed early at %d", cur)
		}
		seen[cur] = true
		cur = got[cur]
	}
	if cur != ids[0] {
		t.Errorf("cycle ends at %d, want back at %d", cur, ids[0])
	}
}

func TestAssignPlain(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	got, err := AssignWith(testRand(1), ids, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checkCycle(t, ids, got)
}

func TestAssignRespectsExclusions(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6}
	ex := []Pair{{A: 1, B: 2}, {A: 3, B: 4}}
	for seed := range uint64(50) {
		got, err := AssignWith(testRand(seed), ids, ex)
		if err != nil {
			t.Fatalf("seed %d: unexpected error: %v", seed, err)
		}
		checkCycle(t, ids, got)
		for _, p := range ex {
			if got[p.A] == p.B {
				t.Errorf("seed %d: %d drew excluded partner %d", seed, p.A, p.B)
			}
			if got[p.B] == p.A {
				t.Errorf("seed %d: %d drew excluded partner %d", seed, p.B, p.A)
			}
		}
	}
}

func TestAssignTwoPeople(t *testing.T) {
	ids := []int64{1, 2}
	got, err := AssignWith(testRand(7), ids, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checkCycle(t, ids, got)
}

func TestAssignImpossible(t *testing.T) {
	cases := map[string]struct {
		ids []int64
		ex  []Pair
	}{
		"two people who may not draw each other": {
			ids: []int64{1, 2},
			ex:  []Pair{{A: 1, B: 2}},
		},
		"one person excluded from everyone else": {
			ids: []int64{1, 2, 3},
			ex:  []Pair{{A: 1, B: 2}, {A: 1, B: 3}},
		},
	}
	for name, c := range cases {
		if _, err := AssignWith(testRand(3), c.ids, c.ex); !errors.Is(err, ErrNoAssignment) {
			t.Errorf("%s: got %v, want ErrNoAssignment", name, err)
		}
	}
}

func TestAssignTooFew(t *testing.T) {
	if _, err := AssignWith(testRand(1), []int64{1}, nil); !errors.Is(err, ErrTooFew) {
		t.Errorf("got %v, want ErrTooFew", err)
	}
}

func TestAssignRejectsDuplicates(t *testing.T) {
	if _, err := AssignWith(testRand(1), []int64{1, 2, 2}, nil); err == nil {
		t.Error("expected an error for a duplicated participant")
	}
}

func TestAssignIgnoresStaleExclusions(t *testing.T) {
	ids := []int64{1, 2, 3}
	// Participant 99 was removed from the campaign; the exclusion is moot.
	got, err := AssignWith(testRand(1), ids, []Pair{{A: 1, B: 99}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checkCycle(t, ids, got)
}

func TestAssignVariesBetweenDraws(t *testing.T) {
	ids := []int64{1, 2, 3, 4, 5, 6}
	seen := map[int64]bool{}
	for seed := range uint64(40) {
		got, err := AssignWith(testRand(seed), ids, nil)
		if err != nil {
			t.Fatalf("seed %d: %v", seed, err)
		}
		seen[got[1]] = true
	}
	if len(seen) < 2 {
		t.Errorf("participant 1 always drew the same person across 40 draws: %v", seen)
	}
}
