package report

// Seconds the climb spends on one level. A level the gate presses down climbs
// in a straight line to its score before the gate, so the bar and the number
// are always at the same height; one that breaks through eases out.
const (
	pressedSpan  = 1.1
	breakSpan    = 1.5
	firstStart   = 0.4
	betweenStage = 0.9
)

// The bars of a level that breaks through ease out along the CSS curve
// cubic-bezier(.2,.7,.25,1); the script counts the numbers along the same
// curve (ease below mirrors it), so both are at the same height at any time.

// Timing is when the parts of one column animate, in seconds from page load.
type Timing struct {
	Start float64
	// Span is how long the number counts; Fill how long the solid bar rises.
	Span, Fill float64
	// OverStart and OverDur are the dashed part above the gate (pressed only).
	OverStart, OverDur float64
	// Cross is when a level that breaks through passes the gate.
	Cross float64
	// Linear is true when bar and number rise evenly; otherwise both follow easeOut.
	Linear bool
}

// Broke is a level that passed the gate: it has a run, the gate did not press
// it down, and it scored above the gate.
func (s Stage) Broke(gateCap float64) bool {
	return s.Kind == KindRun && !s.Capped && s.Total > gateCap
}

// Timing is the schedule of the column; Start is set by the climb.
func (s Stage) Timing(gateCap float64) Timing {
	if s.Kind != KindRun {
		return Timing{Start: 0.2, Linear: true}
	}
	if s.Capped && s.Before > 0 {
		fill := pressedSpan * s.Total / s.Before
		return Timing{Start: s.Start, Span: pressedSpan, Fill: fill, OverStart: fill, OverDur: pressedSpan - fill, Linear: true}
	}
	t := Timing{Start: s.Start, Span: breakSpan, Fill: breakSpan}
	if s.Total > gateCap && s.Total > 0 {
		t.Cross = breakSpan * invEase(gateCap/s.Total)
	}
	return t
}

// ease evaluates the curve: the share of the height reached at share k of the time.
func ease(k float64) float64 {
	s := solveX(k)
	return bezier(s, 0.7, 1)
}

func bezier(s, c1, c2 float64) float64 {
	u := 1 - s
	return 3*u*u*s*c1 + 3*u*s*s*c2 + s*s*s
}

// solveX finds the curve parameter whose x is k.
func solveX(k float64) float64 {
	lo, hi := 0.0, 1.0
	for range 40 {
		mid := (lo + hi) / 2
		if bezier(mid, 0.2, 0.25) < k {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// invEase is the share of the time at which the curve has reached height h.
func invEase(h float64) float64 {
	lo, hi := 0.0, 1.0
	for range 40 {
		mid := (lo + hi) / 2
		if ease(mid) < h {
			lo = mid
		} else {
			hi = mid
		}
	}
	return (lo + hi) / 2
}

// schedule gives every level with a run its start: one after another.
func schedule(stages []Stage, gateCap float64) {
	at := firstStart
	for i := range stages {
		if stages[i].Kind != KindRun {
			continue
		}
		stages[i].Start = at
		at += stages[i].Timing(gateCap).Span + betweenStage
	}
}
