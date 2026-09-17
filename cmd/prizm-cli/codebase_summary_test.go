package main

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/codesummary"
)

func TestFormatCodebaseSummaryReplyIncludesArtifactsAndTruncates(t *testing.T) {
	result := codesummary.Result{
		ReportPath:   `.prizm\data\codebase-summary\summary-test\report.md`,
		EvidencePath: `.prizm\data\codebase-summary\summary-test\evidence.json`,
		FilesScanned: 42,
		PackagesSeen: 7,
		DurationMS:   1234,
		Excerpt:      strings.Repeat("a", 1500),
	}

	reply := formatCodebaseSummaryReply("summary-test", result)
	for _, want := range []string{
		"summary-test",
		result.ReportPath,
		result.EvidencePath,
		"Scanned: 42 files, 7 packages in 1234ms.",
		"...",
	} {
		if !strings.Contains(reply, want) {
			t.Fatalf("reply missing %q:\n%s", want, reply)
		}
	}
	if len(reply) > 1700 {
		t.Fatalf("reply too long: %d chars", len(reply))
	}
}
