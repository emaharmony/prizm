package orchestrator

import (
	"strings"
	"testing"

	"github.com/emaharmony/prizm/internal/freewill"
	"gopkg.in/yaml.v3"
)

func TestFreewillFeatureSwitchesDefaultOffAndRoundTrip(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Freewill.Enabled || cfg.Freewill.Scheduling.Enabled || cfg.Freewill.Execution.Enabled || cfg.Freewill.DiaryEnabled || cfg.Freewill.DiaryOwnerID != "" || cfg.Freewill.TaskAppraisalEnabled || cfg.Freewill.TaskAppraisalOwnerID != "" || cfg.Freewill.TaskSurprise.Enabled || cfg.Freewill.TaskSurprise.ContradictionAuditEnabled || cfg.Freewill.TaskSurprise.ProjectActionID != "" || cfg.Freewill.TaskSurprise.SourceRevisionSHA256 != "" || cfg.Freewill.TaskSurprise.ExpectedOutcome != "" || cfg.Freewill.TaskReflectionEnabled || cfg.Freewill.TaskReflectionOwnerID != "" || cfg.Freewill.GoalProposalEnabled || cfg.Freewill.GoalProposalOwnerID != "" || cfg.Freewill.MemoryContext.Enabled {
		t.Fatal("Freewill feature, scheduler, and execution must default off")
	}

	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal config: %v", err)
	}
	decoded, err := LoadConfigFromBytes(encoded)
	if err != nil {
		t.Fatalf("reload default config: %v", err)
	}
	if decoded.Freewill.Enabled || decoded.Freewill.Scheduling.Enabled || decoded.Freewill.Execution.Enabled || decoded.Freewill.DiaryEnabled || decoded.Freewill.DiaryOwnerID != "" || decoded.Freewill.TaskAppraisalEnabled || decoded.Freewill.TaskAppraisalOwnerID != "" || decoded.Freewill.TaskSurprise.Enabled || decoded.Freewill.TaskSurprise.ContradictionAuditEnabled || decoded.Freewill.TaskSurprise.ProjectActionID != "" || decoded.Freewill.TaskSurprise.SourceRevisionSHA256 != "" || decoded.Freewill.TaskSurprise.ExpectedOutcome != "" || decoded.Freewill.TaskReflectionEnabled || decoded.Freewill.TaskReflectionOwnerID != "" || decoded.Freewill.MemoryContext.Enabled {
		t.Fatal("Freewill flags changed during default config round trip")
	}
	if decoded.Shell.MasterUserID != cfg.Shell.MasterUserID || len(decoded.Shell.Allowlists) != len(cfg.Shell.Allowlists) {
		t.Fatal("adding Freewill schema changed existing Freedom/Free Mode configuration")
	}
}

func TestProjectFreewillContextFilesAndExactSectionsRoundTrip(t *testing.T) {
	project := ProjectConfig{ID: "prizm", RepoPath: t.TempDir(), FreewillContextFiles: []string{"README.md", "docs/roadmap.md"},
		FreewillContextSections: map[string]string{"docs/roadmap.md": "### Current roadmap"}}
	cfg := DefaultConfig()
	cfg.Projects = []ProjectConfig{project}
	encoded, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := LoadConfigFromBytes(encoded)
	if err != nil {
		t.Fatalf("load project context allowlist: %v", err)
	}
	if len(decoded.Projects) != 1 || len(decoded.Projects[0].FreewillContextFiles) != 2 || decoded.Projects[0].FreewillContextFiles[1] != "docs/roadmap.md" || decoded.Projects[0].FreewillContextSections["docs/roadmap.md"] != "### Current roadmap" {
		t.Fatalf("project context scope changed on round trip: %#v", decoded.Projects)
	}
	cfg.Projects[0].FreewillContextFiles = nil
	encoded, err = yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal section-only config: %v", err)
	}
	if _, err := LoadConfigFromBytes(encoded); err == nil || !strings.Contains(err.Error(), "freewill_context_sections requires freewill_context_files") {
		t.Fatalf("section-only config silently fell back to legacy context: %v", err)
	}
}

func TestFreewillTaskSurpriseRequiresExactHostActionAndTypedOutcome(t *testing.T) {
	workspace := t.TempDir()
	agents, projects := []AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true,
		TaskAppraisalOwnerID: "owner", TaskSurprise: FreewillTaskSurpriseConfig{Enabled: true, ProjectActionID: "project:roadmap:M2",
			SourceID: "project:readme", SourceRevisionSHA256: strings.Repeat("a", 64), ExpectedOutcome: FreewillTaskSurpriseCompleted}}
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid host-bound surprise opt-in rejected: %v", err)
	}
	base.TaskSurprise.ContradictionAuditEnabled = true
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid host-bound contradiction audit opt-in rejected: %v", err)
	}
	base.TaskAppraisalEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("task surprise accepted without task appraisal")
	}
	base.TaskAppraisalEnabled = true
	base.TaskSurprise.ExpectedOutcome = "unknown"
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("task surprise accepted an untyped/unknown host expectation")
	}
	base.TaskSurprise.ExpectedOutcome = FreewillTaskSurpriseCompleted
	base.TaskSurprise.SourceRevisionSHA256 = strings.Repeat("A", 64)
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("task surprise accepted a non-canonical source digest")
	}
	base.TaskSurprise = FreewillTaskSurpriseConfig{ContradictionAuditEnabled: true}
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("contradiction audit accepted without the exact task-surprise host binding")
	}
}

func TestFreewillGoalProposalRequiresExplicitMatchingOwnerAndAppraisal(t *testing.T) {
	workspace := t.TempDir()
	agents, projects := []AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner", GoalProposalEnabled: true, GoalProposalOwnerID: "different"}
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("goal proposal accepted an owner different from task appraisal")
	}
	base.GoalProposalOwnerID = " owner "
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("goal proposal accepted whitespace-padded owner")
	}
	base.GoalProposalOwnerID = "owner"
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid goal proposal opt-in rejected: %v", err)
	}
	base.TaskAppraisalEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("goal proposal enabled without task appraisal")
	}
}

func TestFreewillConfigFlagsAreIndependentAndDoNotActivateRuntime(t *testing.T) {
	featureOnly, err := LoadConfigFromBytes([]byte("freewill:\n  enabled: true\n"))
	if err != nil {
		t.Fatalf("load feature-only config: %v", err)
	}
	if !featureOnly.Freewill.Enabled || featureOnly.Freewill.Scheduling.Enabled {
		t.Fatalf("feature and scheduling flags must remain independent: %#v", featureOnly.Freewill)
	}

	schedulingOnly, err := LoadConfigFromBytes([]byte("freewill:\n  scheduling:\n    enabled: true\n"))
	if err != nil {
		t.Fatalf("load scheduling-only config: %v", err)
	}
	if schedulingOnly.Freewill.Enabled || !schedulingOnly.Freewill.Scheduling.Enabled {
		t.Fatalf("scheduling flag must not enable feature flag: %#v", schedulingOnly.Freewill)
	}

	executionOnly, err := LoadConfigFromBytes([]byte("freewill:\n  execution:\n    enabled: true\n"))
	if err != nil {
		t.Fatalf("load execution-only config: %v", err)
	}
	if executionOnly.Freewill.Enabled || !executionOnly.Freewill.Execution.Enabled {
		t.Fatalf("execution opt-in must not turn on Freewill: %#v", executionOnly.Freewill)
	}
}

func TestFreewillEventWakeAllowlistDefaultsOffAndRequiresExactKinds(t *testing.T) {
	defaults := DefaultConfig()
	if len(defaults.Freewill.Scheduling.EventKinds) != 0 {
		t.Fatalf("event wake allowlist must default off: %#v", defaults.Freewill.Scheduling.EventKinds)
	}
	cfg, err := LoadConfigFromBytes([]byte("freewill:\n  scheduling:\n    event_kinds:\n      - prizm.task.completed\n"))
	if err != nil {
		t.Fatalf("load exact event allowlist: %v", err)
	}
	if cfg.Freewill.Enabled || cfg.Freewill.Scheduling.Enabled || len(cfg.Freewill.Scheduling.EventKinds) != 1 || cfg.Freewill.Scheduling.EventKinds[0] != "prizm.task.completed" {
		t.Fatalf("event allowlist activated unrelated flags or failed to round trip: %#v", cfg.Freewill)
	}
	workspace := t.TempDir()
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", Scheduling: cfg.Freewill.Scheduling}
	if err := base.ValidateHostBinding([]AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}); err != nil {
		t.Fatalf("valid exact event kind rejected: %v", err)
	}
	for _, invalid := range [][]string{{"prizm.task.*"}, {"prizm.>"}, {"other.task.completed"}, {"prizm..completed"}, {"prizm.task.completed", "prizm.task.completed"}} {
		base.Scheduling.EventKinds = invalid
		if err := base.ValidateHostBinding([]AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}); err == nil {
			t.Fatalf("invalid event allowlist accepted: %#v", invalid)
		}
	}
}

func TestFreewillTaskAppraisalRequiresExplicitLocalOwnerOptIn(t *testing.T) {
	workspace := t.TempDir()
	if err := (FreewillConfig{Enabled: true, TaskAppraisalEnabled: true}).ValidateHostBinding(nil, nil); err == nil {
		t.Fatal("task appraisal accepted without configured owner")
	}
	if err := (FreewillConfig{Enabled: true, TaskAppraisalEnabled: true, TaskAppraisalOwnerID: " owner "}).ValidateHostBinding(nil, nil); err == nil {
		t.Fatal("task appraisal accepted whitespace-padded owner")
	}
	if err := (FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner"}).ValidateHostBinding([]AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}); err != nil {
		t.Fatalf("explicit task appraisal opt-in rejected: %v", err)
	}
}

func TestFreewillTaskReflectionRequiresMatchingAppraisalOwner(t *testing.T) {
	workspace := t.TempDir()
	agents, projects := []AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner", TaskReflectionEnabled: true, TaskReflectionOwnerID: "other"}
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("reflection accepted a different owner than appraisal")
	}
	base.TaskReflectionOwnerID = " owner "
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("reflection accepted whitespace-padded owner")
	}
	base.TaskReflectionOwnerID = "owner"
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("matching explicit reflection owner rejected: %v", err)
	}
	base.TaskAppraisalEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("reflection enabled without task appraisal")
	}
}

func TestFreewillPreferenceHypothesisRequiresMatchingAppraisalOwner(t *testing.T) {
	workspace := t.TempDir()
	agents, projects := []AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner", PreferenceHypothesisEnabled: true, PreferenceHypothesisOwnerID: "other", PreferenceHypothesisTaskIDs: []string{"task-one", "task-two"}}
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("preference hypothesis accepted mismatched owner")
	}
	base.PreferenceHypothesisOwnerID = " owner "
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("preference hypothesis accepted whitespace owner")
	}
	base.PreferenceHypothesisOwnerID = "owner"
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid preference binding rejected: %v", err)
	}
	base.TaskAppraisalEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("preference hypothesis accepted without task appraisals")
	}
	base.TaskAppraisalEnabled = true
	base.PreferenceHypothesisTaskIDs = []string{"same", "same"}
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("preference hypothesis accepted duplicate source task IDs")
	}
	base.PreferenceHypothesisTaskIDs = []string{"task-one"}
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("preference hypothesis accepted incomplete source task IDs")
	}
}

func TestFreewillPreferenceRetentionIsOptionalAndBounded(t *testing.T) {
	workspace := t.TempDir()
	agents, projects := []AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner", PreferenceHypothesisEnabled: true, PreferenceHypothesisOwnerID: "owner", PreferenceHypothesisTaskIDs: []string{"task-one", "task-two"}}
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("unset/off retention rejected: %v", err)
	}
	base.PreferenceHypothesisRetentionSeconds = 60
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("bounded retention rejected: %v", err)
	}
	base.PreferenceHypothesisRetentionSeconds = -1
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("negative retention accepted")
	}
	base.PreferenceHypothesisRetentionSeconds = 10*365*24*60*60 + 1
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("unbounded retention accepted")
	}
	base.PreferenceHypothesisRetentionSeconds = 60
	base.PreferenceHypothesisEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("retention accepted while preference hypotheses are disabled")
	}
}

func TestFreewillPreferenceInfluenceRequiresOptedInExactHypothesisOwner(t *testing.T) {
	workspace := t.TempDir()
	agents, projects := []AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner",
		PreferenceHypothesisEnabled: true, PreferenceHypothesisOwnerID: "owner", PreferenceHypothesisTaskIDs: []string{"task-one", "task-two"}}
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("default-off preference influence rejected: %v", err)
	}
	base.PreferenceInfluenceEnabled = true
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("influence accepted without explicit owner")
	}
	base.PreferenceInfluenceOwnerID = "other"
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("influence accepted wrong owner")
	}
	base.PreferenceInfluenceOwnerID = " owner "
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("influence accepted whitespace owner")
	}
	base.PreferenceInfluenceOwnerID = "owner"
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid local hypothesis owner rejected: %v", err)
	}
	base.PreferenceHypothesisEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("influence accepted without preference hypothesis feature")
	}
}

func TestFreewillStrategyExperimentRequiresFixedHostVariants(t *testing.T) {
	workspace := t.TempDir()
	agents, projects := []AgentConfig{{ID: "selector"}}, []ProjectConfig{{ID: "project", RepoPath: workspace}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project", TaskAppraisalEnabled: true, TaskAppraisalOwnerID: "owner",
		StrategyExperimentEnabled: true, StrategyExperimentOwnerID: "owner", StrategyExperimentIDs: []string{"baseline", "alternative"}, StrategyExperimentVersions: []string{"v1", "v2"}, StrategyExperimentPatchSHA256: []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}}
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("fixed strategy pair rejected: %v", err)
	}
	base.StrategyVariantID, base.StrategyVariantVersion, base.StrategyVariantPatchSHA256 = "baseline", "v1", strings.Repeat("a", 64)
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("configured baseline variant rejected: %v", err)
	}
	base.StrategyVariantVersion = "v2"
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("variant accepted a version not configured for its ID")
	}
	base.StrategyVariantID, base.StrategyVariantVersion, base.StrategyVariantPatchSHA256 = "", "", ""
	base.StrategyExperimentIDs = []string{"baseline", "baseline"}
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("experiment accepted duplicate strategy IDs")
	}
	base.StrategyExperimentIDs = []string{"baseline", "alternative"}
	base.TaskAppraisalEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("experiment accepted without owner-scoped task outcomes")
	}
}

func TestEnabledFreewillRequiresTrustedAgentProjectAndSchedule(t *testing.T) {
	agents := []AgentConfig{{ID: "selector", Role: "planner", Provider: "ollama", Model: "glm-5.3:cloud"}}
	projects := []ProjectConfig{{ID: "project-a", RepoPath: `D:\projects\project-a`}}
	config := FreewillConfig{Enabled: true, AgentID: "selector"}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("enabled Freewill accepted a missing host project binding")
	}
	config = FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project-a", Scheduling: FreewillSchedulingConfig{Enabled: true}}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("enabled Freewill scheduling accepted a missing cron expression")
	}
	config.Scheduling.Schedule = "*/15 * * * *"
	if err := config.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid host-bound Freewill config rejected: %v", err)
	}
	if err := config.ValidateHostBinding(agents, nil); err == nil {
		t.Fatal("enabled Freewill accepted an unregistered project")
	}
}

func TestFreewillDiaryRequiresFeatureAndExplicitOwner(t *testing.T) {
	if err := (FreewillConfig{DiaryEnabled: true, DiaryOwnerID: "owner"}).ValidateHostBinding(nil, nil); err == nil {
		t.Fatal("diary opt-in activated when Freewill was disabled")
	}
	agents := []AgentConfig{{ID: "selector", Role: "planner", Provider: "ollama", Model: "synthetic"}}
	projects := []ProjectConfig{{ID: "project-a", RepoPath: `D:\projects\project-a`}}
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project-a"}
	base.DiaryEnabled = true
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("diary opt-in accepted without an explicit owner")
	}
	base.DiaryOwnerID = "synthetic-owner"
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("explicit local owner diary config rejected: %v", err)
	}
}

func TestFreewillDiaryAnnotationsRequireDiaryAndMatchingOwner(t *testing.T) {
	base := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project-a", DiaryEnabled: true, DiaryOwnerID: "synthetic-owner"}
	agents := []AgentConfig{{ID: "selector", Role: "planner", Provider: "ollama", Model: "synthetic"}}
	projects := []ProjectConfig{{ID: "project-a", RepoPath: `D:\projects\project-a`}}
	base.DiaryAnnotationsEnabled = true
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("diary annotations accepted without their explicit owner")
	}
	base.DiaryAnnotationsOwnerID = "synthetic-owner"
	if err := base.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid diary annotation config rejected: %v", err)
	}
	base.DiaryAnnotationsOwnerID = "other-owner"
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("diary annotations accepted a different owner")
	}
	base.DiaryAnnotationsOwnerID = "synthetic-owner"
	base.DiaryEnabled = false
	if err := base.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("diary annotations accepted without diary source")
	}
}

func TestFreewillMemoryContextRequiresExplicitDiaryOwnerAndSourceAllowlist(t *testing.T) {
	agents := []AgentConfig{{ID: "selector", Role: "planner", Provider: "ollama", Model: "synthetic"}}
	projects := []ProjectConfig{{ID: "project-a", RepoPath: `D:\projects\project-a`}}
	config := FreewillConfig{Enabled: true, AgentID: "selector", ProjectID: "project-a",
		DiaryEnabled: true, DiaryOwnerID: "diary-owner", MemoryContext: FreewillMemoryContextConfig{Enabled: true,
			OwnerID: "diary-owner", SourceAllowlist: []string{"prizm:freewill-terminal", "prizm:freewill-diary"}}}
	if err := config.ValidateHostBinding(agents, projects); err != nil {
		t.Fatalf("valid memory-context owner/source allowlist rejected: %v", err)
	}
	config.MemoryContext.OwnerID = ""
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("memory context accepted a missing owner")
	}
	config.MemoryContext.OwnerID = "different-owner"
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("memory context accepted an owner different from the diary owner")
	}
	config.DiaryOwnerID = "diary-owner "
	config.MemoryContext.OwnerID = "diary-owner "
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("memory context accepted whitespace-padded owner identities")
	}
	config.DiaryOwnerID = "diary-owner"
	config.MemoryContext.OwnerID = "diary-owner"
	config.MemoryContext.SourceAllowlist = []string{"prizm:freewill-terminal"}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("memory context accepted a partial source allowlist")
	}
	config.MemoryContext.SourceAllowlist = []string{"prizm:freewill-terminal", "prizm:freewill-diary", "recall"}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("memory context accepted an unapproved source")
	}
	config.MemoryContext.SourceAllowlist = []string{"prizm:freewill-terminal", "prizm:freewill-terminal"}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("memory context accepted a duplicate source")
	}
	config.DiaryEnabled = false
	config.MemoryContext.SourceAllowlist = []string{"prizm:freewill-terminal", "prizm:freewill-diary"}
	if err := config.ValidateHostBinding(agents, projects); err == nil {
		t.Fatal("memory context accepted without an enabled diary source")
	}
}

func TestFreewillObjectiveRoundTripsAndIsValidated(t *testing.T) {
	cfg, err := LoadConfigFromBytes([]byte("freewill:\n  objective: Review the synthetic selector path.\n"))
	if err != nil {
		t.Fatalf("load optional objective: %v", err)
	}
	if cfg.Freewill.Objective != "Review the synthetic selector path." || cfg.Freewill.Enabled {
		t.Fatalf("objective changed feature default or was not retained: %#v", cfg.Freewill)
	}
	if err := cfg.Freewill.ValidateHostBinding(cfg.Agents, cfg.Projects); err != nil {
		t.Fatalf("valid disabled objective rejected: %v", err)
	}
	cfg.Freewill.Objective = strings.Repeat("x", freewill.MaxSelectionObjectiveBytes+1)
	if err := cfg.Freewill.ValidateHostBinding(cfg.Agents, cfg.Projects); err == nil {
		t.Fatal("oversized objective accepted")
	}
}

func TestFreewillTerminalFactOptInIsProjectScopedAndDefaultsOff(t *testing.T) {
	defaults := DefaultConfig()
	if len(defaults.Projects) > 0 && (defaults.Projects[0].FreewillTerminalFactsProjectID != "" || defaults.Projects[0].FreewillTerminalFactsRepoPath != "") {
		t.Fatal("terminal fact capture must default off")
	}
	cfg, err := LoadConfigFromBytes([]byte("projects:\n  - id: project-a\n    repo_path: D:/projects/project-a\n    freewill_terminal_facts_project_id: project-a\n    freewill_terminal_facts_repo_path: D:/projects/project-a\n"))
	if err != nil {
		t.Fatalf("load explicit project binding: %v", err)
	}
	if len(cfg.Projects) != 1 || cfg.Projects[0].FreewillTerminalFactsProjectID != "project-a" ||
		cfg.Projects[0].FreewillTerminalFactsRepoPath != "D:/projects/project-a" {
		t.Fatalf("project terminal fact binding did not round trip: %#v", cfg.Projects)
	}
}

func TestFreewillPromiseConfirmationDefaultsOffAndRequiresBoundedOwnerSeed(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Freewill.PromiseConfirmation.Enabled || cfg.Freewill.PromiseConfirmation.PromiseID != "" {
		t.Fatal("promise confirmation must default off")
	}
	cfg.Freewill.Enabled = true
	cfg.Freewill.AgentID = "agent-a"
	cfg.Freewill.ProjectID = "project-a"
	cfg.Agents = []AgentConfig{{ID: "agent-a", Role: "researcher", Provider: "ollama", Model: "fake"}}
	cfg.Projects = []ProjectConfig{{ID: "project-a", RepoPath: t.TempDir()}}
	cfg.Freewill.PromiseConfirmation = FreewillPromiseConfirmationConfig{Enabled: true, OwnerID: "synthetic-owner", PromiseID: "promise-a", Scope: "review two files", Deadline: "2026-11-12T17:00:00Z"}
	if err := cfg.Freewill.ValidateHostBinding(cfg.Agents, cfg.Projects); err != nil {
		t.Fatalf("valid synthetic promise rejected: %v", err)
	}
	cfg.Freewill.PromiseConfirmation.Deadline = "not-a-date"
	if err := cfg.Freewill.ValidateHostBinding(cfg.Agents, cfg.Projects); err == nil {
		t.Fatal("invalid promise deadline accepted")
	}
	cfg.Freewill.PromiseConfirmation.Deadline = "2026-11-12T17:00:00Z"
	cfg.Freewill.PromiseConfirmation.Scope = strings.Repeat("x", 1001)
	if err := cfg.Freewill.ValidateHostBinding(cfg.Agents, cfg.Projects); err == nil {
		t.Fatal("oversized promise scope accepted")
	}
}
