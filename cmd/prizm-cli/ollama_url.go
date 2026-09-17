package main

import "os"

// resolveOllamaURL picks the Ollama base URL using a single precedence order
// shared by every command: explicit --ollama-url flag, then the
// OLLAMA_BASE_URL environment variable, then prizm.yaml's prizm.ollama_url
// (when the command loads a config), then empty — which lets the provider
// fall back to its default (http://localhost:11434).
func resolveOllamaURL(flagValue, configValue string) string {
	if flagValue != "" {
		return flagValue
	}
	if env := os.Getenv("OLLAMA_BASE_URL"); env != "" {
		return env
	}
	return configValue
}
