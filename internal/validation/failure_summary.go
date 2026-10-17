package validation

import (
	"bytes"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const (
	maxValidationOutputScanBytes = 32 * 1024
	maxValidationSummaryChars    = 512
	maxValidationSummaryLines    = 3
)

var (
	validationDiagnosticPattern = regexp.MustCompile(`(?i)(\b(?:keyerror|assertionerror|typeerror|valueerror|indexerror|attributeerror|importerror|modulenotfounderror|exception|panic|fatal)\b|\b(?:error|failed|failure|fail)\b|^\s*(?:E\s+|FAILED\b|ERROR\b|--- FAIL:))`)
	validationTracebackPattern  = regexp.MustCompile(`(?i)^\s*(?:traceback \(most recent call last\):|file ["']|at .+\(.+\)|\^+\s*$)`)
	validationANSISequence      = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	validationSecretAssignment  = regexp.MustCompile(`(?i)\b(token|password|passwd|secret|api[_-]?key|access[_-]?key|authorization|credential)(\s*[:=]\s*)("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;]+)`)
	validationBearerSecret      = regexp.MustCompile(`(?i)\bBearer\s+[^\s,;]+`)
	validationURLCredentials    = regexp.MustCompile(`(?i)(https?://)[^/@\s:]+:[^/@\s]+@`)
	validationCommonSecret      = regexp.MustCompile(`(?i)\b(?:AKIA[A-Z0-9]{16}|(?:sk|rk|pk)-[A-Za-z0-9_-]{16,}|gh[pousr]_[A-Za-z0-9_]{16,}|github_pat_[A-Za-z0-9_]{16,}|xox[baprs]-[A-Za-z0-9-]{16,})\b`)
	validationPrivateContent    = regexp.MustCompile(`(?i)<\s*/?think\b|\bprompt\b|\bsystem message\b`)
)

// readFailureSummary reads only a bounded tail of each validation artifact and
// returns a small sanitized diagnostic for structured handoff to another role.
func readFailureSummary(stdoutPath, stderrPath string, exitCode int) string {
	if exitCode == 0 {
		return ""
	}
	stdout := readBoundedTail(stdoutPath, maxValidationOutputScanBytes)
	stderr := readBoundedTail(stderrPath, maxValidationOutputScanBytes)
	return summarizeFailureOutput(stdout, stderr, exitCode)
}

func readBoundedTail(path string, limit int64) []byte {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if info.Size() > limit {
		if _, err := file.Seek(-limit, io.SeekEnd); err != nil {
			return nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(file, limit))
	if err != nil {
		return nil
	}
	return data
}

func summarizeFailureOutput(stdout, stderr []byte, exitCode int) string {
	if exitCode == 0 {
		return ""
	}
	var selected []string
	for _, output := range [][]byte{stderr, stdout} {
		lines := bytes.Split(output, []byte{'\n'})
		for i := len(lines) - 1; i >= 0 && len(selected) < maxValidationSummaryLines; i-- {
			line := strings.TrimSpace(string(lines[i]))
			if line == "" || validationTracebackPattern.MatchString(line) || !validationDiagnosticPattern.MatchString(line) {
				continue
			}
			line = sanitizeValidationDiagnostic(line)
			if line != "" {
				selected = append(selected, line)
			}
		}
	}
	if len(selected) == 0 {
		return "Validation command failed with exit code " + strconv.Itoa(exitCode) + "."
	}
	// Present diagnostics in their original order, regardless of which stream
	// supplied them. Each source remains capped before the combined cap.
	for left, right := 0, len(selected)-1; left < right; left, right = left+1, right-1 {
		selected[left], selected[right] = selected[right], selected[left]
	}
	summary := strings.Join(selected, " | ")
	return truncateValidationSummary(summary, maxValidationSummaryChars)
}

func sanitizeValidationDiagnostic(line string) string {
	if validationPrivateContent.MatchString(line) || strings.HasPrefix(strings.TrimSpace(line), "{") || strings.HasPrefix(strings.TrimSpace(line), "[") {
		return ""
	}
	line = validationANSISequence.ReplaceAllString(line, "")
	// Redact bearer credentials before key/value assignments. Otherwise an
	// Authorization assignment consumes only the "Bearer" word and leaves the
	// credential token behind for the structured handoff.
	line = validationBearerSecret.ReplaceAllString(line, "Bearer [REDACTED]")
	line = validationSecretAssignment.ReplaceAllString(line, "$1$2[REDACTED]")
	line = validationURLCredentials.ReplaceAllString(line, "$1[REDACTED]@")
	line = validationCommonSecret.ReplaceAllString(line, "[REDACTED]")
	line = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, line)
	line = strings.Join(strings.Fields(line), " ")
	if len(line) > maxValidationSummaryChars {
		line = truncateValidationSummary(line, maxValidationSummaryChars)
	}
	return line
}

func truncateValidationSummary(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	return string(runes[:max-1]) + "…"
}
