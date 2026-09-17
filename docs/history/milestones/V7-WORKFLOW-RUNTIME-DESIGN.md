# V7 — Workflow Runtime

## Mission

Compose Prizm's independently-callable capabilities (tools, gates, dispatch)
into named, repeatable workflows. A workflow is a YAML-defined sequence of
steps that Prizm executes in order, automatically emitting lifecycle events
at every stage.

**Before V7: call one thing at a time. After V7: define a workflow and Prizm runs**
**the full sequence.**

## What Changed

### Workflow Definition (`internal/workflow/definition.go`)
- `Workflow` struct: `Name`, `Description`, `Version`, `Steps`
- `Step` struct: `ID`, `Type`, `Tool`, `Gate`, `Adapter`, `Input`, `When`
- YAML-based: workflows are defined as YAML files, loaded from directory
- Validation: name required, at least one step required

### Step Types
- `tool.execute` — execute a registered Prizm tool
- `gate.evaluate` — evaluate a domain gate (domain-agnostic callback)
- `dispatch.run` — run a dispatch adapter
- `workflow.stop` — halt the workflow

### Workflow Registry (`internal/workflow/registry.go`)
- `Register(Workflow)` — add a workflow definition
- `LoadFromDir(dir)` — load all `.yaml` files from a directory
- `List()`, `Resolve(name)` — list and find workflows

### Workflow Runner (`internal/workflow/runner.go`)
- `Runner.Run(ctx, workflow, input)` — execute all steps in order
- `StepHandlers` — function-based callbacks: `ToolExecuteFunc`, `GateEvaluateFunc`, `DispatchRunFunc`
- Prizm stays domain-agnostic: no gate/dispatch concrete imports in workflow package
- Automatic lifecycle events for workflow and steps
- State persistence: `workflow_state.json`, `workflow_summary.json`
- Pause/resume foundation: `paused` state when approval needed

### Workflow Events
- `prizm.workflow.started/completed/failed` — workflow lifecycle
- `prizm.workflow.paused/resumed` — pause/resume support
- `prizm.workflow.step.started/completed/failed/skipped` — per-step lifecycle

### Condition Support (`internal/workflow/condition.go`)
- Simple conditions: `step_id.field == "value"` expressions
- Skip steps conditionally based on previous step output

### Artifacts (`internal/workflow/artifacts.go`)
- `workflow_state.json` — persisted workflow execution state
- `workflow_summary.json` — human-readable workflow run summary

### Demo Workflow (`examples/workflows/demo-echo.yaml`)
- `demo.echo_tool` — runs the echo tool as a workflow step
- End-to-end example of workflow definition, registration, and execution

### CLI Commands
- `prizm workflow list` — list all registered workflows
- `prizm workflow show <name>` — show workflow definition
- `prizm workflow run <name> --input '{...}'` — execute a workflow
- `prizm workflow status <run_id>` — check workflow run status

## Key Packages/Files

| Package / File | Purpose |
|---|---|
| `internal/workflow/definition.go` | Workflow + Step structs, YAML parsing, validation |
| `internal/workflow/registry.go` | Register, load from dir, list, resolve |
| `internal/workflow/runner.go` | Ordered step execution with event emission |
| `internal/workflow/step.go` | Step type handlers (tool, gate, dispatch) |
| `internal/workflow/condition.go` | Simple condition evaluation |
| `internal/workflow/events.go` | 9 workflow event type constants |
| `internal/workflow/state.go` | WorkflowState persistence |
| `internal/workflow/artifacts.go` | Artifact generation |
| `cmd/prizm-cli/main.go` | Workflow CLI commands |
| `examples/workflows/demo-echo.yaml` | Demo workflow definition |

## Design Decisions

1. **Function-based handlers, not concrete imports** — The workflow package doesn't
   import `internal/tool` or any gate/dispatch packages. Instead, it accepts
   `ToolExecuteFunc` callbacks at runner construction. This keeps Prizm domain-agnostic
   and prevents circular dependencies. Domain repos provide their own handlers.

2. **YAML definitions, not code** — Workflows are data, not code. A YAML file
   describes a named sequence of steps. This enables non-developer workflow
   authoring, version control, and runtime loading without recompilation.

3. **Automatic events** — The runner emits lifecycle events automatically. Step
   authors don't need to add event emission; it's built into the runtime. This
   ensures consistent event coverage for auditing and debugging.

4. **Pause/resume foundation** — When a step requires approval (e.g., approval-gated
   mutation), the workflow enters `paused` state. The resume mechanism is
   architected but full resume-from-pause requires a workflow runner that
   persists state and can be re-invoked.

5. **Simple conditions, not expressions** — V7 uses `step_id.field == "value"` matching,
   not a full expression language. Complex branching belongs in higher-level
   workflow systems or is deferred to V13+ multi-agent coordination.

6. **Demo-driven development** — The `demo.echo_tool` workflow ships as a working
   example. This serves as documentation, a smoke test, and a template for new
   workflows.

7. **Domain-agnostic core** — V6 (Gate System) was moved to AI-Hedge-Prizm.
   V7's `gate.evaluate` step keeps the door open for domain repos to implement
   their own gates through function callbacks. Prizm provides the runtime;
   domains provide the logic.

## Test Coverage

- **32 new tests** (213 total, all passing)
- Workflow: definition validation (missing name, no steps), YAML round-trip
- Registry: register, load from dir, list, resolve, duplicate
- Runner: echo tool execution, step lifecycle events, workflow lifecycle events,
  condition-based skip, handler-not-set error, input propagation
- State: status transitions, persistence round-trip
- Artifacts: state.json, summary.json generation

## Workflow Execution Flow

```
prizm workflow run demo.echo_tool --input '{"message": "hello"}'
  → Registry.Resolve("demo.echo_tool")
  → Runner.Run(ctx, workflow, input)
    → emit(prizm.workflow.started)
    → for each step:
        → emit(prizm.workflow.step.started)
        → handler(ctx, step.input)
        → Success → emit(prizm.workflow.step.completed)
        → Failure → emit(prizm.workflow.step.failed)
        → Condition → emit(prizm.workflow.step.skipped)
    → emit(prizm.workflow.completed)
    → Artifacts: workflow_state.json, workflow_summary.json
```
