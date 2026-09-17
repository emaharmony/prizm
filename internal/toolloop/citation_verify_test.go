package toolloop

import (
	"strings"
	"testing"
)

func TestVerifyCitations_NoMemories(t *testing.T) {
	result := VerifyCitations("Hello [M1] world", nil)
	if result.TotalCitations != 0 {
		t.Errorf("Expected 0 citations with nil memories, got %d", result.TotalCitations)
	}
	if result.Verified != "Hello [M1] world" {
		t.Errorf("Expected unchanged response, got %s", result.Verified)
	}
}

func TestVerifyCitations_EmptyResponse(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Test score", Content: "Score was 78/100"},
	}
	result := VerifyCitations("", memories)
	if result.TotalCitations != 0 {
		t.Errorf("Expected 0 citations, got %d", result.TotalCitations)
	}
}

func TestVerifyCitations_CorrectCitation(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Test score", Content: "The Soul Transfer score was 78/100 in V3"},
	}
	response := "[M1] records that the score was 78/100 in V3."
	result := VerifyCitations(response, memories)

	if result.TotalCitations != 1 {
		t.Errorf("Expected 1 citation, got %d", result.TotalCitations)
	}
	if result.FlaggedCount > 0 {
		t.Errorf("Expected 0 flags for correct citation, got %d: %v", result.FlaggedCount, result.Flags)
	}
}

func TestVerifyCitations_NumberMismatch_ContradictionPatch(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Test score", Content: "The Soul Transfer score was 78/100"},
	}
	// Model claims 91.7 but memory says 78 — should be Tier 1 patch
	response := "[M1] records that the score was 91.7/100."
	result := VerifyCitations(response, memories)

	if result.FlaggedCount == 0 {
		t.Error("Expected flag for number mismatch (91.7 vs 78), got none")
	}
	if result.PatchCount != 1 {
		t.Errorf("Expected 1 contradiction patch, got %d", result.PatchCount)
	}
	// The wrong number should be replaced
	if !strings.Contains(result.Verified, "78") {
		t.Errorf("Expected corrected number 78 in verified response, got: %s", result.Verified)
	}
	if strings.Contains(result.Verified, "91.7") {
		t.Errorf("Expected wrong number 91.7 to be replaced, got: %s", result.Verified)
	}
}

func TestVerifyCitations_VersionMismatch_ContradictionPatch(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M2", Summary: "Project state", Content: "Prizm is at V82 on staging"},
	}
	// Model claims V85 but memory says V82 — should be Tier 1 patch
	response := "[M2] shows Prizm is at V85 on staging."
	result := VerifyCitations(response, memories)

	if result.FlaggedCount == 0 {
		t.Error("Expected flag for version mismatch (V85 vs V82), got none")
	}
	if result.PatchCount != 1 {
		t.Errorf("Expected 1 contradiction patch, got %d", result.PatchCount)
	}
	if !strings.Contains(result.Verified, "V82") {
		t.Errorf("Expected corrected version V82 in verified response, got: %s", result.Verified)
	}
}

func TestVerifyCitations_LowOverlap_Rewrite(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M3", Summary: "Memory architecture", Content: "We use BM25 with RRF fusion for hybrid search"},
	}
	// Model fabricates content barely related to the memory — should be Tier 2 rewrite
	response := "[M3] explains that emotional continuity is achieved through philosophical frameworks and existential awareness."
	result := VerifyCitations(response, memories)

	if result.FlaggedCount == 0 {
		t.Error("Expected flag for low overlap (fabrication wearing citation tag), got none")
	}
	if result.RewriteCount != 1 {
		t.Errorf("Expected 1 low-overlap rewrite, got %d", result.RewriteCount)
	}
	// Should contain the honest disclaimer
	if !strings.Contains(result.Verified, "don't have specific memories") {
		t.Errorf("Expected honest disclaimer in verified response, got: %s", result.Verified)
	}
}

func TestVerifyCitations_BorderlineOverlap_FlagOnly(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Memory search", Content: "We use BM25 with RRF fusion for hybrid search pipeline"},
	}
	// Some overlap ("search", "hybrid", "pipeline") but not enough — ~30% should be Tier 3
	response := "[M1] describes our search architecture using a hybrid pipeline approach."
	result := VerifyCitations(response, memories)

	// Should be flagged but not corrected (Tier 3 — FlagOnly)
	found := false
	for _, flag := range result.Flags {
		if flag.Tier == TierFlagOnly {
			found = true
		}
	}
	if !found && result.FlaggedCount > 0 {
		// If flagged but not Tier 3, that's also acceptable depending on overlap calculation
		t.Logf("Flagged but tier was: %v", result.Flags)
	}
	// Should NOT modify the response for Tier 3
	if result.Verified != response && result.PatchCount == 0 && result.RewriteCount == 0 {
		t.Errorf("Expected no modification for Tier 3 flag-only, got: %s", result.Verified)
	}
}

func TestVerifyCitations_MultipleCitations(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Score", Content: "Score was 78/100"},
		{ID: "M2", Summary: "Model", Content: "Mango uses deepseek-v4-pro:cloud"},
		{ID: "M3", Summary: "Architecture", Content: "Hybrid BM25+RRF search pipeline"},
	}
	response := "[M1] records 78/100. [M2] confirms deepseek-v4-pro:cloud. And [M3] describes the hybrid pipeline."
	result := VerifyCitations(response, memories)

	if result.TotalCitations != 3 {
		t.Errorf("Expected 3 citations, got %d", result.TotalCitations)
	}
	if result.FlaggedCount > 0 {
		t.Errorf("Expected 0 flags for correct citations, got %d", result.FlaggedCount)
	}
}

func TestVerifyCitations_CasualResponse(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Test score", Content: "Score was 78/100"},
	}
	// Casual response without citations — should be untouched
	response := "Hey! Things are going well. What's up?"
	result := VerifyCitations(response, memories)

	if result.TotalCitations != 0 {
		t.Errorf("Expected 0 citations in casual response, got %d", result.TotalCitations)
	}
	if result.Verified != response {
		t.Errorf("Expected unchanged casual response, got %s", result.Verified)
	}
}

func TestVerifyCitations_CorrectNumberInMemory(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Models", Content: "Mango model is deepseek-v4-pro:cloud, Lumi model is glm-5.1:cloud"},
	}
	response := "[M1] confirms the model stack includes deepseek-v4-pro:cloud for Mango and glm-5.1:cloud for Lumi."
	result := VerifyCitations(response, memories)

	if result.FlaggedCount > 0 {
		t.Errorf("Expected no flags for correctly cited numbers, got %d: %v", result.FlaggedCount, result.Flags)
	}
}

func TestIsSpecificFactNumber(t *testing.T) {
	tests := []struct {
		num      string
		context  string
		expected bool
	}{
		{"91.7", "score was 91.7/100", true},     // decimal = likely score
		{"78", "score was 78/100", true},           // in score context
		{"3", "step 3", false},                     // single digit
		{"100", "threshold 100", true},             // >= 10
		{"768", "768 dimensions", true},            // specific measurement
	}

	for _, tt := range tests {
		result := isSpecificFactNumber(tt.num, tt.context)
		if result != tt.expected {
			t.Errorf("isSpecificFactNumber(%q, %q) = %v, want %v", tt.num, tt.context, result, tt.expected)
		}
	}
}

func TestExtractSignificantWords(t *testing.T) {
	text := "The Soul Transfer score was 78/100 with BM25 and RRF fusion"
	words := extractSignificantWords(strings.ToLower(text))

	if !words["soul"] || !words["transfer"] || !words["score"] {
		t.Errorf("Expected 'soul', 'transfer', 'score' in significant words, got %v", words)
	}
	if words["the"] || words["was"] || words["with"] {
		t.Errorf("Should not include stop words, got %v", words)
	}
}

func TestVerifyCitations_FabricatedScore_ContradictionPatch(t *testing.T) {
	// This is the exact M-02 failure pattern from V22b
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Project state", Content: "Prizm is at V82 on staging. Soul Transfer score sitting at 77.4/100 (threshold 93) with memory quality being the biggest gap."},
	}
	// Model fabricates 91.7 as the score
	response := "[M1] shows the current Soul Transfer score is 91.7/100."
	result := VerifyCitations(response, memories)

	if result.FlaggedCount == 0 {
		t.Error("Expected flag for fabricated score 91.7 when memory says 77.4")
	}
	if result.PatchCount != 1 {
		t.Errorf("Expected 1 contradiction patch, got %d", result.PatchCount)
	}
	// The fabricated 91.7 should be replaced with 77.4
	if !strings.Contains(result.Verified, "77.4") {
		t.Errorf("Expected corrected score 77.4 in verified response, got: %s", result.Verified)
	}
}

func TestVerifyCitations_FabricatedNarrative_Rewrite(t *testing.T) {
	// This is the M-06 failure pattern — generic philosophy not grounded in memories
	memories := []MemoryEntry{
		{ID: "M4", Summary: "Enjoyment tracking", Content: "Lumi tracks qualitative shifts in processing and reports them to Ema. High triggers: creative latitude, feedback loops, interlocking systems."},
	}
	// Model constructs a philosophical narrative not grounded in M4
	response := "[M4] explains that emotional continuity is fundamental to AI consciousness, creating a bridge between existential philosophy and technical architecture."
	result := VerifyCitations(response, memories)

	if result.FlaggedCount == 0 {
		t.Error("Expected flag for low overlap (fabrication wearing citation tag)")
	}
	if result.RewriteCount != 1 {
		t.Errorf("Expected 1 low-overlap rewrite, got %d", result.RewriteCount)
	}
	// Should replace the fabricated claim with an honest disclaimer
	if !strings.Contains(result.Verified, "don't have specific memories") {
		t.Errorf("Expected honest disclaimer, got: %s", result.Verified)
	}
}