package dashboard

// ClampRefresh clamps the user-supplied refresh interval (seconds) to [5, 300].
func ClampRefresh(seconds int) int {
	if seconds < 5 {
		return 5
	}
	if seconds > 300 {
		return 300
	}
	return seconds
}
