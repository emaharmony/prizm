package context

import (
	"strings"
)

// Governance markers are text patterns that indicate a document contains
// rules the agent must enforce, not just reference information. When detected,
// the file is tagged as governance and presented differently in the prompt.
//
// This is a copy of the markers in internal/governance/detect.go. The
// duplication is intentional to avoid a circular dependency between
// context and governance packages.
var governanceMarkers = []string{
	"Status: Frozen",
	"Status: Approved",
	"Status: Active",
	"DO NOT MODIFY",
	"DO NOT CHANGE",
	"no schema changes without",
	"requires approval",
	"requires explicit approval",
	"must not be modified",
}

// detectGovernance scans file content for governance markers and returns
// true if the file contains rules the agent must enforce.
func detectGovernance(content string) bool {
	if len(content) == 0 {
		return false
	}
	// Only check the first 500 characters — governance markers are in headers
	header := content
	if len(content) > 500 {
		header = content[:500]
	}
	for _, marker := range governanceMarkers {
		if strings.Contains(header, marker) {
			return true
		}
	}
	return false
}

// DetectGovernance is exported for use by the governance loader package.
func DetectGovernance(content string) bool {
	return detectGovernance(content)
}