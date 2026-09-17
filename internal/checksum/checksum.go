// Package checksum provides SHA-256 checksum helpers for Prizm artifacts.
//
// V14d adds artifact integrity verification. Every file Prizm writes gets a
// sidecar checksum file. On read, Prizm verifies the checksum matches. This
// prevents silent corruption and detects tampering.
//
// Checksums are stored as sidecar files: if the artifact is `output.md`, the
// checksum is `output.md.sha256`. This makes them human-readable, git-friendly,
// and independent of any database.
package checksum

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// ErrChecksumMismatch is returned when a file's checksum doesn't match the
// recorded checksum.
var ErrChecksumMismatch = errors.New("checksum: mismatch")

// ComputeChecksum calculates the SHA-256 checksum of the given data.
// Returns the checksum as a hex-encoded string.
func ComputeChecksum(data []byte) string {
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:])
}

// VerifyChecksum checks that data matches the expected checksum.
// Returns true if the checksum matches, false otherwise.
func VerifyChecksum(data []byte, expected string) bool {
	return ComputeChecksum(data) == expected
}

// WriteWithChecksum writes data to a file and records its SHA-256 checksum
// in a sidecar file (path + ".sha256"). The sidecar contains just the
// hex-encoded checksum string followed by two spaces and the filename,
// matching the sha256sum output format for compatibility.
func WriteWithChecksum(path string, data []byte) error {
	// Ensure parent directory exists
	dir := filepath.Dir(path)
	if dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("checksum: create parent dir: %w", err)
		}
	}

	// Write the data file
	if err := os.WriteFile(path, data, 0644); err != nil {
		return fmt.Errorf("checksum: write file: %w", err)
	}

	// Compute and write the checksum sidecar
	checksum := ComputeChecksum(data)
	sidecar := path + ".sha256"
	content := fmt.Sprintf("%s  %s\n", checksum, filepath.Base(path))
	if err := os.WriteFile(sidecar, []byte(content), 0644); err != nil {
		return fmt.Errorf("checksum: write sidecar: %w", err)
	}

	return nil
}

// ReadWithChecksum reads a file and verifies its checksum against the sidecar.
// Returns ErrChecksumMismatch if the checksum doesn't match.
// If no sidecar exists, it returns the data without verification (no error).
func ReadWithChecksum(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("checksum: read file: %w", err)
	}

	sidecar := path + ".sha256"
	sidecarData, err := os.ReadFile(sidecar)
	if err != nil {
		if os.IsNotExist(err) {
			// No sidecar — return data without verification
			return data, nil
		}
		return nil, fmt.Errorf("checksum: read sidecar: %w", err)
	}

	// Parse the sidecar (format: "checksum  filename\n")
	expected := parseSidecar(sidecarData)
	if expected == "" {
		// Malformed sidecar — return data without verification
		return data, nil
	}

	if !VerifyChecksum(data, expected) {
		return data, ErrChecksumMismatch
	}

	return data, nil
}

// parseSidecar extracts the checksum from a sha256sum-format sidecar file.
// Format: "checksum  filename\n"
func parseSidecar(data []byte) string {
	// Find the two-space separator
	for i := 0; i < len(data)-1; i++ {
		if data[i] == ' ' && data[i+1] == ' ' {
			return string(data[:i])
		}
	}
	// Fallback: first line is the checksum
	for i, b := range data {
		if b == '\n' || b == ' ' {
			return string(data[:i])
		}
	}
	return string(data)
}
