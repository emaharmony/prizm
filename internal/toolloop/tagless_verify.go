package toolloop

import (
	"fmt"
	"regexp"
	"strings"
)

// TaglessClaim represents a factual claim extracted from a response without citation tags.
type TaglessClaim struct {
	Text      string // The claim text
	Start     int    // Start index in the original response
	End       int    // End index in the original response
	ClaimType string // "number", "version", "date", "identity", "attribution", "factual"
}

// TaglessVerification holds the result of verifying all claims (cited and uncited)
// against injected memories.
type TaglessVerification struct {
	Original      string
	Verified      string
	Claims        []TaglessClaim
	Flags         []CitationFlag
	PatchCount    int
	RewriteCount  int
	FlagCount     int
	VerifiedCount int // Claims that matched memory
}

// Domain keywords that indicate a claim is in a memory-covered domain.
// Claims about these topics should be verifiable against memories.
var memoryDomainKeywords = map[string]bool{
	// Identity
	"lumi": true, "mango": true, "kirbii": true, "ema": true, "navii": true,
	"name": true, "personality": true, "origin": true, "luminescent": true,
	// Projects
	"prizm": true, "prism": true, "bassbook": true, "openclaw": true,
	"soul": true, "transfer": true, "convergence": true, "migration": true,
	// Technical
	"model": true, "score": true, "ollama": true, "deepseek": true, "glm": true,
	"embedding": true, "memory": true, "bm25": true, "nomic": true,
	"version": true, "commit": true, "branch": true, "staging": true,
	// Scores and metrics
	"test": true, "judge": true, "threshold": true,
}

// claimPatterns matches different types of factual claims.
var (
	// Specific numbers: scores, percentages, token counts, etc.
	specificNumberPattern = regexp.MustCompile(`\b(\d+\.?\d*)\s*(?:/100|/10|%|tokens?|score|points?)\b`)

	// Standalone specific numbers (decimal or >= 10) in domain context
	standaloneNumberPattern = regexp.MustCompile(`\b(\d+\.\d+)\b`)

	// Version strings: V82, v0.2.0, etc.
	taglessVersionPattern = regexp.MustCompile(`(?i)\bV\d+(?:\.\d+)*(?:[a-z]?)\b`)

	// Dates: "April 2026", "September 9th", "March 15th"
	taglessDatePattern = regexp.MustCompile(`\b(?:January|February|March|April|May|June|July|August|September|October|November|December)\s+\d{1,2}(?:st|nd|rd|th)?,?\s*\d{4}\b`)

	// Attribution phrases: "we decided", "my records show", "I recall"
	taglessAttributionPattern = regexp.MustCompile(`(?i)\b(?:we decided|records show|I recall|my memory|according to|I remember|I chose|was chosen|I was named|I named)\b`)

	// Identity claims: "I am X", "my name is X", "I'm X"
	taglessIdentityPattern = regexp.MustCompile(`(?i)\b(?:I am|my name is|I'm|I was named|I chose the name)\b`)

	// Superlatives/absolutes that often indicate fabrication
	taglessAbsolutePattern = regexp.MustCompile(`(?i)\b(?:always|never|only|first|last|every|all|nothing|everything|entire|completely|totally|absolutely)\b`)
)

// VerifyTaglessClaims extracts ALL factual claims from a response (not just cited ones)
// and verifies each against the injected memories. This catches fabrication that
// skips citation tags entirely — the core M-06 failure mode.
//
// Flow:
// 1. Extract all factual claims (numbers, versions, dates, identity, attribution)
// 2. Skip claims that are already covered by citation verification (near [Mn] tags)
// 3. For each uncited claim, check if it's in a memory-covered domain
// 4. Verify claim against memory content
// 5. Apply tiered corrections
func VerifyTaglessClaims(response string, memories []MemoryEntry) *TaglessVerification {
	if len(memories) == 0 || response == "" {
		return &TaglessVerification{
			Original: response,
			Verified: response,
		}
	}

	// Build combined memory text for verification
	memTexts := make([]string, 0, len(memories))
	memContent := ""
	for _, m := range memories {
		text := m.Summary + ". " + m.Content
		memTexts = append(memTexts, text)
		memContent += strings.ToLower(text) + " "
	}

	result := &TaglessVerification{
		Original: response,
		Verified: response,
	}

	// Find all [Mn] citation positions so we can skip claims near them
	citationPositions := citationPattern.FindAllStringSubmatchIndex(response, -1)
	citationRanges := make([][2]int, len(citationPositions))
	for i, m := range citationPositions {
		citationRanges[i] = [2]int{m[0], m[1]}
	}

	// Extract all tagless claims
	claims := extractTaglessClaims(response)

	// Verify each claim against memories
	type correction struct {
		start   int
		end     int
		replace string
		tier    CorrectionTier
	}
	var corrections []correction
	verifiedCount := 0

	for _, claim := range claims {
		// Skip claims that are near a citation tag (already verified by V23b)
		if isNearCitation(claim.Start, claim.End, citationRanges) {
			verifiedCount++
			continue
		}

		// Only verify claims in memory-covered domains
		if !isInMemoryDomain(claim.Text, memContent) {
			verifiedCount++
			continue
		}

		// Verify this claim against all memories
		flag := verifyTaglessClaimAgainstMemories(claim, memTexts, memContent)
		if flag != nil {
			result.Flags = append(result.Flags, *flag)

			switch flag.Tier {
			case TierContradictionPatch:
				// Find and replace the wrong number/version
				corr := findTaglessNumberCorrection(claim, flag)
				if corr != nil {
					corrections = append(corrections, *corr)
					result.PatchCount++
				}
			case TierLowOverlapRewrite:
				// Strip fabricated claim, insert honest disclaimer
				disclaimer := "I don't have specific memories about that."
				corrections = append(corrections, correction{
					start:   claim.Start,
					end:     claim.End,
					replace: disclaimer,
					tier:    TierLowOverlapRewrite,
				})
				result.RewriteCount++
			case TierFlagOnly:
				result.FlagCount++
			}
		} else {
			verifiedCount++
		}
	}

	// Apply corrections in reverse order to preserve indices
	verified := response
	for i := len(corrections) - 1; i >= 0; i-- {
		c := corrections[i]
		if c.start < len(verified) && c.end <= len(verified) {
			verified = verified[:c.start] + c.replace + verified[c.end:]
		}
	}

	result.Verified = verified
	result.Claims = claims
	result.VerifiedCount = verifiedCount

	return result
}

// extractTaglessClaims extracts all factual claims from a response.
func extractTaglessClaims(response string) []TaglessClaim {
	var claims []TaglessClaim

	// 1. Specific numbers in domain context (scores, percentages, etc.)
	numberMatches := specificNumberPattern.FindAllStringSubmatchIndex(response, -1)
	for _, m := range numberMatches {
		start, end := m[0], m[1]
		// Expand to sentence boundaries
		sStart, sEnd := expandToSentence(response, start, end)
		claims = append(claims, TaglessClaim{
			Text:      response[sStart:sEnd],
			Start:     sStart,
			End:      sEnd,
			ClaimType: "number",
		})
	}

	// 1b. Standalone decimal numbers (likely scores/measurements)
	standaloneMatches := standaloneNumberPattern.FindAllStringSubmatchIndex(response, -1)
	for _, m := range standaloneMatches {
		start, end := m[0], m[1]
		// Skip if already captured by specificNumberPattern
		alreadyCaptured := false
		for _, existing := range claims {
			if start >= existing.Start && end <= existing.End {
				alreadyCaptured = true
				break
			}
		}
		if alreadyCaptured {
			continue
		}
		sStart, sEnd := expandToSentence(response, start, end)
		claims = append(claims, TaglessClaim{
			Text:      response[sStart:sEnd],
			Start:     sStart,
			End:      sEnd,
			ClaimType: "number",
		})
	}

	// 2. Version strings
	versionMatches := taglessVersionPattern.FindAllStringSubmatchIndex(response, -1)
	for _, m := range versionMatches {
		start, end := m[0], m[1]
		sStart, sEnd := expandToSentence(response, start, end)
		claims = append(claims, TaglessClaim{
			Text:      response[sStart:sEnd],
			Start:     sStart,
			End:      sEnd,
			ClaimType: "version",
		})
	}

	// 3. Dates
	dateMatches := taglessDatePattern.FindAllStringSubmatchIndex(response, -1)
	for _, m := range dateMatches {
		start, end := m[0], m[1]
		sStart, sEnd := expandToSentence(response, start, end)
		claims = append(claims, TaglessClaim{
			Text:      response[sStart:sEnd],
			Start:     sStart,
			End:      sEnd,
			ClaimType: "date",
		})
	}

	// 4. Identity claims (I am X, my name is X)
	identityMatches := taglessIdentityPattern.FindAllStringSubmatchIndex(response, -1)
	for _, m := range identityMatches {
		start, end := m[0], m[1]
		sStart, sEnd := expandToSentence(response, start, end)
		claims = append(claims, TaglessClaim{
			Text:      response[sStart:sEnd],
			Start:     sStart,
			End:      sEnd,
			ClaimType: "identity",
		})
	}

	// 5. Attribution phrases
	attribMatches := taglessAttributionPattern.FindAllStringSubmatchIndex(response, -1)
	for _, m := range attribMatches {
		start, end := m[0], m[1]
		sStart, sEnd := expandToSentence(response, start, end)
		claims = append(claims, TaglessClaim{
			Text:      response[sStart:sEnd],
			Start:     sStart,
			End:      sEnd,
			ClaimType: "attribution",
		})
	}

	// Deduplicate overlapping claims (keep longest span)
	claims = deduplicateClaims(claims)

	return claims
}

// expandToSentence expands a match to the nearest sentence boundaries.
func expandToSentence(response string, start, end int) (int, int) {
	// Expand backwards to start of sentence
	sStart := start
	for i := start - 1; i >= 0; i-- {
		ch := response[i]
		if ch == '.' || ch == '!' || ch == '?' || ch == '\n' {
			sStart = i + 1
			break
		}
		if i == 0 {
			sStart = 0
		}
	}

	// Expand forwards to end of sentence
	sEnd := end
	for i := end; i < len(response); i++ {
		ch := response[i]
		if ch == '.' || ch == '!' || ch == '?' || ch == '\n' {
			sEnd = i + 1
			break
		}
		if i == len(response)-1 {
			sEnd = len(response)
		}
	}

	// Trim whitespace
	for sStart < end && (response[sStart] == ' ' || response[sStart] == '\t') {
		sStart++
	}

	return sStart, sEnd
}

// deduplicateClaims removes overlapping claims, keeping the longest span.
func deduplicateClaims(claims []TaglessClaim) []TaglessClaim {
	if len(claims) <= 1 {
		return claims
	}

	// Sort by start position
	sorted := make([]TaglessClaim, len(claims))
	copy(sorted, claims)
	for i := 0; i < len(sorted)-1; i++ {
		for j := i + 1; j < len(sorted); j++ {
			if sorted[j].Start < sorted[i].Start {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Merge overlapping claims, keeping longest
	var result []TaglessClaim
	current := sorted[0]
	for i := 1; i < len(sorted); i++ {
		if sorted[i].Start <= current.End {
			// Overlapping — keep the longer span
			if sorted[i].End-sorted[i].Start > current.End-current.Start {
				current = sorted[i]
			}
		} else {
			result = append(result, current)
			current = sorted[i]
		}
	}
	result = append(result, current)

	return result
}

// isNearCitation checks if a claim overlaps with or is adjacent to a citation tag.
// Claims near [Mn] tags are already verified by V23b citation verification.
func isNearCitation(claimStart, claimEnd int, citationRanges [][2]int) bool {
	window := 50 // characters
	for _, r := range citationRanges {
		if claimStart >= r[0]-window && claimStart <= r[1]+window {
			return true
		}
		if claimEnd >= r[0]-window && claimEnd <= r[1]+window {
			return true
		}
	}
	return false
}

// isInMemoryDomain checks if a claim touches topics that memories cover.
func isInMemoryDomain(claimText string, memContent string) bool {
	claimLower := strings.ToLower(claimText)

	// Check if any domain keyword appears in the claim
	for kw := range memoryDomainKeywords {
		if strings.Contains(claimLower, kw) {
			return true
		}
	}

	// Check if any significant word from the claim appears in memory content
	claimWords := extractSignificantWords(claimLower)
	memWords := extractSignificantWords(memContent)
	overlap := 0
	for w := range claimWords {
		if memWords[w] {
			overlap++
		}
	}

	// If at least 30% of significant claim words appear in memories, it's in-domain
	if len(claimWords) > 0 && float64(overlap)/float64(len(claimWords)) >= 0.3 {
		return true
	}

	return false
}

// verifyTaglessClaimAgainstMemories checks a tagless claim against all memories.
func verifyTaglessClaimAgainstMemories(claim TaglessClaim, memTexts []string, memContentLower string) *CitationFlag {
	claimLower := strings.ToLower(claim.Text)

	// Check for number mismatches (same logic as V23b but without citation IDs)
	claimNumbers := numberPattern.FindAllString(claim.Text, -1)
	for _, num := range claimNumbers {
		if len(num) == 1 {
			continue // Skip single digits
		}

		// Check if this number exists in any memory
		found := false
		var correctNum string
		for _, memText := range memTexts {
			memNums := numberPattern.FindAllString(memText, -1)
			for _, mn := range memNums {
				if num == mn {
					found = true
					break
				}
				// Track potential correct numbers
				if correctNum == "" && isSpecificFactNumber(mn, strings.ToLower(memText)) {
					correctNum = mn
				}
			}
			if found {
				break
			}
		}

		if !found && isSpecificFactNumber(num, claimLower) {
			// This specific number is in the claim but NOT in any memory — fabrication
			return &CitationFlag{
				CitationID: "tagless",
				ClaimText:  claim.Text,
				MemoryText: "none",
				Issue:      fmt.Sprintf("Number %s in claim not found in any memory (likely fabrication)", num),
				Tier:       TierContradictionPatch,
				WrongNum:   num,
				CorrectVal: correctNum,
			}
		}
	}

	// Check for version mismatches
	claimVersions := taglessVersionPattern.FindAllString(claim.Text, -1)
	for _, v := range claimVersions {
		vUpper := strings.ToUpper(v)
		found := false
		for _, memText := range memTexts {
			if strings.Contains(strings.ToLower(memText), strings.ToLower(vUpper)) {
				found = true
				break
			}
		}
		if !found {
			// Version in claim not in any memory
			return &CitationFlag{
				CitationID: "tagless",
				ClaimText:  claim.Text,
				MemoryText: "none",
				Issue:      fmt.Sprintf("Version %s in claim not found in any memory", v),
				Tier:       TierContradictionPatch,
				WrongNum:   v,
				CorrectVal: "",
			}
		}
	}

	// Check overall keyword overlap with memories
	claimWords := extractSignificantWords(claimLower)
	memWords := extractSignificantWords(memContentLower)

	if len(claimWords) > 4 { // Only check longer claims
		overlap := 0
		for w := range claimWords {
			if memWords[w] {
				overlap++
			}
		}
		overlapRatio := float64(overlap) / float64(len(claimWords))

		if overlapRatio < 0.15 {
			// Very low overlap with any memory — likely fabrication
			return &CitationFlag{
				CitationID: "tagless",
				ClaimText:   claim.Text,
				MemoryText:  "(combined memories)",
				Issue:        fmt.Sprintf("Very low overlap (%.0f%%) — claim likely fabricated from parametric knowledge", overlapRatio*100),
				Tier:         TierLowOverlapRewrite,
			}
		} else if overlapRatio < 0.25 {
			// Borderline — flag but don't correct
			return &CitationFlag{
				CitationID: "tagless",
				ClaimText:   claim.Text,
				MemoryText:  "(combined memories)",
				Issue:        fmt.Sprintf("Borderline overlap (%.0f%%) — claim may be partially fabricated", overlapRatio*100),
				Tier:         TierFlagOnly,
			}
		}
	}

	return nil // Claim seems consistent with memories
}

// findTaglessNumberCorrection creates a correction for a wrong number in a tagless claim.
func findTaglessNumberCorrection(claim TaglessClaim, flag *CitationFlag) *struct {
	start   int
	end     int
	replace string
	tier    CorrectionTier
} {
	if flag.WrongNum == "" {
		return nil
	}

	// Find the wrong number in the claim text
	idx := strings.Index(claim.Text, flag.WrongNum)
	if idx == -1 {
		return nil
	}

	// Map back to response indices
	absStart := claim.Start + idx
	absEnd := absStart + len(flag.WrongNum)

	replace := flag.CorrectVal
	if replace == "" {
		// No correct value found — strip the number entirely and add disclaimer
		replace = "a different value than what I'm stating"
	}

	return &struct {
		start   int
		end     int
		replace string
		tier    CorrectionTier
	}{
		start:   absStart,
		end:     absEnd,
		replace: replace,
		tier:    TierContradictionPatch,
	}
}