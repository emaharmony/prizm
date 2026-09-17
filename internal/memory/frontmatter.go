package memory

import (
	"regexp"
	"strings"
	"time"
)

// frontMatterRe matches YAML front matter between --- delimiters at the start of a file.
var frontMatterRe = regexp.MustCompile(`(?s)^---\s*\n(.*?)\n---\s*\n`)

// parseFrontMatter extracts YAML front matter from content and returns
// the parsed metadata and the content without the front matter.
// Front matter is optional — if not present, returns nil metadata and the original content.
func parseFrontMatter(content string) (map[string]string, string) {
	m := frontMatterRe.FindStringSubmatch(content)
	if m == nil {
		return nil, content
	}

	meta := make(map[string]string)
	yaml := m[1]

	for _, line := range strings.Split(yaml, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Parse "key: value" lines
		idx := strings.Index(line, ":")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		// Strip quotes
		val = strings.Trim(val, `"'`)

		// Handle list values like [lumi, origin, name]
		if strings.HasPrefix(val, "[") && strings.HasSuffix(val, "]") {
			val = strings.Trim(val, "[]")
		}

		meta[key] = val
	}

	// Return content without front matter
	remaining := content[len(m[0]):]
	return meta, remaining
}

// applyFrontMatter applies parsed front matter metadata to a Memory entry.
func applyFrontMatter(mem *Memory, meta map[string]string) {
	for key, val := range meta {
		switch strings.ToLower(key) {
		case "id":
			if val != "" {
				mem.ID = val
			}
		case "category":
			mem.Category = val
		case "confidence":
			if mem.Metadata == nil {
				mem.Metadata = make(map[string]string)
			}
			mem.Metadata["confidence"] = val
		case "provenance":
			mem.Source = val
		case "keywords":
			mem.KeyTopics = strings.Split(val, ", ")
			for i, t := range mem.KeyTopics {
				mem.KeyTopics[i] = strings.TrimSpace(t)
			}
		case "superseded_by":
			if mem.Metadata == nil {
				mem.Metadata = make(map[string]string)
			}
			mem.Metadata["superseded_by"] = val
		case "recall_count":
			if mem.Metadata == nil {
				mem.Metadata = make(map[string]string)
			}
			mem.Metadata["recall_count"] = val
		case "last_recalled":
			if mem.Metadata == nil {
				mem.Metadata = make(map[string]string)
			}
			mem.Metadata["last_recalled"] = val
		case "tier":
			mem.Tier = val
		case "created":
			if val != "" {
				for _, fmt := range []string{"2006-01-02", time.RFC3339, "2006-01-02 15:04:05"} {
					if t, err := time.Parse(fmt, val); err == nil {
						mem.CreatedAt = t
						if mem.AccessedAt.IsZero() {
							mem.AccessedAt = t
						}
						break
					}
				}
			}
		}
	}
}

// isSuperseded checks if a memory has been superseded by a newer one.
func isSuperseded(mem Memory) bool {
	if mem.Metadata == nil {
		return false
	}
	superseded, ok := mem.Metadata["superseded_by"]
	return ok && superseded != ""
}

// formatFrontMatter creates YAML front matter from a Memory entry.
func formatFrontMatter(mem Memory) string {
	var sb strings.Builder
	sb.WriteString("---\n")

	if mem.ID != "" {
		sb.WriteString("id: " + mem.ID + "\n")
	}
	if mem.Category != "" {
		sb.WriteString("category: " + mem.Category + "\n")
	}
	if mem.Tier != "" {
		sb.WriteString("tier: " + mem.Tier + "\n")
	}
	if mem.Source != "" {
		sb.WriteString("provenance: " + mem.Source + "\n")
	}
	if len(mem.KeyTopics) > 0 {
		sb.WriteString("keywords: [" + strings.Join(mem.KeyTopics, ", ") + "]\n")
	}
	if mem.Summary != "" {
		summary := strings.ReplaceAll(mem.Summary, `"`, `\"`)
		sb.WriteString("summary: \"" + summary + "\"\n")
	}
	if mem.Metadata != nil {
		if v, ok := mem.Metadata["confidence"]; ok {
			sb.WriteString("confidence: " + v + "\n")
		}
		if v, ok := mem.Metadata["superseded_by"]; ok && v != "" {
			sb.WriteString("superseded_by: " + v + "\n")
		}
		if v, ok := mem.Metadata["recall_count"]; ok {
			sb.WriteString("recall_count: " + v + "\n")
		}
		if v, ok := mem.Metadata["last_recalled"]; ok {
			sb.WriteString("last_recalled: " + v + "\n")
		}
	}
	if !mem.CreatedAt.IsZero() {
		sb.WriteString("created: " + mem.CreatedAt.Format("2006-01-02") + "\n")
	}

	sb.WriteString("---\n")
	return sb.String()
}