package multiagent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// testStoreWorkflowDefinition builds a small, valid, self-contained
// WorkflowDefinition: one role node ("worker") routing directly to one
// terminal node on outcome "done". label distinguishes otherwise-identical
// definitions in tests that need genuinely different compiled content (label
// is folded into the role's displayName, which participates in the compiled
// graph's fingerprint via CompiledNode.DisplayName).
func testStoreWorkflowDefinition(workflowID, userVersion, label string) WorkflowDefinition {
	return WorkflowDefinition{
		APIVersion: SchemaAPIVersion,
		Kind:       SchemaKind,
		Metadata: WorkflowMetadata{
			Name:    workflowID,
			ID:      workflowID,
			Version: userVersion,
		},
		Spec: WorkflowSpec{
			EntryNode: "worker",
			Nodes: []SchemaNode{
				{
					ID:              "worker",
					Type:            "role",
					Role:            "worker",
					DisplayName:     label,
					AgentProfile:    "worker-agent",
					AllowedOutcomes: []string{"done"},
				},
				{
					ID:                "done",
					Type:              "terminal",
					TerminalCondition: string(TerminalConditionCompleted),
				},
			},
			Edges: []SchemaEdge{
				{
					ID:   "worker-done",
					From: "worker",
					To:   "done",
					When: SchemaCondition{Outcome: "done"},
				},
			},
		},
	}
}

func compileForTest(t *testing.T, def WorkflowDefinition) *CompiledGraph {
	t.Helper()
	graph, diags, err := Compile(def, nil, CompileOptions{})
	if err != nil {
		t.Fatalf("compile: %v (diagnostics: %s)", err, diags.Error())
	}
	return graph
}

func newTestDefinitionStore(t *testing.T) *DefinitionStore {
	t.Helper()
	store, err := NewDefinitionStore(filepath.Join(t.TempDir(), "definitions.db"))
	if err != nil {
		t.Fatalf("new definition store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestDefinitionStore_RegisterGetListLatestRoundTrip(t *testing.T) {
	store := newTestDefinitionStore(t)
	ctx := context.Background()

	def := testStoreWorkflowDefinition("wf-roundtrip", "1.0.0", "v1")
	graph := compileForTest(t, def)

	reg, err := store.Register(ctx, def, graph, "file:///tmp/wf.yaml")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	if reg.Version != 1 {
		t.Fatalf("first registration version = %d, want 1", reg.Version)
	}
	if reg.WorkflowID != "wf-roundtrip" || reg.Fingerprint != graph.Fingerprint() {
		t.Fatalf("unexpected registration: %+v", reg)
	}
	if reg.Graph == nil || reg.Graph.RegistryVersion() != 1 {
		t.Fatalf("registered graph registry version = %v, want 1", reg.Graph)
	}

	got, err := store.Get(ctx, "wf-roundtrip", 1)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Fingerprint != graph.Fingerprint() {
		t.Fatalf("get fingerprint = %q, want %q", got.Fingerprint, graph.Fingerprint())
	}
	if got.Graph == nil || got.Graph.RegistryVersion() != 1 {
		t.Fatalf("get graph registry version = %v, want 1", got.Graph)
	}
	if _, err := got.Graph.Resolve(Role("worker"), TransitionOutcome("done")); err != nil {
		t.Fatalf("recompiled graph should be fully usable: resolve worker/done: %v", err)
	}

	byFP, err := store.GetByFingerprint(ctx, "wf-roundtrip", graph.Fingerprint())
	if err != nil {
		t.Fatalf("get by fingerprint: %v", err)
	}
	if byFP.Version != 1 {
		t.Fatalf("get by fingerprint version = %d, want 1", byFP.Version)
	}

	latest, err := store.Latest(ctx, "wf-roundtrip")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.Version != 1 {
		t.Fatalf("latest version = %d, want 1", latest.Version)
	}

	versions, err := store.ListVersions(ctx, "wf-roundtrip")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 || versions[0].Version != 1 {
		t.Fatalf("list versions = %+v, want one v1 summary", versions)
	}

	workflows, err := store.ListWorkflows(ctx)
	if err != nil {
		t.Fatalf("list workflows: %v", err)
	}
	if len(workflows) != 1 || workflows[0] != "wf-roundtrip" {
		t.Fatalf("list workflows = %v, want [wf-roundtrip]", workflows)
	}
}

func TestDefinitionStore_RegisterIdenticalDefinitionIsUnchanged(t *testing.T) {
	store := newTestDefinitionStore(t)
	ctx := context.Background()

	def := testStoreWorkflowDefinition("wf-unchanged", "1.0.0", "same")
	graph := compileForTest(t, def)

	first, err := store.Register(ctx, def, graph, "")
	if err != nil {
		t.Fatalf("first register: %v", err)
	}
	if first.Version != 1 {
		t.Fatalf("first version = %d, want 1", first.Version)
	}

	// Re-registering the byte-identical definition must report
	// ErrDefinitionUnchanged and return the SAME version, not a new one.
	second, err := store.Register(ctx, def, graph, "")
	if !errors.Is(err, ErrDefinitionUnchanged) {
		t.Fatalf("second register err = %v, want ErrDefinitionUnchanged", err)
	}
	if second.Version != 1 {
		t.Fatalf("second register version = %d, want 1 (unchanged)", second.Version)
	}

	versions, err := store.ListVersions(ctx, "wf-unchanged")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 1 {
		t.Fatalf("expected exactly one stored version after a duplicate register, got %d", len(versions))
	}
}

func TestDefinitionStore_RegisterDifferentDefinitionCreatesNewVersion(t *testing.T) {
	store := newTestDefinitionStore(t)
	ctx := context.Background()

	defV1 := testStoreWorkflowDefinition("wf-versions", "1.0.0", "v1")
	graphV1 := compileForTest(t, defV1)
	regV1, err := store.Register(ctx, defV1, graphV1, "")
	if err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if regV1.Version != 1 {
		t.Fatalf("v1 version = %d, want 1", regV1.Version)
	}

	defV2 := testStoreWorkflowDefinition("wf-versions", "2.0.0", "v2 - genuinely different content")
	graphV2 := compileForTest(t, defV2)
	regV2, err := store.Register(ctx, defV2, graphV2, "")
	if err != nil {
		t.Fatalf("register v2: %v", err)
	}
	if regV2.Version != 2 {
		t.Fatalf("v2 version = %d, want 2", regV2.Version)
	}
	if regV2.Fingerprint == regV1.Fingerprint {
		t.Fatal("v1 and v2 fingerprints must differ for genuinely different content")
	}

	defV3 := testStoreWorkflowDefinition("wf-versions", "3.0.0", "v3 - also different")
	graphV3 := compileForTest(t, defV3)
	regV3, err := store.Register(ctx, defV3, graphV3, "")
	if err != nil {
		t.Fatalf("register v3: %v", err)
	}
	if regV3.Version != 3 {
		t.Fatalf("v3 version = %d, want 3", regV3.Version)
	}

	// v1 must remain retrievable, unmodified, at version 1 — registering v2/v3
	// must never alter or shadow it.
	gotV1, err := store.Get(ctx, "wf-versions", 1)
	if err != nil {
		t.Fatalf("get v1 after later registrations: %v", err)
	}
	if gotV1.Fingerprint != graphV1.Fingerprint() {
		t.Fatalf("v1 fingerprint changed: got %q, want %q", gotV1.Fingerprint, graphV1.Fingerprint())
	}

	latest, err := store.Latest(ctx, "wf-versions")
	if err != nil {
		t.Fatalf("latest: %v", err)
	}
	if latest.Version != 3 {
		t.Fatalf("latest version = %d, want 3", latest.Version)
	}

	versions, err := store.ListVersions(ctx, "wf-versions")
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("list versions len = %d, want 3", len(versions))
	}
	for i, v := range versions {
		if v.Version != int64(i+1) {
			t.Fatalf("versions[%d].Version = %d, want %d", i, v.Version, i+1)
		}
	}
}

func TestDefinitionStore_GetUnknownReturnsErrDefinitionNotFound(t *testing.T) {
	store := newTestDefinitionStore(t)
	ctx := context.Background()

	if _, err := store.Get(ctx, "does-not-exist", 1); !errors.Is(err, ErrDefinitionNotFound) {
		t.Fatalf("get unknown workflow: err = %v, want ErrDefinitionNotFound", err)
	}

	def := testStoreWorkflowDefinition("wf-partial", "1.0.0", "v1")
	graph := compileForTest(t, def)
	if _, err := store.Register(ctx, def, graph, ""); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, err := store.Get(ctx, "wf-partial", 99); !errors.Is(err, ErrDefinitionNotFound) {
		t.Fatalf("get unknown version: err = %v, want ErrDefinitionNotFound", err)
	}
	if _, err := store.Latest(ctx, "does-not-exist"); !errors.Is(err, ErrDefinitionNotFound) {
		t.Fatalf("latest unknown workflow: err = %v, want ErrDefinitionNotFound", err)
	}
}

// TestDefinitionStore_ConcurrentRegistrationProducesDistinctSequentialVersions
// spins up several goroutines registering genuinely DIFFERENT definitions
// under the same workflow_id at the same time, against a real on-disk
// SQLite database (not a mock), and asserts every registration succeeds with
// a distinct, sequential version number and no lost writes.
func TestDefinitionStore_ConcurrentRegistrationProducesDistinctSequentialVersions(t *testing.T) {
	store := newTestDefinitionStore(t)
	ctx := context.Background()

	const workflowID = "wf-concurrent"
	const n = 8

	var wg sync.WaitGroup
	versions := make([]int64, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			def := testStoreWorkflowDefinition(workflowID, fmt.Sprintf("1.0.%d", i), fmt.Sprintf("concurrent-%d", i))
			graph := compileForTest(t, def)
			reg, err := store.Register(ctx, def, graph, "")
			versions[i] = reg.Version
			errs[i] = err
		}(i)
	}
	wg.Wait()

	seen := make(map[int64]bool, n)
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: register error: %v", i, errs[i])
		}
		if seen[versions[i]] {
			t.Fatalf("version %d was assigned more than once: %v", versions[i], versions)
		}
		seen[versions[i]] = true
	}
	for v := int64(1); v <= n; v++ {
		if !seen[v] {
			t.Fatalf("version %d was never assigned: %v", v, versions)
		}
	}

	all, err := store.ListVersions(ctx, workflowID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	if len(all) != n {
		t.Fatalf("stored version count = %d, want %d (no lost writes)", len(all), n)
	}
}

func TestDefinitionStore_RegisterRequiresCompiledGraph(t *testing.T) {
	store := newTestDefinitionStore(t)
	def := testStoreWorkflowDefinition("wf-nograph", "1.0.0", "v1")
	if _, err := store.Register(context.Background(), def, nil, ""); err == nil {
		t.Fatal("expected an error registering with a nil compiled graph")
	}
}
