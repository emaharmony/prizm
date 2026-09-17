package context

import (
	"os"
	"path/filepath"
	"strings"
)

// DiscoverFiles searches docs/ for files matching keywords from a task description.
// Returns ranked file paths sorted by relevance (hit count descending).
func DiscoverFiles(workspaceRoot, task string) []string {
	keywords := extractKeywords(task)
	docsDir := filepath.Join(workspaceRoot, "docs")

	// Check if docs/ exists
	if _, err := os.Stat(docsDir); os.IsNotExist(err) {
		return nil
	}

	type fileScore struct {
		path  string
		score int
	}

	var scored []fileScore

	// Walk docs/ directory
	filepath.Walk(docsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		content := strings.ToLower(string(data))
		filename := strings.ToLower(filepath.Base(path))

		score := 0
		for _, kw := range keywords {
			lkw := strings.ToLower(kw)
			// Filename match counts more
			if strings.Contains(filename, lkw) {
				score += 3
			}
			// Content match
			score += strings.Count(content, lkw)
		}

		if score > 0 {
			scored = append(scored, fileScore{path: path, score: score})
		}
		return nil
	})

	// Sort by score descending
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].score > scored[j-1].score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}

	var result []string
	for _, s := range scored {
		result = append(result, s.path)
	}
	return result
}

// extractKeywords splits a task description into search keywords.
// Removes common English stopwords.
func extractKeywords(task string) []string {
	stopwords := map[string]bool{
		"the": true, "a": true, "an": true, "and": true, "or": true, "but": true,
		"in": true, "on": true, "at": true, "to": true, "for": true, "of": true,
		"with": true, "by": true, "from": true, "is": true, "it": true, "that": true,
		"this": true, "was": true, "are": true, "be": true, "has": true, "had": true,
		"have": true, "will": true, "would": true, "could": true, "should": true,
		"can": true, "do": true, "did": true, "not": true, "no": true, "fix": true,
		"implement": true, "create": true, "remove": true, "delete": true,
	}

	words := strings.Fields(strings.ToLower(task))
	var keywords []string
	for _, w := range words {
		w = strings.Trim(w, ".,;:!?()[]{}\"'")
		if len(w) > 2 && !stopwords[w] {
			keywords = append(keywords, w)
		}
	}
	return keywords
}