package context

// NamedSources maps short context names to workspace files.
// These are the canonical context sources that can be listed in an agent's
// `context:` array in prizm.yaml. Each name maps to a single file in the
// workspace root.
var NamedSources = map[string]string{
	"soul":           "SOUL.md",
	"agents":         "AGENTS.md",
	"user":           "USER.md",
	"heartbeat":      "HEARTBEAT.md",
	"memory":         "MEMORY.md",
	"identity":       "IDENTITY.md",
	"snippy":         "SNIPPY.md",
	"correspondence": "CORRESPONDENCE.md",
}

// SourcePriority controls truncation order when the token budget is exceeded.
// Lower priority = truncated first. Soul (100) is never truncated.
var SourcePriority = map[string]int{
	"soul":           100, // Never truncate
	"agents":         80,
	"user":           80,
	"heartbeat":      50,
	"memory":         50,
	"snippy":         60,
	"correspondence": 70,
}

// Directory constants for workspace context loading.
const (
	memoryDir        = "memory"        // memory/*.md — session history and decisions
	correspondenceDir = "correspondence" // correspondence/*.md — inter-agent letters
)

// AvailableNamedContexts returns the list of available context source names.
func AvailableNamedContexts() []string {
	names := make([]string, 0, len(NamedSources))
	for name := range NamedSources {
		names = append(names, name)
	}
	return names
}