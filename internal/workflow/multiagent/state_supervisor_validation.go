package multiagent

func validateLoopTraversalCounts(counts LoopTraversalCounts, problems *[]string) {
	if counts.TesterToDeveloper < 0 {
		*problems = append(*problems, "state tester-to-developer loop traversals must be non-negative")
	}
	if counts.ReviewerToDeveloper < 0 {
		*problems = append(*problems, "state reviewer-to-developer loop traversals must be non-negative")
	}
}
