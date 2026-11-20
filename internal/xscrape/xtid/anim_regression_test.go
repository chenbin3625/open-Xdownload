package xtid

import "testing"

func TestCubicValueTerminatesOnMalformedControlPoints(t *testing.T) {
	// Non-monotonic / extreme control points that previously risked non-convergence.
	c := cubicCurve{curves: []float64{10, -5, -10, 5}}
	_ = c.value(0.5)
}
