# V9 — Adapter Contract System Design

## Mission

Make external integrations first-class while keeping Prizm Core generic.

## Problem

Right now, Prizm has no formal way to connect to external domains. The AI-Hedge-Prizm fork exists because trading gates couldn't plug into Prizm cleanly — they had to be bolted on. V7's `dispatch.run` step type references an `adapter` field, but there's nothing to dispatch *to*. V8's policy engine can deny `dispatch.run` with `context.mode: live`, but it can't actually *run* a dispatch.

V9 formalizes how adapters plug into Prizm. After V9, "trading adapter" is a config file and an interface implementation, not a fork.

## Core Insight

Adapters are the **only** way external domain logic enters Prizm. Tools are internal. Gates are internal. Workflows are internal. Adapters are the seam between Prizm Core and the outside world.

## Design Decisions (Post-Review)

### D1: Adapter interface = 5 methods, no more
Name, Version, Capabilities, Execute, Health. That's the contract. Input validation is an optional interface (`InputValidator`) — adapters opt in, the core contract stays small.

### D2: Result has no Decision field
Gate-like decisions go in `Output["decision"]`, not a top-level field. An echo adapter shouldn't have a Decision field.

### D3: Workflow steps have an explicit `action` field
`dispatch.run` steps now take `action: evaluate` explicitly, not buried in input. This makes workflows readable and policy-targetable.

### D4: Policy is authoritative over Capability.RequiresApproval
Capability.RequiresApproval is advisory — it tells the system "this action typically needs approval." Policy gets final say. If policy says allowed, it's allowed regardless of what the capability declares.

### D5: Adapter names: alphanumeric + hyphens, no dots
Prevents ambiguity in the `adapter.<name>.<action>` policy action format.

### D6: Manifests are optional
`Registry.Register()` accepts an Adapter interface directly. `Registry.RegisterFromManifest()` for YAML-based loading. Built-ins register programmatically; domain adapters can use manifests for discovery.

### D7: Policy evaluation happens in the dispatch handler
The workflow runner's `StepHandlers` now includes a `PolicyEvaluateFunc` for dispatch steps, mirroring the tool executor's `PolicyEvaluatorFunc`. Policy isn't just a CLI concern — it's enforced at the runtime level.

## What V9 Adds

### 1. Adapter Interface (`internal/adapter/adapter.go`)

```go
package adapter

// Adapter is the contract that all domain adapters must implement.
// Adapters connect Prizm to external systems (trading, publishing, deployment, etc.)
// Adapter names must be alphanumeric + hyphens only (no dots).
type Adapter interface {
    Name() string
    Version() string
    Capabilities() []Capability
    Execute(ctx context.Context, action string, input map[string]any) (*Result, error)
    Health(ctx context.Context) (*HealthResult, error)
}

// InputValidator is an optional interface adapters can implement for input validation.
// If implemented, the registry calls ValidateInput before Execute.
type InputValidator interface {
    ValidateInput(action string, input map[string]any) error
}

// Capability describes what an adapter can do.
type Capability struct {
    Action             string         // e.g., "evaluate", "execute", "publish"
    Description        string
    InputSchema        map[string]any // JSON Schema for input (documentation/metadata)
    OutputSchema       map[string]any // JSON Schema for output (documentation/metadata)
    RequiresApproval   bool           // Advisory hint — policy is authoritative
}

// Result is the output of an adapter execution.
type Result struct {
    Success  bool           `json:"success"`
    Output   map[string]any `json:"output,omitempty"`
    Error    string         `json:"error,omitempty"`
    Metadata map[string]any `json:"metadata,omitempty"`
}

// HealthResult reports adapter readiness.
type HealthResult struct {
    Ready   bool           `json:"ready"`
    Message string         `json:"message,omitempty"`
    Details map[string]any `json:"details,omitempty"`
}
```

### 2. Adapter Registry (`internal/adapter/registry.go`)

```go
type Registry struct {
    adapters map[string]Adapter
}

func (r *Registry) Register(a Adapter) error            // Programmatic registration
func (r *Registry) Resolve(name string) (Adapter, error)  // Lookup by name
func (r *Registry) List() []string                       // All registered names
func (r *Registry) Capabilities(name string) ([]Capability, error)
func (r *Registry) ValidateName(name string) error        // Enforce no-dots rule
```

### 3. Adapter Manifest (`internal/adapter/manifest.go`)

```go
type Manifest struct {
    Name         string       `yaml:"name"`
    Version      string       `yaml:"version"`
    Description  string       `yaml:"description"`
    Capabilities []Capability `yaml:"capabilities"`
}

func LoadManifestFromYAML(data []byte) (*Manifest, error)
func LoadManifestFromDir(dir string) ([]Manifest, error)
```

Manifests are optional. Use them for declarative discovery. Register adapters programmatically for built-ins and tests.

### 4. Adapter Events (`internal/adapter/events.go`)

```go
const (
    EventTypeAdapterRegistered = "prizm.adapter.registered"
    EventTypeAdapterHealth     = "prizm.adapter.health"
    EventTypeAdapterExecute   = "prizm.adapter.execute"
    EventTypeAdapterSuccess   = "prizm.adapter.success"
    EventTypeAdapterFailed    = "prizm.adapter.failed"
)
```

### 5. Policy Integration

Policy action format: `adapter.<name>.<action>`

```yaml
policies:
  - id: allow_trading_evaluate
    match:
      action: adapter.trading.evaluate
    decision: allowed
    reason: "Trading gate evaluation is permitted."

  - id: deny_live_execution
    match:
      action: adapter.trading.execute
      context.mode: live
    decision: denied
    reason: "Live trading execution is not supported."
    severity: critical
```

### 6. Workflow Integration

The `StepHandlers` struct in V7 gets a `PolicyEvaluateFunc` for dispatch steps:

```go
type StepHandlers struct {
    ToolExecute    ToolExecuteFunc
    GateEvaluate   GateEvaluateFunc
    DispatchRun    DispatchRunFunc
}
```

The dispatch handler in the CLI resolves the adapter from the registry and evaluates policy before execution.

### 7. CLI Commands

```bash
./prizm adapter list
./prizm adapter show <name>
./prizm adapter health <name>
```

### 8. Echo Adapter (Built-in)

Minimal built-in adapter for testing and demos. Echoes input back.

### 9. Demo Workflow

```yaml
name: demo.adapter_echo
description: Demo workflow that uses the echo adapter
version: 1
steps:
  - id: echo
    type: dispatch.run
    adapter: echo
    action: echo
    input:
      message: "hello from adapter"
```

## What V9 Does NOT Include

- Plugin marketplace or remote loading
- Untrusted third-party plugin execution
- Dynamic code loading
- Complex permissions UI
- Domain-specific adapters (trading, Roblox, etc.) — those live in their own repos
- Hot-reloading of adapters

## Acceptance Criteria

1. Adapter interface exists with Name, Version, Capabilities, Execute, Health
2. InputValidator is an optional interface (not on the core Adapter)
3. Adapter registry can register, resolve, and list adapters
4. Adapter names enforce no-dots rule
5. Adapter manifests can be loaded from YAML (optional)
6. Adapter events are emitted during execution
7. Adapter artifacts are persisted
8. Policy engine can evaluate adapter actions
9. Workflow dispatch.run resolves adapters from registry
10. Echo adapter works end-to-end
11. CLI can list, show, and health-check adapters
12. All existing tests pass (256+)
13. README documents V9 truthfully