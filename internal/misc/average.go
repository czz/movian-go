package misc

// Canonical port of src/misc/average.c — a 4-slot per-second moving
// average used for download-rate reporting.

// Average — C: average_t (misc/average.h:26-31).
type Average struct {
	last    int    // C: int last — last sample's epoch second
	lastVal int64  // C: int64_t last_val
	slots   [4]int // C: int slots[4]
	slotptr int    // C: int slotptr
}

// AverageFill — C: average_fill (average.c:26-46). Records a cumulative
// counter reading at the given epoch second; zero-fills skipped seconds.
func (a *Average) AverageFill(now int, value int64) {
	dt := now - a.last
	if dt <= 0 {
		return
	}
	a.last = now

	delta := int(value - a.lastVal)
	a.lastVal = value

	if dt > 3 {
		dt = 3
	}

	for dt > 1 {
		a.slots[a.slotptr] = 0
		a.slotptr = (a.slotptr + 1) & 3
		dt--
	}

	a.slots[a.slotptr] = delta
	a.slotptr = (a.slotptr + 1) & 3
}

// AverageRead — C: average_read (average.c:52-70). Returns the mean of
// the last N slots, where N shrinks with the staleness of the last fill.
func (a *Average) AverageRead(now int) int {
	dt := max(now-a.last-1, 0)

	slots := 4 - dt
	if slots <= 0 {
		return 0
	}

	x := 0
	for i := range slots {
		x += a.slots[(a.slotptr-1-i)&3]
	}
	return x / slots
}
