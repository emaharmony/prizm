package toolloop

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// MemoryEntry represents a memory for citation verification.
// This is a minimal interface to avoid importing the memory package.
type MemoryEntry struct {
	ID      string // e.g., "M1"
	Summary string
	Content string
}

// CorrectionTier represents the type of correction applied to a flagged citation.
// Following the Hallucination-Detector and PAVE tiered verification pattern:
// - Tier 1 (ContradictionPatch): Wrong numbers/versions replaced with memory-sourced values
// - Tier 2 (LowOverlapRewrite): Fabricated claims stripped and replaced with honest disclaimer
// - Tier 3 (FlagOnly): Flagged but not corrected (borderline overlap)
type CorrectionTier int

const (
	TierNone              CorrectionTier = iota // Not flagged
	TierContradictionPatch                      // Wrong number/version → replace with correct value
	TierLowOverlapRewrite                       // Fabricated claim → strip and disclaim
	TierFlagOnly                                // Borderline → flag for logging only
)

func (t CorrectionTier) String() string {
	switch t {
	case TierNone:
		return "none"
	case TierContradictionPatch:
		return "contradiction-patch"
	case TierLowOverlapRewrite:
		return "low-overlap-rewrite"
	case TierFlagOnly:
		return "flag-only"
	default:
		return fmt.Sprintf("unknown(%d)", t)
	}
}

// CitationVerification holds the result of verifying citations in a response.
type CitationVerification struct {
	Original       string // Original response
	Verified       string // Verified/corrected response
	Flags          []CitationFlag
	TotalCitations int
	VerifiedCount  int
	FlaggedCount   int
	PatchCount     int // Number of Tier 1 contradiction patches applied
	RewriteCount   int // Number of Tier 2 low-overlap rewrites applied
}

// CitationFlag represents a flagged citation mismatch.
type CitationFlag struct {
	CitationID   string         // e.g., "M3"
	ClaimText    string         // The text attributed to this citation
	MemoryText   string         // The actual memory content
	Issue        string         // Description of the mismatch
	Corrected    bool           // Whether the claim was corrected
	Tier         CorrectionTier // Which correction tier was applied
	WrongNum     string         // The number that was wrong (for TierContradictionPatch)
	CorrectVal   string         // The correct value from memory (for TierContradictionPatch)
}

// citationPattern matches [M1], [M2], etc.
var citationPattern = regexp.MustCompile(`\[M(\d+)\]`)

// numberPattern extracts specific numbers (scores, percentages, versions)
var numberPattern = regexp.MustCompile(`\b\d+[\.\d]*\b`)

// versionPattern extracts version strings like V82, v20, V21
var versionPattern = regexp.MustCompile(`(?i)\bV\d+(\.\d+)?\b`)

// VerifyCitations checks that claims attributed to [Mn] citations are actually
// present in the referenced memory content. It applies tiered corrections:
// - Tier 1: Replace wrong numbers/versions with correct values from memory
// - Tier 2: Strip fabricated claims (low overlap) and insert honest disclaimers
// - Tier 3: Flag borderline cases for logging without modifying the response
//
// This implements the "post-hoc entailment verification" pattern from V23
// research (RAC, PAVE, Hallucination-Detector tiered verification).
func VerifyCitations(response string, memories []MemoryEntry) *CitationVerification {
	if len(memories) == 0 || response == "" {
		return &CitationVerification{
			Original:       response,
			Verified:       response,
			TotalCitations: 0,
			VerifiedCount:  0,
			FlaggedCount:   0,
		}
	}

	// Build memory ID → content map
	memMap := make(map[string]MemoryEntry)
	for _, m := range memories {
		memMap[m.ID] = m
	}

	result := &CitationVerification{
		Original: response,
		Verified: response,
	}

	// Find all citations in the response
	matches := citationPattern.FindAllStringSubmatchIndex(response, -1)
	result.TotalCitations = len(matches)

	verified := response
	flaggedCount := 0
	verifiedCount := 0
	patchCount := 0
	rewriteCount := 0

	// Collect all corrections to apply, then apply in reverse order
	// to preserve string indices
	type correction struct {
		start    int    // Start index in the original response
		end      int    // End index in the original response
		replace  string // Replacement text
		tier     CorrectionTier
	}

	var corrections []correction

	// Process citations in order to collect corrections
	for i := 0; i < len(matches); i++ {
		match := matches[i]
		fullMatchStart := match[0]
		fullMatchEnd := match[1]
		groupStart := match[2]
		groupEnd := match[3]
		memID := "M" + response[groupStart:groupEnd]
		mem, ok := memMap[memID]
		if !ok {
			// Citation references a memory that doesn't exist — leave it
			verifiedCount++
			continue
		}

		// Extract the claim text after this citation
		claimText := extractClaimAfterCitation(response, fullMatchEnd)

		// Check if the claim is consistent with the memory content
		flag := verifyClaimAgainstMemory(memID, claimText, mem)
		if flag != nil {
			flaggedCount++

			switch flag.Tier {
			case TierContradictionPatch:
				// Tier 1: Find and replace the wrong number in the claim area
				// The wrong number is near the citation, within the claim text
				corr := findNumberCorrection(response, fullMatchStart, fullMatchEnd, flag)
				if corr != nil {
					corrections = append(corrections, *corr)
					patchCount++
				}
				flag.Corrected = corr != nil

			case TierLowOverlapRewrite:
				// Tier 2: Replace the entire claim sentence with an honest disclaimer
				claimStart := fullMatchStart
				claimEnd := findClaimEndIndex(response, fullMatchEnd)
				disclaimer := fmt.Sprintf("[%s] I don't have specific memories about this — the claim above wasn't grounded in what I actually know.", memID)
				corrections = append(corrections, correction{
					start:   claimStart,
					end:     claimEnd,
					replace: disclaimer,
					tier:    TierLowOverlapRewrite,
				})
				rewriteCount++
				flag.Corrected = true

			case TierFlagOnly:
				// Tier 3: Flag only, no correction
				flag.Corrected = false
			}

			result.Flags = append(result.Flags, *flag)
		} else {
			verifiedCount++
		}
	}

	// Apply corrections in reverse order to preserve indices
	for i := len(corrections) - 1; i >= 0; i-- {
		c := corrections[i]
		if c.start < len(verified) && c.end <= len(verified) {
			verified = verified[:c.start] + c.replace + verified[c.end:]
		}
	}

	result.Verified = verified
	result.VerifiedCount = verifiedCount
	result.FlaggedCount = flaggedCount
	result.PatchCount = patchCount
	result.RewriteCount = rewriteCount

	return result
}

// findNumberCorrection finds the wrong number near a citation and creates a correction
// that replaces it with the correct value from memory.
func findNumberCorrection(response string, citationStart, citationEnd int, flag *CitationFlag) *struct {
	start   int
	end     int
	replace string
	tier    CorrectionTier
} {
	// Find the wrong number in the response near the citation
	// Look within a reasonable window after the citation
	windowStart := citationStart
	windowEnd := min(len(response), citationEnd+200)
	window := response[windowStart:windowEnd]

	// Find the wrong number in the window
	wrongNum := flag.WrongNum
	if wrongNum == "" {
		return nil
	}

	// Find the number in the window
	idx := strings.Index(window, wrongNum)
	if idx == -1 {
		// Try case-insensitive for version strings
		idx = strings.Index(strings.ToLower(window), strings.ToLower(wrongNum))
		if idx == -1 {
			return nil
		}
	}

	absStart := windowStart + idx
	absEnd := absStart + len(wrongNum)

	return &struct {
		start   int
		end     int
		replace string
		tier    CorrectionTier
	}{
		start:   absStart,
		end:     absEnd,
		replace: flag.CorrectVal,
		tier:    TierContradictionPatch,
	}
}

// applyLowOverlapRewrite replaces the claim text after a citation with an honest disclaimer.
// This is called for Tier 2 corrections where the claim has very low overlap with the memory.
func applyLowOverlapRewrite(response string, citationEnd int, memID string, mem MemoryEntry) string {
	// Find the end of the sentence containing the citation
	sentenceEnd := findClaimEndIndex(response, citationEnd)
	disclaimer := fmt.Sprintf(" [%s] I don't have specific memories about this.", memID)
	return response[:sentenceEnd] + disclaimer + response[sentenceEnd:]
}

// extractClaimAfterCitation extracts text from after the citation to the next
// sentence boundary, next citation, or end of response.
func extractClaimAfterCitation(response string, citationEnd int) string {
	remaining := response[citationEnd:]
	if len(remaining) > 200 {
		remaining = remaining[:200]
	}

	// Find the end of the current claim
	end := len(remaining)
	for i := 0; i < len(remaining) && i < 200; i++ {
		ch := remaining[i]
		if ch == '.' || ch == '!' || ch == '?' || ch == '\n' {
			// Don't split on periods that are part of decimals (e.g., 91.7)
			if ch == '.' && i > 0 && i < len(remaining)-1 {
				prevIsDigit := remaining[i-1] >= '0' && remaining[i-1] <= '9'
				nextIsDigit := remaining[i+1] >= '0' && remaining[i+1] <= '9'
				if prevIsDigit && nextIsDigit {
					continue // This period is part of a decimal number
				}
			}
			end = i + 1
			break
		}
	}

	claim := strings.TrimSpace(remaining[:end])
	return claim
}

// findClaimEndIndex finds the index of the end of the sentence containing the citation.
func findClaimEndIndex(response string, citationEnd int) int {
	remaining := response[citationEnd:]
	for i, ch := range remaining {
		if ch == '.' || ch == '!' || ch == '?' || ch == '\n' {
			return citationEnd + i + 1
		}
		if i > 200 {
			break
		}
	}
	return len(response)
}

// findClaimEnd finds the end of the sentence containing the citation for inserting corrections.
func findClaimEnd(response string, citationEnd int) int {
	return findClaimEndIndex(response, citationEnd)
}

// verifyClaimAgainstMemory checks if a claim attributed to a memory is consistent
// with the actual memory content. Returns a CitationFlag with the appropriate tier.
func verifyClaimAgainstMemory(memID string, claimText string, mem MemoryEntry) *CitationFlag {
	// Combine summary + content for full memory text
	memText := mem.Summary + ". " + mem.Content
	memTextLower := strings.ToLower(memText)
	claimLower := strings.ToLower(claimText)

	// Check 1: Specific number mismatch (Tier 1 — ContradictionPatch)
	claimNumbers := numberPattern.FindAllString(claimText, -1)
	memNumbers := numberPattern.FindAllString(memText, -1)

	for _, num := range claimNumbers {
		// Skip single-digit numbers
		if len(num) == 1 && num[0] >= '0' && num[0] <= '9' {
			continue
		}

		// Check if this number exists in the memory text
		found := false
		for _, memNum := range memNumbers {
			if num == memNum {
				found = true
				break
			}
		}

		if !found && isSpecificFactNumber(num, claimText) {
			// Find the correct number from memory that should replace this one
			// Look for numbers in the memory that are in a similar context
			correctVal := findCorrectNumber(num, claimLower, memTextLower, memNumbers)
			return &CitationFlag{
				CitationID: memID,
				ClaimText:  claimText,
				MemoryText: truncate(memText, 200),
				Issue:      fmt.Sprintf("Number %s in claim not found in memory %s (should be %s)", num, memID, correctVal),
				Tier:       TierContradictionPatch,
				WrongNum:   num,
				CorrectVal: correctVal,
			}
		}
	}

	// Check 2: Version mismatch (Tier 1 — ContradictionPatch)
	claimVersions := versionPattern.FindAllString(claimText, -1)
	for _, v := range claimVersions {
		vUpper := strings.ToUpper(v)
		if !strings.Contains(memTextLower, strings.ToLower(vUpper)) {
			// Find the correct version from memory
			memVersions := versionPattern.FindAllString(memText, -1)
			correctVersion := ""
			if len(memVersions) > 0 {
				correctVersion = memVersions[len(memVersions)-1] // Use latest version
			}
			return &CitationFlag{
				CitationID: memID,
				ClaimText:  claimText,
				MemoryText: truncate(memText, 200),
				Issue:      fmt.Sprintf("Version %s in claim not found in memory %s (should be %s)", v, memID, correctVersion),
				Tier:       TierContradictionPatch,
				WrongNum:   v,
				CorrectVal: correctVersion,
			}
		}
	}

	// Check 3: Keyword overlap (Tier 2 — LowOverlapRewrite if <20%, Tier 3 — FlagOnly if 20-40%)
	claimWords := extractSignificantWords(claimLower)
	memWords := extractSignificantWords(memTextLower)

	if len(claimWords) > 3 {
		overlap := 0
		for w := range claimWords {
			if memWords[w] {
				overlap++
			}
		}
		overlapRatio := float64(overlap) / float64(len(claimWords))

		if overlapRatio < 0.2 {
			// Tier 2: Low overlap — likely fabrication wearing a citation tag
			return &CitationFlag{
				CitationID: memID,
				ClaimText:  claimText,
				MemoryText: truncate(memText, 200),
				Issue:      fmt.Sprintf("Low overlap (%.0f%%) between claim and memory %s — likely fabrication", overlapRatio*100, memID),
				Tier:       TierLowOverlapRewrite,
			}
		} else if overlapRatio < 0.4 {
			// Tier 3: Borderline — flag for logging but don't correct
			return &CitationFlag{
				CitationID: memID,
				ClaimText:  claimText,
				MemoryText: truncate(memText, 200),
				Issue:      fmt.Sprintf("Borderline overlap (%.0f%%) between claim and memory %s", overlapRatio*100, memID),
				Tier:       TierFlagOnly,
			}
		}
	}

	return nil
}

// findCorrectNumber tries to find the correct number from memory that corresponds
// to the wrong number in the claim. It looks for numbers in similar context.
func findCorrectNumber(wrongNum string, claimLower string, memLower string, memNumbers []string) string {
	// If the claim has a "/100" pattern and the memory has a different score, use that
	if strings.Contains(claimLower, "/100") || strings.Contains(claimLower, "score") {
		// Look for scores in memory (X/100 pattern or just numbers near "score")
		for _, num := range memNumbers {
			if strings.Contains(memLower, num+"/100") || strings.Contains(memLower, "score") && strings.Contains(memLower, num) {
				if num != wrongNum && isSpecificFactNumber(num, memLower) {
					return num
				}
			}
		}
	}

	// For version strings, find the latest version in memory
	if strings.HasPrefix(strings.ToUpper(wrongNum), "V") {
		for _, num := range memNumbers {
			if strings.HasPrefix(strings.ToUpper(num), "V") && num != wrongNum {
				return num
			}
		}
	}

	// Fallback: return the most specific number from memory that isn't the wrong one
	for _, num := range memNumbers {
		if num != wrongNum && isSpecificFactNumber(num, memLower) {
			return num
		}
	}

	return ""
}

// isSpecificFactNumber checks if a number looks like a specific fact (score, version, measurement)
// rather than a common number.
func isSpecificFactNumber(num string, context string) bool {
	// Skip single-digit numbers unless they're clearly scores
	if len(num) == 1 {
		return false
	}

	// Numbers with decimals are likely scores/measurements
	if strings.Contains(num, ".") {
		return true
	}

	// Numbers in context like "X/100" are scores
	if strings.Contains(context, num+"/100") || strings.Contains(context, num+"/10") {
		return true
	}

	// Numbers >= 10 are likely specific facts
	n := 0
	fmt.Sscanf(num, "%d", &n)
	return n >= 10
}

// extractSignificantWords extracts content words (not stop words) from text.
func extractSignificantWords(text string) map[string]bool {
	stopWords := map[string]bool{
		"a": true, "an": true, "the": true, "is": true, "are": true,
		"was": true, "were": true, "be": true, "been": true, "being": true,
		"have": true, "has": true, "had": true, "do": true, "does": true,
		"did": true, "will": true, "would": true, "could": true, "should": true,
		"may": true, "might": true, "can": true, "shall": true, "must": true,
		"to": true, "of": true, "in": true, "for": true, "on": true,
		"with": true, "at": true, "by": true, "from": true, "as": true,
		"into": true, "through": true, "during": true, "before": true, "after": true,
		"above": true, "below": true, "between": true, "under": true, "over": true,
		"and": true, "or": true, "but": true, "not": true, "no": true,
		"this": true, "that": true, "these": true, "those": true,
		"it": true, "its": true, "i": true, "me": true, "my": true,
		"we": true, "our": true, "you": true, "your": true,
		"he": true, "she": true, "him": true, "her": true, "his": true,
		"they": true, "them": true, "their": true,
		"what": true, "which": true, "who": true, "whom": true,
		"so": true, "if": true, "than": true, "then": true,
		"also": true, "just": true, "very": true, "even": true,
		"about": true, "up": true, "out": true, "how": true, "all": true,
		"each": true, "every": true, "both": true, "few": true, "more": true,
		"most": true, "other": true, "some": true, "such": true, "only": true,
		"own": true, "same": true, "when": true, "where": true, "here": true,
		"there": true, "because": true, "since": true, "while": true,
	}

	words := map[string]bool{}
	for _, word := range strings.Fields(text) {
		clean := strings.TrimFunc(word, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		clean = strings.ToLower(clean)
		if len(clean) > 2 && !stopWords[clean] {
			words[clean] = true
		}
	}
	return words
}

// truncate truncates a string to maxLen characters.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}