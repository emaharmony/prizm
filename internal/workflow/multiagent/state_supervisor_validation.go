package multiagent

import "fmt"

func validateLoopTraversalCounts(counts LoopTraversalCounts, problems *[]string) {
	for kind, value := range counts.Counts {
		if value < 0 {
			*problems = append(*problems, fmt.Sprintf(
				"state loop traversal count for %q must be non-negative", kind))
		}
	}
}
