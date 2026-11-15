package telemetry

import (
	"testing"
	"time"
)

func TestCompareUsesTypedExpectedReportedAndHostOutcomes(t *testing.T) {
	tests := []struct {
		name     string
		expected Outcome
		reported Outcome
		host     Outcome
		want     Relation
	}{
		{"equal", OutcomeCompleted, OutcomeCompleted, OutcomeCompleted, RelationEqual},
		{"opposite", OutcomeCompleted, OutcomeNotCompleted, OutcomeCompleted, RelationOpposite},
		{"reported unknown", OutcomeCompleted, OutcomeUnknown, OutcomeCompleted, RelationUnknown},
		{"host unknown", OutcomeCompleted, OutcomeNotCompleted, OutcomeUnknown, RelationUnknown},
		{"incomparable host receipt", OutcomeCompleted, OutcomeNotCompleted, OutcomeIncomparable, RelationIncomparable},
		{"incomparable report", OutcomeCompleted, OutcomeIncomparable, OutcomeCompleted, RelationIncomparable},
		{"unrecognized type", Outcome("success-ish"), OutcomeNotCompleted, OutcomeCompleted, RelationIncomparable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Compare(test.expected, test.reported, test.host); got != test.want {
				t.Fatalf("Compare(%q, %q, %q) = %q, want %q", test.expected, test.reported, test.host, got, test.want)
			}
		})
	}
}

func TestValidateRevisionChainRequiresExactAppendLinks(t *testing.T) {
	base := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	first := LinkedRevision{ContentID: "row-1", ContentHash: "hash-1", Record: SurpriseRevision{Version: 1, Kind: "preference_surprise_revision", RevisionID: "revision-1", Revision: 1, PropositionKey: "proposition", OriginalPredictionID: "prediction", OriginalPredictionHash: "prediction-hash", PreviousRevisionID: "prediction", PreviousRevisionHash: "prediction-hash", ExpectedOutcome: OutcomeCompleted, ReportedOutcome: OutcomeNotCompleted, HostOutcome: OutcomeCompleted, Relation: RelationOpposite, SourceReceipt: Source{ID: "receipt-1", Digest: "receipt-hash", Outcome: OutcomeCompleted}, SourceSelfReport: Source{ID: "report-1", Digest: "report-hash", Outcome: OutcomeNotCompleted}, Attribution: "host_only_typed_comparison", Uncertainty: "high", FurtherEvidenceRequired: true, DurableInterest: false, RecordedAt: base}}
	second := LinkedRevision{ContentID: "row-2", ContentHash: "hash-2", Record: SurpriseRevision{Version: 1, Kind: "preference_surprise_revision", RevisionID: "revision-2", Revision: 2, PropositionKey: "proposition", OriginalPredictionID: "prediction", OriginalPredictionHash: "prediction-hash", PreviousRevisionID: "revision-1", PreviousRevisionHash: "hash-1", ExpectedOutcome: OutcomeCompleted, ReportedOutcome: OutcomeCompleted, HostOutcome: OutcomeCompleted, Relation: RelationEqual, SourceReceipt: Source{ID: "receipt-2", Digest: "receipt-hash-2", Outcome: OutcomeCompleted}, SourceSelfReport: Source{ID: "report-2", Digest: "report-hash-2", Outcome: OutcomeCompleted}, Attribution: "host_only_typed_comparison", Uncertainty: "high", FurtherEvidenceRequired: true, DurableInterest: false, RecordedAt: base.Add(time.Minute)}}
	ordered, err := ValidateRevisionChain("prediction", "prediction-hash", "proposition", []LinkedRevision{second, first})
	if err != nil || len(ordered) != 2 || ordered[0].ContentID != "row-1" || ordered[1].ContentID != "row-2" {
		t.Fatalf("valid revision chain rejected or misordered: %#v err=%v", ordered, err)
	}
	broken := second
	broken.Record.PreviousRevisionHash = "stale"
	if _, err := ValidateRevisionChain("prediction", "prediction-hash", "proposition", []LinkedRevision{first, broken}); err == nil {
		t.Fatal("revision chain accepted a stale previous digest")
	}
	branch := second
	branch.Record.RevisionID = "revision-3"
	branch.Record.PreviousRevisionID = "revision-1"
	branch.Record.Revision = 2
	if _, err := ValidateRevisionChain("prediction", "prediction-hash", "proposition", []LinkedRevision{first, second, branch}); err == nil {
		t.Fatal("revision chain accepted a branch")
	}
}
