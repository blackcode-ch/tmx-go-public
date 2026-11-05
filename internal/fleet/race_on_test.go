//go:build race

package fleet

// raceDetector: the race detector slows code 5-10x, so timing budgets are not checked under it.
const raceDetector = true
