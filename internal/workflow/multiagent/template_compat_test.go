// template_compat_test.go is PR7's regression proof that the three shipped
// example workflows under templates/ actually work end to end, not merely
// parse: for each template this loads it (LoadDefinition), validates it
// (ValidateDefinition, asserting zero error-severity diagnostics), compiles
// it (Compile, asserting success), loads its matching scenario fixture(s)
// under testdata/template-scenarios/ (LoadScenarioFile), and runs every
// scenario in each fixture against the compiled graph (RunScenario,
// asserting every scenario passes). This is the CI-exercised guarantee that
// a developer copying one of these templates as a starting point is copying
// something that is known to actually run, not just something that
// satisfies the schema.
//
// Scenario fixtures deliberately live outside templates/ (in
// testdata/template-scenarios/, referencing their workflow via a relative
// "../../templates/<file>.yaml" path) rather than alongside the workflow
// definitions: `prism graph validate`/`prism graph test` both walk a given
// directory recursively and treat every .yaml/.yml/.json file found as,
// respectively, a workflow definition or a scenario fixture, with no
// content-based disambiguation (see cmd_graph_testcmd_test.go's
// TestExecuteGraphTestWalksDirectoryRecursively for the existing, deliberate
// convention this mirrors). Keeping the two file kinds in separate directory
// trees is what lets `prism graph validate ./templates` and a `graph test`
// run against the scenario directory each succeed against a clean,
// single-purpose file set.
package multiagent

import (
	"context"
	"path/filepath"
	"testing"
)

// templateCase names one shipped template and the scenario fixture files
// that exercise it.
type templateCase struct {
	workflowFile  string
	scenarioFiles []string
}

func shippedTemplateCases() []templateCase {
	return []templateCase{
		{
			workflowFile: filepath.Join("templates", "software-delivery.yaml"),
			scenarioFiles: []string{
				filepath.Join("testdata", "template-scenarios", "software-delivery-happy-path.yaml"),
				filepath.Join("testdata", "template-scenarios", "software-delivery-loop-exhaustion.yaml"),
			},
		},
		{
			workflowFile: filepath.Join("templates", "security-review.yaml"),
			scenarioFiles: []string{
				filepath.Join("testdata", "template-scenarios", "security-review-happy-path.yaml"),
				filepath.Join("testdata", "template-scenarios", "security-review-loop-exhaustion.yaml"),
			},
		},
		{
			workflowFile: filepath.Join("templates", "documentation-change.yaml"),
			scenarioFiles: []string{
				filepath.Join("testdata", "template-scenarios", "documentation-change-happy-path.yaml"),
			},
		},
	}
}

// TestShippedTemplatesLoadValidateCompile is the load/validate/compile half
// of the regression proof: every shipped template must decode with zero
// load-time diagnostics, validate with zero error-severity diagnostics, and
// compile successfully.
func TestShippedTemplatesLoadValidateCompile(t *testing.T) {
	for _, tc := range shippedTemplateCases() {
		tc := tc
		t.Run(tc.workflowFile, func(t *testing.T) {
			def, idx, loadDiags, err := LoadDefinition(tc.workflowFile)
			if err != nil {
				t.Fatalf("LoadDefinition(%s): %v", tc.workflowFile, err)
			}
			if loadDiags.HasErrors() {
				t.Fatalf("LoadDefinition(%s) produced error diagnostics: %s", tc.workflowFile, loadDiags.Error())
			}

			validateDiags := ValidateDefinition(def, idx)
			if validateDiags.HasErrors() {
				t.Fatalf("ValidateDefinition(%s) produced error diagnostics: %s", tc.workflowFile, validateDiags.Error())
			}

			graph, compileDiags, err := Compile(def, idx, CompileOptions{})
			if err != nil {
				t.Fatalf("Compile(%s): %v (diagnostics: %s)", tc.workflowFile, err, compileDiags.Error())
			}
			if graph == nil {
				t.Fatalf("Compile(%s) returned a nil graph with no error", tc.workflowFile)
			}
			if graph.Fingerprint() == "" {
				t.Errorf("Compile(%s) produced an empty fingerprint", tc.workflowFile)
			}
		})
	}
}

// TestShippedTemplatesScenariosPass is the scenario half of the regression
// proof: every scenario in every matching fixture must pass against the
// template's compiled graph, with zero scenario-runner setup errors.
func TestShippedTemplatesScenariosPass(t *testing.T) {
	for _, tc := range shippedTemplateCases() {
		tc := tc
		t.Run(tc.workflowFile, func(t *testing.T) {
			def, idx, loadDiags, err := LoadDefinition(tc.workflowFile)
			if err != nil {
				t.Fatalf("LoadDefinition(%s): %v", tc.workflowFile, err)
			}
			if loadDiags.HasErrors() {
				t.Fatalf("LoadDefinition(%s) produced error diagnostics: %s", tc.workflowFile, loadDiags.Error())
			}
			graph, compileDiags, err := Compile(def, idx, CompileOptions{})
			if err != nil {
				t.Fatalf("Compile(%s): %v (diagnostics: %s)", tc.workflowFile, err, compileDiags.Error())
			}

			for _, scenarioFile := range tc.scenarioFiles {
				scenarioFile := scenarioFile
				t.Run(scenarioFile, func(t *testing.T) {
					sf, err := LoadScenarioFile(scenarioFile)
					if err != nil {
						t.Fatalf("LoadScenarioFile(%s): %v", scenarioFile, err)
					}
					if len(sf.Scenarios) == 0 {
						t.Fatalf("scenario file %s declares zero scenarios", scenarioFile)
					}
					for _, scenario := range sf.Scenarios {
						scenario := scenario
						t.Run(scenario.Name, func(t *testing.T) {
							result, err := RunScenario(context.Background(), graph, scenario)
							if err != nil {
								t.Fatalf("RunScenario(%s :: %s): %v", scenarioFile, scenario.Name, err)
							}
							if !result.Passed {
								t.Errorf("scenario %s :: %s failed:\n%s", scenarioFile, scenario.Name, joinFailures(result.Failures))
							}
						})
					}
				})
			}
		})
	}
}

// TestShippedTemplateExplicitLoopBudgetsExhaustAtLimit proves the shipped
// correction loops are unlimited by default while a workflow author can opt
// into an exact traversal ceiling. It reuses each finite script from
// TestShippedTemplatesScenariosPass against a copy with only that edge capped.
func TestShippedTemplateExplicitLoopBudgetsExhaustAtLimit(t *testing.T) {
	cases := []struct {
		name         string
		workflowFile string
		scenarioFile string
		edgeID       string
		limit        int
		budgetName   string
		visits       map[string]int
		traversals   map[string]int
	}{
		{
			name:         "software-delivery tester correction",
			workflowFile: filepath.Join("templates", "software-delivery.yaml"),
			scenarioFile: filepath.Join("testdata", "template-scenarios", "software-delivery-loop-exhaustion.yaml"),
			edgeID:       "tester-to-dev",
			limit:        2,
			budgetName:   "max_tester_to_developer_loops",
			visits:       map[string]int{"planner": 1, "developer": 3, "tester": 3},
			traversals:   map[string]int{"edge:tester:tests_failed": 2},
		},
		{
			name:         "security-review reviewer correction",
			workflowFile: filepath.Join("templates", "security-review.yaml"),
			scenarioFile: filepath.Join("testdata", "template-scenarios", "security-review-loop-exhaustion.yaml"),
			edgeID:       "reviewer-to-impl",
			limit:        3,
			budgetName:   "max_edge:security-reviewer:issues_found_loops",
			visits:       map[string]int{"implementer": 4, "security-scanner": 4, "security-reviewer": 4},
			traversals:   map[string]int{"edge:security-reviewer:issues_found": 3},
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			def, idx, loadDiags, err := LoadDefinition(tc.workflowFile)
			if err != nil {
				t.Fatalf("LoadDefinition(%s): %v", tc.workflowFile, err)
			}
			if loadDiags.HasErrors() {
				t.Fatalf("LoadDefinition(%s) produced error diagnostics: %s", tc.workflowFile, loadDiags.Error())
			}

			var correctionEdge *SchemaEdge
			for i := range def.Spec.Edges {
				if def.Spec.Edges[i].ID == tc.edgeID {
					correctionEdge = &def.Spec.Edges[i]
					break
				}
			}
			if correctionEdge == nil || correctionEdge.Loop == nil {
				t.Fatalf("correction edge %q is missing its loop policy", tc.edgeID)
			}
			if correctionEdge.Loop.MaxTraversals != -1 {
				t.Fatalf("default %s maxTraversals = %d, want -1 (unlimited)", tc.edgeID, correctionEdge.Loop.MaxTraversals)
			}
			correctionEdge.Loop.MaxTraversals = tc.limit

			graph, compileDiags, err := Compile(def, idx, CompileOptions{})
			if err != nil {
				t.Fatalf("Compile(%s with explicit loop limit): %v (diagnostics: %s)", tc.workflowFile, err, compileDiags.Error())
			}
			sf, err := LoadScenarioFile(tc.scenarioFile)
			if err != nil {
				t.Fatalf("LoadScenarioFile(%s): %v", tc.scenarioFile, err)
			}
			if len(sf.Scenarios) != 1 {
				t.Fatalf("scenario file %s has %d scenarios, want 1", tc.scenarioFile, len(sf.Scenarios))
			}
			scenario := sf.Scenarios[0]
			scenario.Expect = ScenarioExpectation{
				FinalStatus:    "budget_exhausted",
				LoopExhausted:  tc.budgetName,
				NodeVisits:     tc.visits,
				EdgeTraversals: tc.traversals,
			}
			result, err := RunScenario(context.Background(), graph, scenario)
			if err != nil {
				t.Fatalf("RunScenario(%s :: %s): %v", tc.scenarioFile, scenario.Name, err)
			}
			if !result.Passed {
				t.Fatalf("scenario %s :: %s failed with explicit maxTraversals %d:\n%s", tc.scenarioFile, scenario.Name, tc.limit, joinFailures(result.Failures))
			}
		})
	}
}

func joinFailures(failures []string) string {
	out := ""
	for _, f := range failures {
		out += "  - " + f + "\n"
	}
	return out
}
