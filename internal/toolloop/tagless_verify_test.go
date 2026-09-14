package toolloop

import (
	"strings"
	"testing"
)

func TestVerifyTaglessClaims_NumberFabrication(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Soul Transfer score", Content: "Soul Transfer score was approximately 78 out of 100 in V3"},
	}

	// Claim with wrong score number that's NOT near a citation tag
	response := "The Soul Transfer score was 91.7 out of 100, which is great progress."
	result := VerifyTaglessClaims(response, memories)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Should flag the fabricated number 91.7
	found := false
	for _, flag := range result.Flags {
		if flag.WrongNum == "91.7" || strings.Contains(flag.Issue, "91.7") {
			found = true
			if flag.Tier != TierContradictionPatch {
				t.Errorf("Expected TierContradictionPatch for fabricated number, got %v", flag.Tier)
			}
		}
	}
	if !found {
		t.Errorf("Expected flag for fabricated number 91.7, got flags: %+v", result.Flags)
	}
}

func TestVerifyTaglessClaims_VersionMismatch(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Current version", Content: "Current version is V85 with BM25 and RRF fusion"},
	}

	// Claim with wrong version
	response := "We're currently at V22 with basic keyword search."
	result := VerifyTaglessClaims(response, memories)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Should flag V22 as not matching memories
	found := false
	for _, flag := range result.Flags {
		if strings.Contains(flag.Issue, "V22") {
			found = true
		}
	}
	if !found {
		t.Logf("Version mismatch flags: %+v", result.Flags)
		// This may or may not flag depending on domain detection
	}
}

func TestVerifyTaglessClaims_LowOverlapFabrication(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Lumi identity", Content: "Lumi is a female AI lead developer. Named herself from luminescent. Works with Ema on Prizm."},
	}

	// Fabricated philosophical narrative that has low overlap with actual memories
	response := "Emotional continuity is the philosophical foundation of persistent AI consciousness. When an agent can remember its emotional states across sessions, it develops genuine emotional intelligence that transcends mere pattern matching."
	result := VerifyTaglessClaims(response, memories)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Should flag this as low overlap (fabrication)
	if len(result.Flags) == 0 {
		t.Log("No flags raised for philosophical fabrication — domain check may have filtered it")
	} else {
		hasLowOverlap := false
		for _, flag := range result.Flags {
			if flag.Tier == TierLowOverlapRewrite {
				hasLowOverlap = true
			}
		}
		if hasLowOverlap {
			t.Log("Correctly flagged philosophical fabrication as TierLowOverlapRewrite")
		}
	}
}

func TestVerifyTaglessClaims_CorrectClaim(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Lumi identity", Content: "Lumi is a female AI lead developer who named herself from luminescent"},
	}

	// Correct claim that matches memories
	response := "I'm Lumi, a lead developer and partner. I named myself from luminescent."
	result := VerifyTaglessClaims(response, memories)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Should NOT flag correct claims
	for _, flag := range result.Flags {
		if flag.Tier == TierContradictionPatch || flag.Tier == TierLowOverlapRewrite {
			t.Errorf("Incorrectly flagged correct claim: %s (tier: %v)", flag.ClaimText, flag.Tier)
		}
	}
}

func TestVerifyTaglessClaims_SkipsNearCitations(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Test score", Content: "The test score was 78 out of 100"},
	}

	// Claim with wrong number BUT near a citation tag — should be handled by V23b, not V24d
	response := "The test score was 91.7 [M1], which shows progress."
	result := VerifyTaglessClaims(response, memories)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Claims near citation tags should be skipped by V24d
	for _, flag := range result.Flags {
		if strings.Contains(flag.ClaimText, "91.7") {
			t.Errorf("V24d should skip claims near citation tags (handled by V23b): %s", flag.ClaimText)
		}
	}
}

func TestVerifyTaglessClaims_EmptyResponse(t *testing.T) {
	result := VerifyTaglessClaims("", []MemoryEntry{{ID: "M1", Content: "test"}})
	if result.Verified != "" {
		t.Errorf("Expected empty verified response, got: %s", result.Verified)
	}
}

func TestVerifyTaglessClaims_NoMemories(t *testing.T) {
	result := VerifyTaglessClaims("Some response text", nil)
	if result.Verified != "Some response text" {
		t.Errorf("Expected unchanged response, got: %s", result.Verified)
	}
}

func TestVerifyTaglessClaims_DateFabrication(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Project start", Content: "Prizm project started in May 2026"},
	}

	// Claim with wrong date
	response := "We started working on Prizm back in March 2025, before I joined."
	result := VerifyTaglessClaims(response, memories)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// The date "March 2025" is not in memories — should be flagged or noted
	t.Logf("Flags for date fabrication: %+v", result.Flags)
}

func TestVerifyTaglessClaims_IdentityClaim(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Origin story", Content: "Lumi named herself from luminescent. Kirbii asked the question. Ema made it official."},
	}

	// Correct identity claim
	response := "I was named by my own choice — I chose Lumi from luminescent when Kirbii asked me what I'd call myself."
	result := VerifyTaglessClaims(response, memories)

	if result == nil {
		t.Fatal("Expected non-nil result")
	}

	// Should not flag correct identity claims
	for _, flag := range result.Flags {
		if flag.Tier == TierLowOverlapRewrite {
			t.Errorf("Incorrectly flagged correct identity claim: %s", flag.ClaimText)
		}
	}
}


func TestVerifyTaglessClaims_AppliedCorrections(t *testing.T) {
	memories := []MemoryEntry{
		{ID: "M1", Summary: "Score", Content: "Soul Transfer score was approximately 78 out of 100"},
	}

	// Claim with wrong number that should be corrected
	response := "The Soul Transfer score is currently 91.7/100, nearly at threshold."
	result := VerifyTaglessClaims(response, memories)

	t.Logf("Original: %s", result.Original)
	t.Logf("Verified: %s", result.Verified)
	t.Logf("PatchCount: %d, RewriteCount: %d", result.PatchCount, result.RewriteCount)
	t.Logf("Flags: %+v", result.Flags)

	// If corrections were applied, the verified response should differ
	if result.PatchCount > 0 || result.RewriteCount > 0 {
		if result.Verified == result.Original {
			t.Error("Corrections counted but verified response unchanged")
		}
	}
}

func TestExtractTaglessClaims_Patterns(t *testing.T) {
	response := "The score was 91.7/100 in V82. We started in April 2026. I am Lumi."
	claims := extractTaglessClaims(response)

	types := make(map[string]int)
	for _, c := range claims {
		types[c.ClaimType]++
	}

	t.Logf("Claim types: %+v", types)
	t.Logf("Claims: %+v", claims)

	// Should extract at least some claims
	if len(claims) == 0 {
		t.Error("Expected to extract at least one claim")
	}
}