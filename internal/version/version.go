// Package version provides Prizm's semantic release version.
package version

// Version is the semantic version for development builds. Release builds
// override it with -ldflags "-X github.com/emaharmony/prizm/internal/version.Version=<tag>".
var Version = "0.2.0-preview.1"

// String returns the user-facing CLI version.
func String() string {
	return "prizm v" + Version
}
