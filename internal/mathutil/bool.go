package mathutil

// BoolToInt converts a bool to int (1 for true, 0 for false).
func BoolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
