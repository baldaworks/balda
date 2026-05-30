package handlers

const defaultGoalMaxIterations = 25

func normalizeGoalMaxIterations(v int) int {
	if v <= 0 {
		return defaultGoalMaxIterations
	}
	return v
}
