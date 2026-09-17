// Package multiagent defines the canonical contracts and invariants for
// bounded multi-agent workflow execution.
//
// The package intentionally does not execute agents, select transitions,
// persist state, or publish events. Those responsibilities belong to the
// supervisor, storage, and event boundaries that consume these contracts.
package multiagent
