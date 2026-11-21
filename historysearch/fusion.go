package historysearch

import (
	"sort"
	"time"
)

// rrfK is the constant of reciprocal rank fusion: a document at rank r of
// one list scores 1/(rrfK+r). 60 is the value of the original paper; it keeps
// a first place in one list from outweighing good places in several.
const rrfK = 60

// timeWeight is the weight of the time hint's own ranking. A time named in a
// question is one more vote, worth half a wording: it lifts what falls in the
// window and removes nothing.
const timeWeight = 0.5

// fuse merges ranked id lists with reciprocal rank fusion. Each list is in
// rank order, best first; weights, when given, scale each list's votes. The
// result is every id that appears in any list, best first; ties keep the
// order in which ids were first seen.
func fuse(lists [][]string, weights []float64) []scored {
	scores := map[string]float64{}
	var order []string
	for i, list := range lists {
		weight := 1.0
		if i < len(weights) {
			weight = weights[i]
		}
		for rank, id := range list {
			if _, seen := scores[id]; !seen {
				order = append(order, id)
			}
			scores[id] += weight / float64(rrfK+rank+1)
		}
	}
	out := make([]scored, len(order))
	for i, id := range order {
		out[i] = scored{id: id, score: scores[id]}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].score > out[j].score })
	return out
}

type scored struct {
	id    string
	score float64
}

// TimeHint is a time a question names. It never filters: documents inside
// [From, To] gain a vote, those closest to the middle the most.
type TimeHint struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
}

// Valid reports whether the hint names a window.
func (h TimeHint) Valid() bool { return !h.From.IsZero() && !h.To.IsZero() && !h.To.Before(h.From) }

// Contains reports whether t falls in the window.
func (h TimeHint) Contains(t time.Time) bool {
	return h.Valid() && !t.Before(h.From) && !t.After(h.To)
}

// timeRanking ranks the candidates inside the hint's window by their
// distance from its middle. Candidates outside it are not in the list, and so
// keep the score their wordings gave them.
func timeRanking(hint TimeHint, ids []string, times map[string]time.Time) []string {
	if !hint.Valid() {
		return nil
	}
	middle := hint.From.Add(hint.To.Sub(hint.From) / 2)
	var inside []string
	for _, id := range ids {
		if hint.Contains(times[id]) {
			inside = append(inside, id)
		}
	}
	sort.SliceStable(inside, func(i, j int) bool {
		return absDuration(times[inside[i]].Sub(middle)) < absDuration(times[inside[j]].Sub(middle))
	})
	return inside
}

func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
