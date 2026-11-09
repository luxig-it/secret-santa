// Package draw turns a list of participants into a Secret Santa assignment.
//
// The result is always a single closed cycle: everybody gives to exactly one
// person, receives from exactly one person, and nobody draws themselves.
// Building one cycle rather than an arbitrary permutation also rules out
// mutual pairs, where two people only ever gift each other and the rest of
// the group never enters into it.
package draw

import (
	crand "crypto/rand"
	"errors"
	"fmt"
	"math/rand/v2"
)

// ErrNoAssignment means no valid cycle exists for these exclusions, or the
// search gave up looking. Both cases call for the same fix: drop an exclusion
// or add a participant.
var ErrNoAssignment = errors.New("no valid assignment for these exclusions")

// ErrTooFew means there is nothing to draw.
var ErrTooFew = errors.New("a draw needs at least two participants")

// maxSteps caps the backtracking search. With few exclusions the graph is
// nearly complete and a cycle is found within the first few hundred steps;
// the cap only guards against a pathological exclusion set.
const maxSteps = 500_000

// Pair is an unordered pair of participants who must not draw each other.
type Pair struct {
	A, B int64
}

// Assign produces an assignment keyed by giver, seeded from the system CSPRNG.
func Assign(ids []int64, exclusions []Pair) (map[int64]int64, error) {
	var seed [32]byte
	if _, err := crand.Read(seed[:]); err != nil {
		return nil, fmt.Errorf("seed draw: %w", err)
	}
	return AssignWith(rand.New(rand.NewChaCha8(seed)), ids, exclusions)
}

// AssignWith is Assign with a caller supplied source, which is what the tests
// use to get reproducible draws.
func AssignWith(r *rand.Rand, ids []int64, exclusions []Pair) (map[int64]int64, error) {
	n := len(ids)
	if n < 2 {
		return nil, ErrTooFew
	}

	index := make(map[int64]int, n)
	for i, id := range ids {
		if _, dup := index[id]; dup {
			return nil, fmt.Errorf("participant %d listed twice", id)
		}
		index[id] = i
	}

	// blocked[i][j] reports that i must not give to j.
	blocked := make([][]bool, n)
	for i := range blocked {
		blocked[i] = make([]bool, n)
		blocked[i][i] = true
	}
	for _, p := range exclusions {
		i, iOK := index[p.A]
		j, jOK := index[p.B]
		// Exclusions naming someone who is no longer in the campaign are
		// simply irrelevant rather than an error.
		if !iOK || !jOK {
			continue
		}
		blocked[i][j] = true
		blocked[j][i] = true
	}

	// Any rotation of a cycle is the same cycle, so fixing the first element
	// costs no generality and cuts the search space by a factor of n.
	path := make([]int, 1, n)
	used := make([]bool, n)
	used[0] = true
	steps := 0

	var extend func() bool
	extend = func() bool {
		if steps++; steps > maxSteps {
			return false
		}
		last := path[len(path)-1]
		if len(path) == n {
			return !blocked[last][0]
		}
		candidates := make([]int, 0, n-len(path))
		for j := range n {
			if !used[j] && !blocked[last][j] {
				candidates = append(candidates, j)
			}
		}
		r.Shuffle(len(candidates), func(a, b int) {
			candidates[a], candidates[b] = candidates[b], candidates[a]
		})
		for _, j := range candidates {
			used[j] = true
			path = append(path, j)
			if extend() {
				return true
			}
			path = path[:len(path)-1]
			used[j] = false
		}
		return false
	}

	if !extend() {
		return nil, ErrNoAssignment
	}

	out := make(map[int64]int64, n)
	for i, pos := range path {
		next := path[(i+1)%n]
		out[ids[pos]] = ids[next]
	}
	return out, nil
}
