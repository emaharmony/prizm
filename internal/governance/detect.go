package governance

import (
	"strings"
)

// detectGovernance is the governance package's own copy of the detection
// function. This avoids a circular dependency on internal/context/ while
// keeping the same detection logic.
//
// Exported so context.DetectGovernance can delegate to it if needed.

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

// DetectGovernance scans file content for governance markers.
func DetectGovernance(content string) bool {
	if len(content) == 0 {
		return false
	}
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