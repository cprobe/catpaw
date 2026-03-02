package diagnose

// SeverityRank returns a numeric rank for severity comparison.
// Higher rank = more severe.
func SeverityRank(status string) int {
	switch status {
	case "Critical":
		return 3
	case "Warning":
		return 2
	case "Info":
		return 1
	default:
		return 0
	}
}
