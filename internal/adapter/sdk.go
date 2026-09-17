// Package adapter provides the Adapter SDK for third-party Prizm adapters.
//
// V23 M4.4: Adapter SDK extensions
//
// This file adds lifecycle management, event bus interface, and manifest
// extensions for building third-party adapters that connect Prizm to
// external systems (Discord, Telegram, HTTP, IoT, etc.).
package adapter

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// LifecycleAdapter extends the basic Adapter interface with lifecycle methods.
// Third-party adapters should implement this for proper start/stop management.
type LifecycleAdapter interface {
	Adapter

	// Init initializes the adapter with the provided configuration.
	// Called once before Start. Use this to set up connections, parse config, etc.
	Init(config map[string]any) error

	// Start begins the adapter's main loop.
	// Called after Init. The adapter should run until ctx is cancelled or Stop is called.
	Start(ctx context.Context) error

	// Stop gracefully shuts down the adapter.
	// Called after Start returns or when the orchestrator shuts down.
	Stop() error
}

// EventBus is the interface adapters use to publish and subscribe to events.
// This decouples adapters from the NATS client library.
type EventBus interface {
	// Publish sends an event to the bus.
	Publish(subject string, data []byte) error

	// Subscribe registers a handler for events matching the subject pattern.
	Subscribe(subject string, handler func(subject string, data []byte)) error

	// Unsubscribe removes a subscription.
	Unsubscribe(subject string) error
}

// SDKManifest extends Manifest with SDK-specific fields for third-party adapters.
// It includes event descriptors, config schema, and dependencies.
type SDKManifest struct {
	// Name is the unique identifier for this adapter.
	Name string `yaml:"name"`

	// Version is the semantic version.
	Version string `yaml:"version"`

	// Description is a human-readable description.
	Description string `yaml:"description"`

	// Author is the creator/maintainer.
	Author string `yaml:"author"`

	// Type is the adapter category (e.g., "chat", "iot", "http", "custom").
	Type string `yaml:"type"`

	// Events lists the event subjects this adapter produces or consumes.
	Events []EventDescriptor `yaml:"events"`

	// Config defines the configuration schema for this adapter.
	Config []ConfigField `yaml:"config"`

	// Dependencies lists other adapters or services this adapter requires.
	Dependencies []string `yaml:"dependencies,omitempty"`
}

// EventDescriptor describes an event that an adapter produces or consumes.
type EventDescriptor struct {
	// Subject is the NATS subject pattern (e.g., "adapter.discord.message.received").
	Subject string `yaml:"subject"`

	// Direction is "in" (adapter consumes) or "out" (adapter produces) or "both".
	Direction string `yaml:"direction"`

	// Description explains what this event does.
	Description string `yaml:"description"`
}

// ConfigField describes a configuration parameter.
type ConfigField struct {
	// Name is the configuration key.
	Name string `yaml:"name"`

	// Type is the value type (string, int, bool, url, path).
	Type string `yaml:"type"`

	// Required indicates whether this field must be provided.
	Required bool `yaml:"required"`

	// Default is the default value if not provided.
	Default string `yaml:"default,omitempty"`

	// Description explains what this config field does.
	Description string `yaml:"description"`
}

// LoadSDKManifest reads and parses a manifest.yaml file.
// The path is cleaned and symlinks are resolved to prevent path traversal.
func LoadSDKManifest(path string) (*SDKManifest, error) {
	// Clean and resolve path to prevent traversal
	cleanPath := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("adapter sdk: resolve path: %w", err)
	}

	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, fmt.Errorf("adapter sdk: read manifest: %w", err)
	}

	var m SDKManifest
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("adapter sdk: parse manifest: %w", err)
	}

	if m.Name == "" {
		return nil, fmt.Errorf("adapter sdk: manifest missing name")
	}
	if m.Version == "" {
		return nil, fmt.Errorf("adapter sdk: manifest missing version")
	}
	if m.Type == "" {
		return nil, fmt.Errorf("adapter sdk: manifest missing type")
	}

	return &m, nil
}

// ValidateConfig checks that all required config fields are present.
func (m *SDKManifest) ValidateConfig(config map[string]any) error {
	if config == nil {
		config = map[string]any{}
	}
	for _, field := range m.Config {
		if field.Required {
			val, exists := config[field.Name]
			if !exists || val == nil {
				return fmt.Errorf("adapter sdk: missing required config field %q", field.Name)
			}
			// Check for empty strings
			if s, ok := val.(string); ok && s == "" {
				return fmt.Errorf("adapter sdk: required config field %q must not be empty", field.Name)
			}
		}
	}
	return nil
}

// HealthStatus represents the health of an adapter.
type HealthStatus struct {
	Name      string    `json:"name"`
	Healthy   bool      `json:"healthy"`
	Message   string    `json:"message,omitempty"`
	StartedAt time.Time `json:"started_at,omitempty"`
}

// LifecycleHealthChecker is an optional interface that lifecycle adapters
// can implement to provide detailed health status.
type LifecycleHealthChecker interface {
	LifecycleAdapter
	HealthStatus() HealthStatus
}
