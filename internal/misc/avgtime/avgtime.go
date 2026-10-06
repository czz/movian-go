// Package avgtime — canonical port of src/misc/avgtime.h.
//
// avgtime_t is a rolling statistics accumulator: a ring buffer of the
// last 10 measured durations (microseconds), a peak, and a moving
// average. avgtime_stop also publishes avg/peak (in milliseconds) to
// props and returns the rolling average in microseconds.
package avgtime

import (
	"github.com/czz/movian-go/internal/arch"
	propcore "github.com/czz/movian-go/internal/prop"
)

// AvgTime — C: avgtime_t (misc/avgtime.h:26-34)
type AvgTime struct {
	Samples [10]int // C: int samples[10]
	Ptr     int     // C: int ptr
	Start   int64   // C: int start — µs timestamp (Go int64: no 32-bit wrap)
	Peak    int     // C: int peak
	Avg     int     // C: int avg
}

// StartTiming — C: avgtime_start
func (a *AvgTime) StartTiming() {
	a.Start = arch.GetTS()
}

// Stop — C: avgtime_stop. Pushes the sample, updates peak and the
// rolling average, publishes avg/peak (in ms) to the given props and
// returns the rolling average in microseconds.
func (a *AvgTime) Stop(avg, peak *propcore.Prop) int {
	now := arch.GetTS()
	d := int(now - a.Start)

	a.Ptr++
	if a.Ptr == 10 {
		a.Ptr = 0
	}

	a.Samples[a.Ptr] = d

	if d > a.Peak {
		a.Peak = d
	}

	sum := 0
	for i := range 10 {
		sum += a.Samples[i]
	}
	a.Avg = sum / 10

	if avg != nil {
		avg.SetInt(a.Avg / 1000)
	}
	if peak != nil {
		peak.SetInt(a.Peak / 1000)
	}

	return a.Avg
}
