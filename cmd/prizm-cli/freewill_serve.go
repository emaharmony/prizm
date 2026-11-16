package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/codexcli"
	"github.com/emaharmony/prizm/internal/provider/ollama"
	"github.com/emaharmony/prizm/internal/sandbox"
	"github.com/emaharmony/prizm/internal/tool"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
	"github.com/nats-io/nats.go"
)

const freewillScheduleSubject = "prizm.freewill.scheduled"

const freewillDirectOllamaCloudEnv = "PRIZM_FREEWILL_OLLAMA_CLOUD_DIRECT"
const freewillEventDiagnosticsEnv = "PRIZM_FREEWILL_EVENT_DIAGNOSTICS"

// takeOllamaAPIKey removes ambient Ollama credentials before serve can start
// any child process. The caller retains the value only if the exact direct
// Cloud selector is configured.
func takeOllamaAPIKey(getenv func(string) string, unsetenv func(string) error) (string, error) {
	key := strings.TrimSpace(getenv("OLLAMA_API_KEY"))
	if err := unsetenv("OLLAMA_API_KEY"); err != nil {
		return "", errors.New("prizm could not remove OLLAMA_API_KEY from process environment")
	}
	return key, nil
}

func usesDirectFreewillCloudSelector(cfg *orchestrator.Config) bool {
	if cfg == nil || !cfg.Freewill.Enabled || os.Getenv(freewillDirectOllamaCloudEnv) != "1" {
		return false
	}
	for _, agent := range cfg.Agents {
		if agent.ID != cfg.Freewill.AgentID || agent.Provider != "ollama" {
			continue
		}
		model := strings.TrimSpace(cfg.Freewill.Model)
		if model == "" {
			model = strings.TrimSpace(agent.Model)
		}
		return model == "glm-5.3-flash:cloud"
	}
	return false
}

type freewillServeRuntime struct {
	wake                 *multiagent.FreewillWakeService
	approvals            *multiagent.FreewillApprovalService
	store                *multiagent.SQLiteDurableRunStore
	control              *multiagent.FreewillOperatorControl
	childApprovals       *approval.Store
	sandboxConfig        sandbox.Config
	events               *event.SQLiteEventStore
	eventsPath           string
	runDir               string
	configPath           string
	schedulingEnabled    bool
	wakeSourcesEnabled   bool
	eventKinds           []string
	executionEnabled     bool
	readOnly             bool
	mu                   sync.Mutex
	started              bool
	closed               bool
	subs                 []*nats.Subscription
	cancel               context.CancelFunc
	done                 chan struct{}
	wakeHandlers         sync.WaitGroup
	beforeSubscribe      func()
	beforeWakeHandler    func()
	resumeMu             sync.Mutex
	resumingChildren     map[string]bool
	childApprovalMu      sync.Mutex
	goalProposals        *freewillGoalProposalSink
	strategyExperiments  *freewillStrategyExperimentSink
	diaryAnnotations     *freewillDiaryAnnotationSink
	diaryConsent         *freewillDiaryConsentController
	relationshipConsent  *freewillRelationshipConsentController
	promiseConfirmation  *freewillPromiseConfirmationSink
	preferenceChallenges *freewillPreferenceHypothesisSink
	taskSurpriseRecovery *freewillTaskSurpriseRecovery
}

type freewillReferenceExecutionDispatcher struct {
	runDir        string
	configPath    string
	approvals     *multiagent.FreewillApprovalService
	control       *multiagent.FreewillOperatorControl
	sandboxConfig sandbox.Config
}

func (d freewillReferenceExecutionDispatcher) Start(ctx context.Context, request multiagent.FreewillExecutionRequest) (multiagent.FreewillExecutionResult, error) {
	reconcile := func(ctx context.Context, childRunID string) error {
		return d.reconcileChild(ctx, request.ParentRunID, childRunID)
	}
	if d.sandboxConfig.Image == "" {
		return startFreewillReferenceRunWithExecutionControl(ctx, d.runDir, d.configPath, request, reconcile, d.control)
	}
	return startFreewillReferenceRunWithExecutionControl(ctx, d.runDir, d.configPath, request, reconcile, d.control, d.sandboxConfig)
}

func (d freewillReferenceExecutionDispatcher) reconcileChild(ctx context.Context, parentRunID, childRunID string) error {
	if d.approvals == nil {
		return errors.New("freewill: terminal child reconciler is unavailable")
	}
	result, err := freewillTerminalResultFromReferenceRun(ctx, d.runDir, childRunID)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < 40; attempt++ {
		_, err = d.approvals.ReconcileExecutionTerminal(ctx, parentRunID, result)
		if !errors.Is(err, multiagent.ErrFreewillDispatchNotAccepted) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
	return err
}

// openFreewillStatusRuntime exposes historical status only when Freewill is
// disabled. It never creates a database or constructs a provider/wake service.
func openFreewillStatusRuntime(cfg *orchestrator.Config, configPath string) (*freewillServeRuntime, error) {
	if cfg == nil {
		return nil, errors.New("freewill: host configuration is required")
	}
	dataDir := cfg.Prizm.DataDir
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(filepath.Dir(configPath), dataDir)
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("freewill: inspect existing status database: %w", err)
	}
	store, err := multiagent.OpenExistingSQLiteDurableRunStoreReadOnly(dbPath)
	if err != nil {
		return nil, err
	}
	statusReader, err := multiagent.NewFreewillApprovalStatusReader(store,
		approval.NewStore(resolveConfigPath(configPath, cfg.Prizm.RunsDir)))
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	return &freewillServeRuntime{approvals: statusReader, store: store, readOnly: true}, nil
}

func openFreewillServeRuntime(cfg *orchestrator.Config, providers *provider.ProviderRegistry, _ string, configPath, directCloudAPIKey string) (*freewillServeRuntime, error) {
	if cfg == nil || providers == nil || !cfg.Freewill.Enabled {
		return nil, errors.New("freewill: enabled host configuration and provider registry are required")
	}
	freewillCfg := cfg.Freewill
	if err := freewillCfg.ValidateHostBinding(cfg.Agents, cfg.Projects); err != nil {
		return nil, err
	}
	var projectPath string
	var projectStateFile string
	var projectContextFiles []string
	var projectContextSections map[string]string
	var projectRemoteContextProjectID string
	var projectRemoteContextRepoPath string
	var terminalFactsProjectID string
	var terminalFactsRepoPath string
	for _, project := range cfg.Projects {
		if project.ID == freewillCfg.ProjectID {
			projectPath = project.RepoPath
			projectStateFile = project.StateFile
			projectContextFiles = append([]string(nil), project.FreewillContextFiles...)
			if len(project.FreewillContextSections) > 0 {
				projectContextSections = make(map[string]string, len(project.FreewillContextSections))
				for path, heading := range project.FreewillContextSections {
					projectContextSections[path] = heading
				}
			}
			projectRemoteContextProjectID = project.FreewillRemoteContextProjectID
			projectRemoteContextRepoPath = project.FreewillRemoteContextRepoPath
			terminalFactsProjectID = project.FreewillTerminalFactsProjectID
			terminalFactsRepoPath = project.FreewillTerminalFactsRepoPath
			break
		}
	}
	if !filepath.IsAbs(projectPath) {
		return nil, errors.New("freewill: configured project repository path must be absolute")
	}
	if len(projectContextFiles) == 0 && len(projectContextSections) > 0 {
		return nil, errors.New("freewill: project context sections require an exact project file allowlist")
	}
	projectPath, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return nil, errors.New("freewill: configured project repository is unavailable")
	}
	projectInfo, err := os.Stat(projectPath)
	if err != nil || !projectInfo.IsDir() {
		return nil, errors.New("freewill: configured project repository is unavailable")
	}
	projectReadAuthorized := freewillProjectReadAuthorized(cfg, configPath, projectPath)
	var sandboxConfig sandbox.Config
	if freewillCfg.Execution.Enabled {
		sandboxConfig, err = freewillSandboxConfigFromEnvironment(projectPath)
		if err != nil {
			return nil, err
		}
	}
	var selectorAgent *orchestrator.AgentConfig
	for i := range cfg.Agents {
		if cfg.Agents[i].ID == freewillCfg.AgentID {
			selectorAgent = &cfg.Agents[i]
			break
		}
	}
	if selectorAgent == nil {
		return nil, errors.New("freewill: configured selector agent is unavailable")
	}
	model := strings.TrimSpace(freewillCfg.Model)
	if model == "" {
		model = selectorAgent.Model
	}
	chatProvider, err := resolveFreewillSelectorProvider(cfg, providers, *selectorAgent, model, directCloudAPIKey)
	if err != nil {
		return nil, fmt.Errorf("freewill: configured selector provider is unavailable: %w", err)
	}
	if !freewillLocalPersistenceProviderAllowed(freewillCfg, selectorAgent.Provider, model, cfg.Prizm.OllamaURL) {
		return nil, errors.New("freewill: local diary, task appraisal, and memory context require a local selector provider")
	}

	policyDir := freewillCfg.PolicyDir
	if policyDir == "" {
		policyDir = "policies"
	}
	if !filepath.IsAbs(policyDir) {
		policyDir = filepath.Join(filepath.Dir(configPath), policyDir)
	}
	policyRegistry := policy.NewRegistry()
	if _, err := policyRegistry.LoadFromDir(policyDir); err != nil {
		return nil, fmt.Errorf("freewill: configured policy directory is unavailable: %w", err)
	}

	dataDir := cfg.Prizm.DataDir
	if !filepath.IsAbs(dataDir) {
		dataDir = filepath.Join(filepath.Dir(configPath), dataDir)
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, fmt.Errorf("freewill: create durable data directory: %w", err)
	}
	dbPath := filepath.Join(dataDir, "freewill.db")
	store, err := multiagent.NewSQLiteDurableRunStore(dbPath)
	if err != nil {
		return nil, fmt.Errorf("freewill: open durable run store: %w", err)
	}
	events, err := event.NewSQLiteEventStore(dbPath)
	if err != nil {
		_ = store.Close()
		return nil, fmt.Errorf("freewill: open durable event store: %w", err)
	}
	graph, err := multiagent.CompatAdaptDefinition(multiagent.DefaultReferenceDefinition())
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, fmt.Errorf("freewill: compile non-dispatch graph guard: %w", err)
	}
	if strings.TrimSpace(cfg.Prizm.RunsDir) == "" {
		_ = events.Close()
		_ = store.Close()
		return nil, errors.New("freewill: durable claim root is empty")
	}
	runtime, err := multiagent.NewDurableRuntime(graph, unavailableRoleRunner{}, store,
		multiagent.FileRunClaimer{Root: resolveConfigPath(configPath, cfg.Prizm.RunsDir)}, events, multiagent.DurableRuntimeOptions{})
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, fmt.Errorf("freewill: compose durable runtime: %w", err)
	}
	approvalStore := approval.NewStore(resolveConfigPath(configPath, cfg.Prizm.RunsDir))
	policyApproval, err := multiagent.NewFreewillApprovalService(runtime, store,
		approvalStore, policy.NewEvaluator(policyRegistry), "freewill-policy")
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, err
	}
	var terminalFactSink *freewillTerminalFactMemorySink
	var taskReflectionSink *freewillTaskReflectionSink
	var goalProposals *freewillGoalProposalSink
	var strategyExperiments *freewillStrategyExperimentSink
	var diaryAnnotations *freewillDiaryAnnotationSink
	var diaryConsent *freewillDiaryConsentController
	var relationshipConsent *freewillRelationshipConsentController
	var promiseConfirmation *freewillPromiseConfirmationSink
	var preferenceChallenges *freewillPreferenceHypothesisSink
	var taskSurpriseRecovery *freewillTaskSurpriseRecovery
	var taskAppraisalSink *freewillTaskAppraisalSink
	if freewillTerminalFactsBindingMatches(freewillCfg.ProjectID, projectPath, terminalFactsProjectID, terminalFactsRepoPath) {
		factSink, err := newFreewillTerminalFactMemorySink(dataDir, freewillCfg.ProjectID, projectPath, events, freewillCfg.MemoryContext.OwnerID)
		if err != nil {
			_ = events.Close()
			_ = store.Close()
			return nil, fmt.Errorf("freewill: configure local terminal fact store: %w", err)
		}
		terminalFactSink = factSink
		policyApproval.SetTerminalFactSink(factSink)
		if freewillCfg.PromiseConfirmation.Enabled {
			promiseConfirmation, err = newFreewillPromiseConfirmationSink(factSink, freewillCfg.ProjectID, projectPath, selectorAgent.ID, freewillCfg.PromiseConfirmation)
			if err != nil {
				_ = events.Close()
				_ = store.Close()
				return nil, fmt.Errorf("freewill: configure local promise confirmation: %w", err)
			}
		}
		if freewillCfg.DiaryAnnotationsEnabled {
			diaryAnnotations, err = newFreewillDiaryAnnotationSink(factSink, freewillCfg.DiaryAnnotationsOwnerID, freewillCfg.ProjectID, projectPath, selectorAgent.ID)
			if err != nil {
				_ = events.Close()
				_ = store.Close()
				return nil, fmt.Errorf("freewill: configure local diary annotations: %w", err)
			}
		}
		if freewillCfg.DiaryEnabled {
			diarySink, err := newFreewillTerminalDiarySink(factSink, freewillChatRetrospectiveProvider{provider: chatProvider, model: model}, freewillCfg.DiaryOwnerID)
			if err != nil {
				_ = events.Close()
				_ = store.Close()
				return nil, fmt.Errorf("freewill: configure local diary retrospective: %w", err)
			}
			policyApproval.SetTerminalDiarySink(diarySink)
		}
		if freewillCfg.TaskAppraisalEnabled {
			taskAppraisalSink, err = newFreewillTaskAppraisalSink(factSink, freewillChatTaskAppraisalProvider{provider: chatProvider, model: model}, freewillCfg.TaskAppraisalOwnerID)
			if err != nil {
				_ = events.Close()
				_ = store.Close()
				return nil, fmt.Errorf("freewill: configure local task appraisal: %w", err)
			}
			policyApproval.SetTaskAppraisalSink(taskAppraisalSink)
			if freewillCfg.TaskSurprise.Enabled {
				taskSurpriseRecovery, err = newFreewillTaskSurpriseRecovery(taskAppraisalSink, store, approvalStore, freewillCfg.TaskSurprise,
					freewillCfg.TaskAppraisalOwnerID, freewillCfg.ProjectID, selectorAgent.ID, projectPath, projectPath, projectStateFile,
					freewillCfg.Objective, resolveConfigPath(configPath, cfg.Prizm.RunsDir))
				if err != nil {
					_ = events.Close()
					_ = store.Close()
					return nil, fmt.Errorf("freewill: configure local task surprise recovery: %w", err)
				}
			}
			if freewillCfg.GoalProposalEnabled {
				goalSink, err := newFreewillGoalProposalSink(taskAppraisalSink, freewillChatGoalProvider{provider: chatProvider, model: model}, freewillCfg.GoalProposalOwnerID, freewillCfg.ProjectID, projectPath, selectorAgent.ID)
				if err != nil {
					_ = events.Close()
					_ = store.Close()
					return nil, fmt.Errorf("freewill: configure local goal proposal: %w", err)
				}
				goalProposals = goalSink
			}
			if freewillCfg.StrategyExperimentEnabled {
				experimentSink, err := newFreewillStrategyExperimentSink(taskAppraisalSink, store, approvalStore, freewillCfg.StrategyExperimentOwnerID, projectPath, selectorAgent.ID,
					freewillCfg.StrategyExperimentIDs, freewillCfg.StrategyExperimentVersions, freewillCfg.StrategyExperimentPatchSHA256)
				if err != nil {
					_ = events.Close()
					_ = store.Close()
					return nil, fmt.Errorf("freewill: configure local strategy experiment: %w", err)
				}
				strategyExperiments = experimentSink
			}
			if freewillCfg.TaskReflectionEnabled {
				reflectionSink, err := newFreewillTaskReflectionSink(taskAppraisalSink, freewillChatTaskReflectionProvider{provider: chatProvider, model: model}, freewillCfg.TaskReflectionOwnerID)
				if err != nil {
					_ = events.Close()
					_ = store.Close()
					return nil, fmt.Errorf("freewill: configure local task reflection: %w", err)
				}
				taskReflectionSink = reflectionSink
				policyApproval.SetTaskReflectionSink(reflectionSink)
			}
			if freewillCfg.PreferenceHypothesisEnabled {
				preferenceSink, err := newFreewillPreferenceHypothesisSink(taskAppraisalSink, freewillChatPreferenceProvider{provider: chatProvider, model: model}, freewillCfg.PreferenceHypothesisOwnerID, freewillCfg.PreferenceHypothesisTaskIDs, freewillCfg.PreferenceHypothesisRetentionSeconds)
				if err != nil {
					_ = events.Close()
					_ = store.Close()
					return nil, fmt.Errorf("freewill: configure local preference hypothesis: %w", err)
				}
				policyApproval.SetPreferenceHypothesisSink(preferenceSink)
				preferenceChallenges = preferenceSink
			}
		}
	} else if freewillCfg.DiaryEnabled || freewillCfg.DiaryAnnotationsEnabled || freewillCfg.TaskAppraisalEnabled || freewillCfg.TaskSurprise.Enabled || freewillCfg.GoalProposalEnabled || freewillCfg.PreferenceHypothesisEnabled || freewillCfg.StrategyExperimentEnabled || freewillCfg.PromiseConfirmation.Enabled {
		_ = events.Close()
		_ = store.Close()
		return nil, errors.New("freewill: diary requires the exact terminal facts project and path binding")
	}
	control, err := multiagent.NewFreewillOperatorControl(store, nil)
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, fmt.Errorf("freewill: compose durable operator control: %w", err)
	}
	if freewillCfg.Execution.Enabled {
		policyApproval.SetExecutionDispatcher(freewillReferenceExecutionDispatcher{
			runDir: resolveConfigPath(configPath, cfg.Prizm.RunsDir), configPath: configPath,
			approvals: policyApproval, control: control, sandboxConfig: sandboxConfig,
		})
	}
	var priorReflection func(context.Context) (*multiagent.FreewillReflectionEvidence, error)
	if taskReflectionSink != nil {
		priorReflection = func(ctx context.Context) (*multiagent.FreewillReflectionEvidence, error) {
			prior, err := taskReflectionSink.ReadLatestOwnerProjectReflection(ctx, freewillCfg.TaskReflectionOwnerID, freewillCfg.ProjectID, projectPath, selectorAgent.ID)
			if prior != nil {
				prior.Workspace = projectPath
			}
			return prior, err
		}
	}
	var preferenceInfluenceSource multiagent.FreewillPreferenceInfluenceSource
	if freewillCfg.PreferenceInfluenceEnabled {
		preferenceInfluenceSource = preferenceChallenges
	}
	wake, err := multiagent.NewFreewillWakeService(runtime, store,
		multiagent.ChatFreewillSelector{Provider: chatProvider, AgentID: selectorAgent.ID, Model: model,
			PreferenceInfluenceEnabled: freewillCfg.PreferenceInfluenceEnabled, PreferenceInfluenceOwnerID: freewillCfg.PreferenceInfluenceOwnerID, PreferenceInfluenceSource: preferenceInfluenceSource,
			ReasoningEffort: freewillFirstNonEmpty(freewillCfg.ReasoningEffort, selectorAgent.ReasoningEffort)},
		multiagent.FreewillWakeServiceConfig{Enabled: true, SchedulingEnabled: freewillCfg.Scheduling.Enabled, EventKinds: freewillCfg.Scheduling.EventKinds,
			ExecutionEnabled:       freewillCfg.Execution.Enabled,
			PriorReflection:        priorReflection,
			PriorReflectionOwnerID: freewillCfg.TaskReflectionOwnerID,
			AgentID:                selectorAgent.ID, Scope: freewill.Scope{ProjectID: freewillCfg.ProjectID, Workspace: projectPath},
			Objective: freewillCfg.Objective, StrategyID: freewillCfg.StrategyVariantID, StrategyVersion: freewillCfg.StrategyVariantVersion, ExpectedPatchSHA256: freewillCfg.StrategyVariantPatchSHA256,
			Owner: "prizm-freewill-scheduler", LeaseFor: 2 * time.Minute, OperatorControl: control,
			ProjectContext: func(ctx context.Context) (freewill.ProjectContextSnapshot, error) {
				var snapshot freewill.ProjectContextSnapshot
				if !projectReadAuthorized {
					snapshot = freewill.NewProjectContextSnapshot(freewillCfg.ProjectID, freewillCfg.Objective, time.Now().UTC())
				} else if len(projectContextFiles) > 0 {
					snapshot, err = tool.BuildFreewillProjectContextForFiles(ctx, freewillCfg.ProjectID, freewillCfg.Objective, projectPath, projectContextFiles, projectContextSections)
					if err != nil {
						return freewill.ProjectContextSnapshot{}, err
					}
				} else {
					snapshot, err = tool.BuildFreewillProjectContext(ctx, freewillCfg.ProjectID, freewillCfg.Objective, projectPath, projectStateFile)
					if err != nil {
						return freewill.ProjectContextSnapshot{}, err
					}
				}
				if freewillCfg.ProjectProfileRepositoryURL != "" || freewillCfg.ProjectProfileCommitSHA != "" {
					if !projectReadAuthorized {
						return freewill.ProjectContextSnapshot{}, errors.New("freewill: private project profile requires authorized project read scope")
					}
					snapshot, err = tool.ApplyFreewillPrivateProfile(ctx, snapshot, projectPath, freewillCfg.ProjectProfileRepositoryURL, freewillCfg.ProjectProfileCommitSHA)
					if err != nil {
						return freewill.ProjectContextSnapshot{}, err
					}
				}
				if freewillCfg.MemoryContext.Enabled {
					if terminalFactSink == nil {
						return freewill.ProjectContextSnapshot{}, errors.New("freewill: opted-in project memory store is unavailable")
					}
					snapshot, err = appendFreewillOwnerMemoryContext(ctx, snapshot, terminalFactSink,
						freewillCfg.MemoryContext.OwnerID, freewillCfg.ProjectID, projectPath, selectorAgent.ID,
						freewillCfg.MemoryContext.SourceAllowlist)
					if err != nil {
						return freewill.ProjectContextSnapshot{}, err
					}
				}
				if freewillSelectorIsRemote(selectorAgent.Provider, model, cfg.Prizm.OllamaURL) && !freewillRemoteContextBindingMatches(
					freewillCfg.ProjectID, projectPath, projectRemoteContextProjectID, projectRemoteContextRepoPath) {
					snapshot.SelectorEgressAllowed = false
				}
				snapshot.Seal()
				return snapshot, nil
			}}, nil)
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, err
	}
	if diaryAnnotations != nil {
		diaryConsent, err = newFreewillDiaryConsentController(filepath.Join(dataDir, "freewill_diary_consent"), freewillCfg.DiaryAnnotationsOwnerID)
		if err != nil {
			_ = events.Close()
			_ = store.Close()
			return nil, fmt.Errorf("freewill: configure local diary consent store: %w", err)
		}
	}
	if cfg.API.LocalDiaryPrincipalAuth.Enabled {
		if diaryConsent == nil {
			_ = events.Close()
			_ = store.Close()
			return nil, errors.New("freewill: relationship consent requires the principal-bound diary reply store")
		}
		relationshipConsent, err = newFreewillRelationshipConsentController(diaryConsent)
		if err != nil {
			_ = diaryConsent.store.Close()
			_ = events.Close()
			_ = store.Close()
			return nil, fmt.Errorf("freewill: configure local relationship consent store: %w", err)
		}
	}
	return &freewillServeRuntime{wake: wake, approvals: policyApproval, store: store, control: control, events: events, eventsPath: dbPath, goalProposals: goalProposals, strategyExperiments: strategyExperiments, diaryAnnotations: diaryAnnotations, diaryConsent: diaryConsent, relationshipConsent: relationshipConsent, promiseConfirmation: promiseConfirmation, preferenceChallenges: preferenceChallenges, taskSurpriseRecovery: taskSurpriseRecovery,
		childApprovals: approvalStore, sandboxConfig: sandboxConfig,
		runDir: resolveConfigPath(configPath, cfg.Prizm.RunsDir), configPath: configPath, resumingChildren: make(map[string]bool),
		schedulingEnabled:  freewillCfg.Scheduling.Enabled,
		wakeSourcesEnabled: freewillCfg.Enabled && (freewillCfg.Scheduling.Enabled || len(freewillCfg.Scheduling.EventKinds) > 0),
		eventKinds:         append([]string(nil), freewillCfg.Scheduling.EventKinds...), executionEnabled: freewillCfg.Execution.Enabled}, nil
}

func freewillLocalPersistenceProviderAllowed(cfg orchestrator.FreewillConfig, providerName, model, endpoint string) bool {
	requiresLocal := cfg.DiaryEnabled || cfg.TaskAppraisalEnabled || cfg.TaskReflectionEnabled || cfg.PreferenceHypothesisEnabled || cfg.GoalProposalEnabled || cfg.StrategyExperimentEnabled || cfg.MemoryContext.Enabled || cfg.PromiseConfirmation.Enabled
	return !requiresLocal || !freewillSelectorIsRemote(providerName, model, endpoint)
}

func freewillRemoteContextBindingMatches(projectID, projectPath, approvedProjectID, approvedRepoPath string) bool {
	if strings.TrimSpace(projectID) == "" || projectID != approvedProjectID || strings.TrimSpace(approvedRepoPath) == "" {
		return false
	}
	approvedPath := approvedRepoPath
	if !filepath.IsAbs(approvedPath) {
		return false
	}
	resolved, err := filepath.EvalSymlinks(approvedPath)
	if err != nil {
		return false
	}
	projectResolved, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return false
	}
	left, err := filepath.Abs(projectResolved)
	if err != nil {
		return false
	}
	right, err := filepath.Abs(resolved)
	if err != nil {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func freewillSelectorIsRemote(providerName, model, endpoint string) bool {
	providerName = strings.ToLower(strings.TrimSpace(providerName))
	model = strings.ToLower(strings.TrimSpace(model))
	if providerName == "ollama" {
		if strings.HasSuffix(model, ":cloud") {
			return true
		}
		endpoint = strings.TrimSpace(endpoint)
		if endpoint == "" {
			return false // The configured default is the local Ollama daemon.
		}
		parsed, err := url.Parse(endpoint)
		if err != nil || parsed.Scheme != "http" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return true
		}
		host := strings.Trim(parsed.Hostname(), "[]")
		if strings.EqualFold(host, "localhost") {
			return false
		}
		ip := net.ParseIP(host)
		return ip == nil || !ip.IsLoopback()
	}
	return providerName != "" && providerName != "local"
}

func freewillProjectReadAuthorized(cfg *orchestrator.Config, configPath, projectPath string) bool {
	if cfg == nil {
		return false
	}
	readRoots := configuredReadRoots(cfg)
	workspace := cfg.Prizm.Workspace
	if strings.TrimSpace(workspace) != "" && !filepath.IsAbs(workspace) {
		workspace = resolveConfigPath(configPath, workspace)
	}
	if strings.TrimSpace(workspace) != "" {
		if _, err := tool.ResolveToolPath(tool.ToolPaths{WorkspaceRoot: workspace, AllowedPaths: readRoots}, projectPath); err == nil {
			return true
		}
	}
	if len(readRoots) == 0 {
		return false
	}
	_, err := tool.ResolveToolPath(tool.ToolPaths{WorkspaceRoot: readRoots[0], AllowedPaths: readRoots[1:]}, projectPath)
	return err == nil
}

func freewillSandboxConfigFromEnvironment(sourceRoot string) (sandbox.Config, error) {
	dockerPath := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_DOCKER"))
	if dockerPath == "" {
		dockerPath = "docker"
	}
	if _, err := exec.LookPath(dockerPath); err != nil {
		return sandbox.Config{}, errors.New("freewill: execution requires an available local OCI engine")
	}
	image := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_IMAGE"))
	if !pinnedSandboxImage(image) {
		return sandbox.Config{}, errors.New("freewill: execution requires a sandbox image pinned by sha256 digest")
	}
	workRoot := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_WORK_ROOT"))
	artifactRoot := strings.TrimSpace(os.Getenv("PRIZM_SUBAGENT_SANDBOX_ARTIFACT_ROOT"))
	if !filepath.IsAbs(workRoot) || !filepath.IsAbs(artifactRoot) || freewillSandboxPathsOverlap(sourceRoot, workRoot) || freewillSandboxPathsOverlap(sourceRoot, artifactRoot) {
		return sandbox.Config{}, errors.New("freewill: execution requires absolute sandbox storage roots outside the project")
	}
	return sandbox.Config{DockerPath: dockerPath, Image: image, WorkRoot: workRoot, ArtifactRoot: artifactRoot}, nil
}

func pinnedSandboxImage(image string) bool {
	name, digest, ok := strings.Cut(image, "@sha256:")
	if !ok || name == "" || len(digest) != 64 {
		return false
	}
	for _, char := range digest {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func freewillSandboxPathsOverlap(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr != nil || rightErr != nil {
		return true
	}
	leftRel, leftRelErr := filepath.Rel(leftAbs, rightAbs)
	rightRel, rightRelErr := filepath.Rel(rightAbs, leftAbs)
	return leftRelErr != nil || rightRelErr != nil || leftRel == "." || rightRel == "." ||
		(leftRel != ".." && !strings.HasPrefix(leftRel, ".."+string(filepath.Separator))) ||
		(rightRel != ".." && !strings.HasPrefix(rightRel, ".."+string(filepath.Separator)))
}

func sameFreewillSandboxConfig(left, right *sandbox.Config) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func (r *freewillServeRuntime) ListChildApprovals(ctx context.Context, parentRunID, childRunID string) ([]api.FreewillChildApproval, error) {
	if _, _, err := r.validateFreewillChild(ctx, parentRunID, childRunID, "", false); err != nil {
		return nil, err
	}
	items, err := r.childApprovals.List(childRunID)
	if err != nil {
		return nil, err
	}
	result := make([]api.FreewillChildApproval, 0, len(items))
	for _, item := range items {
		if item == nil || item.Status != approval.StatusPending || item.MutationType != approval.MutationApplyPatch ||
			item.PatchPlan == nil || item.SandboxBinding == nil {
			continue
		}
		projected, err := freewillChildApprovalProjection(parentRunID, childRunID, item)
		if err != nil {
			return nil, err
		}
		result = append(result, projected)
	}
	return result, nil
}

func (r *freewillServeRuntime) ListChildResourceExtensions(ctx context.Context, parentRunID, childRunID string) ([]api.FreewillChildResourceExtension, error) {
	manifest, err := r.validateFreewillResourceChild(ctx, parentRunID, childRunID)
	if err != nil {
		return nil, err
	}
	inspection, err := openInspectionReferenceRuntime(r.runDir, manifest)
	if err != nil {
		return nil, err
	}
	record, inspectErr := inspection.runtime.Inspect(ctx, childRunID)
	if inspectErr != nil {
		return nil, errors.Join(inspectErr, inspection.close())
	}
	closeErr := inspection.close()
	if closeErr != nil {
		return nil, closeErr
	}
	if record.ResourceNegotiation == nil {
		return []api.FreewillChildResourceExtension{}, nil
	}
	result := make([]api.FreewillChildResourceExtension, 0, len(record.ResourceNegotiation.Requests))
	for _, request := range record.ResourceNegotiation.Requests {
		item := api.FreewillChildResourceExtension{ParentRunID: parentRunID, ChildRunID: childRunID,
			RequestID: request.RequestID, Status: request.Status, Reason: request.Reason,
			CurrentAllowance: request.CurrentAllowance, RequestedIncrement: request.RequestedIncrement,
			HardCeiling: request.HardCeiling, FromRole: string(request.FromRole), NextRole: string(request.NextRole),
			GrantGeneration: request.GrantGeneration, Generation: request.Generation,
			CheckpointRevision: request.CheckpointRevision}
		for _, decision := range record.ResourceNegotiation.Decisions {
			if decision.RequestID == request.RequestID {
				item.Decision, item.Actor, item.DecidedAt = decision.Decision, decision.Actor, decision.DecidedAt
				break
			}
		}
		result = append(result, item)
	}
	if len(result) > 20 {
		result = result[len(result)-20:]
	}
	return result, nil
}

func (r *freewillServeRuntime) DecideChildResourceExtension(ctx context.Context, parentRunID, childRunID, requestID string, expectedRevision int64, decision, actor, reason string) (api.FreewillChildResourceExtension, error) {
	r.childApprovalMu.Lock()
	defer r.childApprovalMu.Unlock()
	manifest, err := r.validateFreewillResourceChild(ctx, parentRunID, childRunID)
	if err != nil {
		return api.FreewillChildResourceExtension{}, err
	}
	inspection, err := openInspectionReferenceRuntime(r.runDir, manifest)
	if err != nil {
		return api.FreewillChildResourceExtension{}, err
	}
	current, inspectErr := inspection.runtime.InspectResourceExtension(ctx, childRunID)
	if inspectErr != nil {
		return api.FreewillChildResourceExtension{}, errors.Join(inspectErr, inspection.close())
	}
	if current.RequestID != requestID || current.CheckpointRevision != expectedRevision {
		return api.FreewillChildResourceExtension{}, errors.Join(errors.New("freewill: resource request is stale"), inspection.close())
	}
	if current.Status == "pending" {
		if r.approvals == nil {
			return api.FreewillChildResourceExtension{}, errors.New("freewill: parent approval service is unavailable")
		}
		if err := r.approvals.ValidateAcceptedExecutionResume(ctx, parentRunID); err != nil {
			return api.FreewillChildResourceExtension{}, fmt.Errorf("freewill: parent controls block resource decision: %w", err)
		}
	}
	decisionReceipt, decisionErr := inspection.runtime.DecideResourceExtension(ctx, childRunID, requestID, expectedRevision, decision, actor, reason)
	closeErr := inspection.close()
	if err := errors.Join(decisionErr, closeErr); err != nil {
		return api.FreewillChildResourceExtension{}, err
	}
	if current.Status == "pending" && decisionReceipt.Decision == "accept" {
		if err := r.resumeAcceptedChild(ctx, parentRunID, childRunID); err != nil {
			return api.FreewillChildResourceExtension{}, err
		}
	}
	items, err := r.ListChildResourceExtensions(ctx, parentRunID, childRunID)
	if err != nil {
		return api.FreewillChildResourceExtension{}, err
	}
	for _, item := range items {
		if item.RequestID == requestID {
			return item, nil
		}
	}
	return api.FreewillChildResourceExtension{}, errors.New("freewill: resource decision receipt is unavailable")
}

func (r *freewillServeRuntime) validateFreewillResourceChild(ctx context.Context, parentRunID, childRunID string) (referenceWorkflowManifest, error) {
	if r == nil || r.readOnly || !r.executionEnabled || r.store == nil || r.approvals == nil ||
		strings.TrimSpace(parentRunID) == "" || strings.TrimSpace(childRunID) == "" || parentRunID == "." || parentRunID == ".." || childRunID == "." || childRunID == ".." || strings.ContainsAny(parentRunID+childRunID, `/\\`) {
		return referenceWorkflowManifest{}, errors.New("freewill: child resource service is unavailable")
	}
	parent, err := r.store.Load(ctx, parentRunID)
	if err != nil {
		return referenceWorkflowManifest{}, err
	}
	if parent.Freewill == nil || parent.Freewill.ApprovalReceipt == nil || parent.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted ||
		parent.Freewill.ExecutionDispatch == nil || parent.Freewill.ExecutionDispatch.ChildRunID != childRunID || parent.State.RequestIdentity == nil ||
		(parent.Freewill.ExecutionDispatch.Status != "accepted" && parent.Freewill.ExecutionDispatch.Status != "completed" &&
			parent.Freewill.ExecutionDispatch.Status != "execution_failed" && parent.Freewill.ExecutionDispatch.Status != "execution_cancelled") {
		return referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	manifest, err := loadReferenceManifest(r.runDir, childRunID)
	if err != nil || manifest.RunID != childRunID || !manifest.SandboxApprovalMode || manifest.FreewillSandboxConfig == nil ||
		manifest.SourceWorkspacePath == "" || manifest.WorkspacePath == "" || filepath.Clean(manifest.SourceWorkspacePath) != filepath.Clean(parent.Freewill.Scope.Workspace) ||
		manifest.ResourceNegotiation == nil || !manifest.ResourceNegotiation.Enabled {
		return referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	cfg := manifest.ResourceNegotiation
	if cfg.ParentRunID != parentRunID || cfg.AgentID != parent.State.RequestIdentity.RequesterID || cfg.ProjectID != parent.Freewill.Scope.ProjectID ||
		cfg.Workspace != parent.Freewill.Scope.Workspace || cfg.GrantGeneration != parent.Freewill.ApprovalGeneration {
		return referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	return manifest, nil
}

func (r *freewillServeRuntime) ListChildApprovalHistory(ctx context.Context, parentRunID, childRunID string) ([]api.FreewillChildApprovalDecision, error) {
	if r == nil || r.readOnly || !r.executionEnabled || r.store == nil || r.childApprovals == nil ||
		strings.TrimSpace(parentRunID) == "" || strings.TrimSpace(childRunID) == "" ||
		parentRunID == "." || parentRunID == ".." || childRunID == "." || childRunID == ".." ||
		strings.ContainsAny(parentRunID+childRunID, `/\\`) {
		return nil, errors.New("freewill: child approval history is unavailable")
	}
	parent, err := r.store.Load(ctx, parentRunID)
	if err != nil {
		return nil, err
	}
	if parent.Freewill == nil || parent.Freewill.ApprovalReceipt == nil || parent.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted ||
		parent.Freewill.ExecutionDispatch == nil || parent.Freewill.ExecutionDispatch.ChildRunID != childRunID ||
		(parent.Freewill.ExecutionDispatch.Status != "accepted" && parent.Freewill.ExecutionDispatch.Status != "completed" &&
			parent.Freewill.ExecutionDispatch.Status != "execution_failed" && parent.Freewill.ExecutionDispatch.Status != "execution_cancelled") {
		return nil, multiagent.ErrFreewillApprovalStale
	}
	manifest, err := loadReferenceManifest(r.runDir, childRunID)
	if err != nil || manifest.RunID != childRunID || !manifest.SandboxApprovalMode || manifest.FreewillSandboxConfig == nil ||
		manifest.SourceWorkspacePath == "" || manifest.WorkspacePath == "" ||
		filepath.Clean(manifest.SourceWorkspacePath) != filepath.Clean(parent.Freewill.Scope.Workspace) {
		return nil, multiagent.ErrFreewillApprovalStale
	}
	items, err := r.childApprovals.List(childRunID)
	if err != nil {
		return nil, err
	}
	result := make([]api.FreewillChildApprovalDecision, 0, len(items))
	for _, item := range items {
		if item == nil || item.RunID != childRunID || item.MutationType != approval.MutationApplyPatch || item.IdempotencyKey == "" || item.ValidateSubject() != nil ||
			(item.Status != approval.StatusApproved && item.Status != approval.StatusDenied) || item.PatchPlan == nil ||
			item.SandboxBinding == nil || item.SandboxBinding.AgentID != item.RequestedBy ||
			item.SandboxBinding.ExecutionKey != item.CorrelationID || item.SandboxBinding.WorkspacePath != manifest.WorkspacePath ||
			item.SandboxBinding.WorkspaceID != manifest.WorkspaceID || item.SandboxBinding.ArtifactDigest == "" ||
			len(item.PatchPlan.Paths) == 0 || len(item.PatchPlan.Paths) > 32 || len(item.PatchPlan.PatchSHA256) > 128 {
			continue
		}
		decision := api.FreewillChildApprovalDecision{ParentRunID: parentRunID, ChildRunID: childRunID,
			ApprovalID: item.ApprovalID, ProposalID: item.ProposalID, Status: string(item.Status),
			Paths: append([]string(nil), item.PatchPlan.Paths...), PatchSHA256: item.PatchPlan.PatchSHA256}
		if item.Status == approval.StatusApproved {
			decision.Decision, decision.Actor, decision.Reason = "grant", strings.TrimSpace(item.ApprovedBy), item.DecisionReason
			if item.ApprovedAt != nil {
				decision.DecidedAt = *item.ApprovedAt
			}
		} else {
			decision.Decision, decision.Actor, decision.Reason = "deny", strings.TrimSpace(item.DeniedBy), item.DenialReason
			if item.DeniedAt != nil {
				decision.DecidedAt = *item.DeniedAt
			}
		}
		if decision.Actor == "" || decision.DecidedAt.IsZero() || len(decision.Actor) > 128 || len(decision.Reason) > 256 {
			continue
		}
		validPaths := true
		for _, path := range decision.Paths {
			if strings.TrimSpace(path) == "" || len(path) > 512 {
				validPaths = false
				break
			}
		}
		if validPaths {
			result = append(result, decision)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].DecidedAt.Equal(result[j].DecidedAt) {
			return result[i].ApprovalID < result[j].ApprovalID
		}
		return result[i].DecidedAt.After(result[j].DecidedAt)
	})
	if len(result) > 20 {
		result = result[:20]
	}
	return result, nil
}

func (r *freewillServeRuntime) DecideChildApproval(ctx context.Context, parentRunID, childRunID, approvalID, decision, actor, reason string) (api.FreewillChildApproval, error) {
	if !r.executionEnabled || r.readOnly || r.childApprovals == nil || strings.TrimSpace(actor) == "" ||
		(decision != "grant" && decision != "deny") {
		return api.FreewillChildApproval{}, errors.New("freewill: child approval is not enabled")
	}
	r.childApprovalMu.Lock()
	defer r.childApprovalMu.Unlock()
	item, _, err := r.validateFreewillChild(ctx, parentRunID, childRunID, approvalID, true)
	if err != nil {
		return api.FreewillChildApproval{}, err
	}
	if item.Status != approval.StatusPending {
		return api.FreewillChildApproval{}, errors.New("freewill: child approval is no longer pending")
	}
	if decision == "grant" {
		if err := item.ApproveWithReason(actor, safeFreewillApprovalReason(reason)); err != nil {
			return api.FreewillChildApproval{}, err
		}
	} else if err := item.Deny(actor, safeFreewillApprovalReason(reason)); err != nil {
		return api.FreewillChildApproval{}, err
	}
	if err := r.childApprovals.Save(item); err != nil {
		return api.FreewillChildApproval{}, err
	}
	if err := r.resumeAcceptedChild(ctx, parentRunID, childRunID); err != nil {
		return api.FreewillChildApproval{}, err
	}
	return freewillChildApprovalProjection(parentRunID, childRunID, item)
}

func (r *freewillServeRuntime) validateFreewillChild(ctx context.Context, parentRunID, childRunID, approvalID string, requireApproval bool) (*approval.Approval, referenceWorkflowManifest, error) {
	if r == nil || r.readOnly || !r.executionEnabled || r.store == nil || r.childApprovals == nil ||
		strings.TrimSpace(parentRunID) == "" || strings.TrimSpace(childRunID) == "" {
		return nil, referenceWorkflowManifest{}, errors.New("freewill: child approval service is unavailable")
	}
	parent, err := r.store.Load(ctx, parentRunID)
	if err != nil {
		return nil, referenceWorkflowManifest{}, err
	}
	if parent.Freewill == nil || parent.Freewill.ApprovalReceipt == nil || parent.Freewill.ApprovalReceipt.Decision != multiagent.FreewillApprovalGranted ||
		parent.Freewill.ExecutionDispatch == nil || parent.Freewill.ExecutionDispatch.ChildRunID != childRunID ||
		parent.Freewill.ExecutionDispatch.Status != "accepted" {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	manifest, err := loadReferenceManifest(r.runDir, childRunID)
	if err != nil || manifest.RunID != childRunID || !manifest.SandboxApprovalMode || manifest.FreewillSandboxConfig == nil ||
		manifest.SourceWorkspacePath == "" || manifest.WorkspacePath == "" || filepath.Clean(manifest.SourceWorkspacePath) != filepath.Clean(parent.Freewill.Scope.Workspace) {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	inspection, err := openInspectionReferenceRuntime(r.runDir, manifest)
	if err != nil {
		return nil, referenceWorkflowManifest{}, err
	}
	child, inspectErr := inspection.runtime.Inspect(ctx, childRunID)
	closeErr := inspection.close()
	if inspectErr != nil || closeErr != nil {
		return nil, referenceWorkflowManifest{}, errors.Join(inspectErr, closeErr)
	}
	if child.State.Status.Terminal() || requireApproval && child.State.Status != multiagent.RunStatusPaused {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	if approvalID == "" {
		return nil, manifest, nil
	}
	item, err := r.childApprovals.Load(childRunID, approvalID)
	if err != nil {
		return nil, referenceWorkflowManifest{}, err
	}
	if item.RunID != childRunID || item.ApprovalID != approvalID || item.Status != approval.StatusPending ||
		item.MutationType != approval.MutationApplyPatch || item.PatchPlan == nil || item.SandboxBinding == nil ||
		item.SandboxBinding.AgentID != item.RequestedBy || item.SandboxBinding.ExecutionKey != item.CorrelationID ||
		item.SandboxBinding.WorkspacePath != manifest.WorkspacePath || item.SandboxBinding.WorkspaceID != manifest.WorkspaceID ||
		item.SandboxBinding.ArtifactDigest == "" {
		return nil, referenceWorkflowManifest{}, multiagent.ErrFreewillApprovalStale
	}
	return item, manifest, nil
}

func freewillChildApprovalProjection(parentRunID, childRunID string, item *approval.Approval) (api.FreewillChildApproval, error) {
	if item == nil || item.MutationType != approval.MutationApplyPatch || item.PatchPlan == nil || len(item.Content) > 1<<20 {
		return api.FreewillChildApproval{}, errors.New("freewill: child patch is unavailable or exceeds the review limit")
	}
	return api.FreewillChildApproval{ParentRunID: parentRunID, ChildRunID: childRunID, ApprovalID: item.ApprovalID,
		ProposalID: item.ProposalID, Status: string(item.Status), RequestedBy: item.RequestedBy,
		TargetPath: strings.Join(item.PatchPlan.Paths, ", "), Patch: item.Content,
		SandboxBinding: api.FreewillChildSandboxBinding{TaskID: item.SandboxBinding.TaskID,
			ExecutionKey: item.SandboxBinding.ExecutionKey, AgentID: item.SandboxBinding.AgentID,
			WorkspaceID: item.SandboxBinding.WorkspaceID, WorkspacePath: item.SandboxBinding.WorkspacePath,
			BaseSHA: item.SandboxBinding.BaseSHA, BaseTree: item.SandboxBinding.BaseTree,
			ArtifactDigest: item.SandboxBinding.ArtifactDigest},
		PatchPlan: api.FreewillChildPatchPlan{BaseSHA: item.PatchPlan.BaseSHA, BaseTree: item.PatchPlan.BaseTree,
			ExpectedTree: item.PatchPlan.ExpectedTree, PatchSHA256: item.PatchPlan.PatchSHA256,
			Paths: append([]string(nil), item.PatchPlan.Paths...)}}, nil
}

func safeFreewillApprovalReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if len(reason) > 256 {
		return reason[:256]
	}
	return reason
}

func resolveFreewillSelectorProvider(cfg *orchestrator.Config, providers *provider.ProviderRegistry, agent orchestrator.AgentConfig, model, directCloudAPIKey string) (provider.ChatProvider, error) {
	if cfg == nil || providers == nil {
		return nil, errors.New("selector configuration is incomplete")
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return nil, errors.New("selector model is required")
	}
	if len(agent.Fallbacks) != 0 {
		return nil, errors.New("selector fallback targets are not allowed")
	}
	switch agent.Provider {
	case "codex":
		if strings.TrimSpace(cfg.Codex.Profile) != "" {
			return nil, errors.New("codex selector profiles are not allowed")
		}
		if len(cfg.Codex.ExtraArgs) != 0 {
			return nil, errors.New("codex selector extra arguments are not allowed")
		}
		configuredModel := strings.TrimSpace(cfg.Codex.Model)
		if configuredModel == "" {
			configuredModel = model
		}
		if configuredModel != model {
			return nil, errors.New("codex selector model must match the configured Freewill model")
		}
		selectorCfg := codexcli.NormalizeConstrainedSelectorConfig(codexcli.ConstrainedSelectorConfig{
			Executable: cfg.Codex.Executable, Model: configuredModel,
			TimeoutMinutes: cfg.Codex.TimeoutMinutes, OutputSchema: multiagent.FreewillSelectionOutputSchema(),
		})
		if _, err := exec.LookPath(selectorCfg.Executable); err != nil {
			return nil, fmt.Errorf("codex CLI executable %q not found: %w", selectorCfg.Executable, err)
		}
		return codexcli.NewConstrainedSelector(selectorCfg)
	case "ollama":
		info, err := providers.ModelInfo(model)
		if err != nil {
			return nil, err
		}
		if info.ID != model || info.ProviderName != "ollama" {
			return nil, errors.New("selector model registry binding does not match Ollama")
		}
		if strings.TrimSpace(model) == "glm-5.3-flash:cloud" && os.Getenv(freewillDirectOllamaCloudEnv) == "1" {
			if strings.TrimSpace(directCloudAPIKey) == "" {
				return nil, errors.New("direct Ollama Cloud selector requires OLLAMA_API_KEY")
			}
			return ollama.NewDirectCloudChatProvider(directCloudAPIKey)
		}
		direct, err := providers.Get(model)
		if err != nil {
			return nil, err
		}
		chat, ok := direct.(provider.ChatProvider)
		if !ok {
			return nil, errors.New("configured Ollama selector does not support chat generation")
		}
		return chat, nil
	default:
		return nil, errors.New("selector provider must be codex or ollama")
	}
}

func (r *freewillServeRuntime) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return
	}
	r.closed = true
	subs, cancel, done := r.subs, r.cancel, r.done
	r.subs, r.cancel, r.done = nil, nil, nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	for _, sub := range subs {
		if sub != nil {
			_ = sub.Unsubscribe()
		}
	}
	if done != nil {
		<-done
	}
	r.wakeHandlers.Wait()
	if r.diaryConsent != nil && r.diaryConsent.store != nil {
		_ = r.diaryConsent.store.Close()
	}
	if r.relationshipConsent != nil && r.relationshipConsent.store != nil {
		_ = r.relationshipConsent.store.Close()
	}
	if r.events != nil {
		_ = r.events.Close()
	}
	if r.store != nil {
		_ = r.store.Close()
	}
}

func (r *freewillServeRuntime) Start(ctx context.Context, nc *nats.Conn, wakeSourcesEnabled bool) error {
	if r == nil {
		return errors.New("freewill: serve runtime is incomplete")
	}
	if r.readOnly {
		return nil
	}
	if os.Getenv(freewillEventDiagnosticsEnv) == "1" {
		digest := sha256.Sum256([]byte(strings.Join(r.eventKinds, "\n")))
		log.Printf("[FREEWILL-DIAG] event_subscription_config wake_sources=%t scheduling=%t count=%d subjects_sha256=%x", wakeSourcesEnabled, r.schedulingEnabled, len(r.eventKinds), digest)
	}
	if r.wake == nil || r.approvals == nil || (wakeSourcesEnabled && nc == nil) {
		return errors.New("freewill: serve runtime is incomplete")
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return errors.New("freewill: serve runtime is closed")
	}
	if r.started {
		r.mu.Unlock()
		return nil
	}
	// A configured wake must finish its durable decision even if the caller's
	// request context is canceled after the subscription has been installed.
	workerCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	r.started, r.cancel, r.done = true, cancel, make(chan struct{})
	done := r.done
	r.mu.Unlock()
	fail := func(err error) error {
		cancel()
		r.mu.Lock()
		r.started, r.cancel, r.done = false, nil, nil
		r.mu.Unlock()
		close(done)
		return err
	}
	if wakeSourcesEnabled {
		if err := nc.Flush(); err != nil {
			return fail(fmt.Errorf("freewill: NATS is not ready: %w", err))
		}
		if r.beforeSubscribe != nil {
			r.beforeSubscribe()
		}
		subs := make([]*nats.Subscription, 0, 1+len(r.eventKinds))
		if r.schedulingEnabled {
			sub, err := nc.Subscribe(freewillScheduleSubject, func(msg *nats.Msg) {
				if !r.beginWakeHandler() {
					return
				}
				defer r.wakeHandlers.Done()
				if r.beforeWakeHandler != nil {
					r.beforeWakeHandler()
				}
				select {
				case <-workerCtx.Done():
					return
				default:
				}
				if err := runFreewillScheduledTick(workerCtx, r.recoverPending, func(ctx context.Context) error {
					// The scheduler message carries no scope, identity, candidate, or policy
					// authority. Those values come only from the validated host config.
					run, runErr := r.wake.HandleScheduledTick(ctx, time.Now().UTC())
					if runErr != nil {
						return runErr
					}
					if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateProposed {
						if _, approvalErr := r.approvals.RequestApproval(ctx, run.State.RunID); approvalErr != nil {
							log.Printf("[FREEWILL] approval proposal could not be recorded: %v", approvalErr)
						}
					}
					return nil
				}); err != nil {
					log.Printf("[FREEWILL] scheduled tick deferred: %v", err)
				}
			})
			if err != nil {
				return fail(fmt.Errorf("freewill: subscribe to scheduled wake: %w", err))
			}
			subs = append(subs, sub)
		}
		for _, eventKind := range r.eventKinds {
			kind := eventKind
			sub, err := nc.Subscribe(kind, func(msg *nats.Msg) {
				diagnostics := os.Getenv(freewillEventDiagnosticsEnv) == "1"
				if diagnostics {
					log.Printf("[FREEWILL-DIAG] event_callback_entered")
				}
				if !r.beginWakeHandler() {
					if diagnostics {
						log.Printf("[FREEWILL-DIAG] event_callback_skipped reason=runtime_not_accepting")
					}
					return
				}
				defer r.wakeHandlers.Done()
				if r.beforeWakeHandler != nil {
					r.beforeWakeHandler()
				}
				select {
				case <-workerCtx.Done():
					if diagnostics {
						log.Printf("[FREEWILL-DIAG] event_callback_skipped reason=worker_context_canceled")
					}
					return
				default:
				}
				var incoming event.Event
				if err := json.Unmarshal(msg.Data, &incoming); err != nil || msg.Subject != kind || incoming.Type != kind {
					if diagnostics {
						log.Printf("[FREEWILL-DIAG] event_callback_rejected reason=invalid_envelope")
					} else {
						log.Printf("[FREEWILL] event wake ignored: subject/envelope mismatch or malformed event")
					}
					return
				}
				handleCalled := false
				handleErrorReason := "none"
				if err := runFreewillScheduledTick(workerCtx, r.recoverPending, func(ctx context.Context) error {
					handleCalled = true
					run, runErr := r.wake.HandleEvent(ctx, incoming)
					if runErr != nil {
						handleErrorReason = freewillEventErrorReason(runErr)
						return runErr
					}
					if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateProposed {
						if _, approvalErr := r.approvals.RequestApproval(ctx, run.State.RunID); approvalErr != nil {
							log.Printf("[FREEWILL] approval proposal could not be recorded: %v", approvalErr)
						}
					}
					return nil
				}); err != nil {
					if diagnostics {
						stage := "recovery"
						reason := freewillEventErrorReason(err)
						if handleCalled {
							stage, reason = "handle_event", handleErrorReason
						}
						log.Printf("[FREEWILL-DIAG] event_callback_deferred stage=%s reason=%s", stage, reason)
					} else {
						log.Printf("[FREEWILL] event-triggered wake deferred: %v", err)
					}
				} else if diagnostics {
					log.Printf("[FREEWILL-DIAG] event_callback_returned result=no_error")
				}
			})
			if err != nil {
				for _, prior := range subs {
					_ = prior.Unsubscribe()
				}
				return fail(fmt.Errorf("freewill: subscribe to event wake %q: %w", kind, err))
			}
			subs = append(subs, sub)
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			for _, sub := range subs {
				_ = sub.Unsubscribe()
			}
			cancel()
			close(done)
			return errors.New("freewill: serve runtime closed during start")
		}
		r.subs = subs
		r.mu.Unlock()
		if err := nc.Flush(); err != nil {
			for _, sub := range subs {
				_ = sub.Unsubscribe()
			}
			r.mu.Lock()
			r.subs = nil
			r.mu.Unlock()
			return fail(fmt.Errorf("freewill: configured wake subscription did not become ready: %w", err))
		}
		if os.Getenv(freewillEventDiagnosticsEnv) == "1" {
			digest := sha256.Sum256([]byte(strings.Join(r.eventKinds, "\n")))
			log.Printf("[FREEWILL-DIAG] event_subscriptions_ready count=%d subjects_sha256=%x", len(r.eventKinds), digest)
		}
	}
	go func() {
		defer close(done)
		if err := r.recoverPending(workerCtx); err != nil && workerCtx.Err() == nil {
			log.Printf("[FREEWILL] recovery scan failed: %v", err)
		}
	}()
	return nil
}

func freewillEventErrorReason(err error) string {
	switch {
	case errors.Is(err, multiagent.ErrFreewillDisabled):
		return "disabled"
	case errors.Is(err, multiagent.ErrFreewillEventNotAllowed):
		return "event_not_allowed"
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "other"
	}
}

// beginWakeHandler registers a NATS callback while holding the same mutex
// Close uses to stop future registrations. Once Close marks the runtime
// closed, no WaitGroup Add can race with its Wait.
func (r *freewillServeRuntime) beginWakeHandler() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	r.wakeHandlers.Add(1)
	return true
}

// runFreewillScheduledTick resumes durable accepted work before admitting a
// new intention. A recovery error defers this tick so a later message retries
// the same parent/child instead of overlapping it with new work.
func runFreewillScheduledTick(ctx context.Context, recoverPending func(context.Context) error, handleTick func(context.Context) error) error {
	if err := recoverPending(ctx); err != nil {
		return fmt.Errorf("recover accepted Freewill work before scheduled wake: %w", err)
	}
	return handleTick(ctx)
}

func (r *freewillServeRuntime) recoverPending(ctx context.Context) error {
	if r.preferenceChallenges != nil {
		if err := r.preferenceChallenges.ExpireDuePreferenceRecords(ctx); err != nil {
			return fmt.Errorf("recover expired preference records: %w", err)
		}
	}
	if r.wakeSourcesEnabled {
		if err := r.wake.RecoverPending(ctx, time.Now().UTC(), 500); err != nil {
			return err
		}
	}
	lister := multiagent.FreewillRunLister(r.store)
	after := ""
	for {
		page, err := lister.ListFreewillRuns(ctx, after, 500)
		if err != nil {
			return err
		}
		if len(page) == 0 {
			return nil
		}
		for _, record := range page {
			if record.Freewill != nil && record.Freewill.ExecutionDispatch != nil && record.Freewill.ExecutionDispatch.Status == "recovery_uncertain" {
				dispatch := record.Freewill.ExecutionDispatch
				if dispatch.RecoveryReason == "child_mutation_possible" {
					receipt, proven, reconcileErr := freewillReferenceChildReconcileNoEffectFailure(ctx, r.runDir, dispatch.ChildRunID)
					if reconcileErr != nil {
						return fmt.Errorf("freewill: inspect quarantined child %s no-effect evidence: %w", dispatch.ChildRunID, reconcileErr)
					}
					if !proven || r.approvals == nil {
						return fmt.Errorf("freewill: accepted child %s has unresolved mutation evidence and remains quarantined", dispatch.ChildRunID)
					}
					resolved, reconcileErr := r.approvals.ReconcileExecutionNoEffectFailure(ctx, record.State.RunID, receipt)
					if reconcileErr != nil {
						return fmt.Errorf("freewill: reconcile exact no-effect evidence for child %s: %w", dispatch.ChildRunID, reconcileErr)
					}
					record = resolved
				}
				if record.Freewill.ExecutionDispatch.Status == "recovery_uncertain" &&
					record.Freewill.ExecutionDispatch.RecoveryReason != "child_manifest_missing" && record.Freewill.ExecutionDispatch.RecoveryReason != "terminal_report_unavailable" &&
					record.Freewill.ExecutionDispatch.RecoveryReason != "terminal_report_invalid" {
					return fmt.Errorf("freewill: accepted child %s recovery remains uncertain and requires host evidence", dispatch.ChildRunID)
				}
				if record.Freewill.ExecutionDispatch.Status == "recovery_uncertain" {
					result, reconcileErr := freewillTerminalResultFromReferenceRun(ctx, r.runDir, dispatch.ChildRunID)
					if reconcileErr != nil {
						return fmt.Errorf("freewill: accepted child %s remains quarantined pending valid host terminal evidence: %w", dispatch.ChildRunID, reconcileErr)
					}
					if r.approvals == nil {
						return errors.New("freewill: approval service is unavailable for quarantined child reconciliation")
					}
					if _, reconcileErr = r.approvals.ReconcileExecutionTerminal(ctx, record.State.RunID, result); reconcileErr != nil {
						return fmt.Errorf("freewill: reconcile quarantined child %s from host terminal evidence: %w", dispatch.ChildRunID, reconcileErr)
					}
				}
			}
			if r.approvals != nil && record.Freewill != nil && record.Freewill.ExecutionDispatch != nil {
				switch record.Freewill.ExecutionDispatch.Status {
				case "completed", "execution_failed", "execution_cancelled":
					terminalFactErr := r.approvals.RetryTerminalFactCapture(ctx, record.State.RunID)
					if r.taskSurpriseRecovery != nil {
						if err := r.taskSurpriseRecovery.CaptureAfterTerminalFact(ctx, record.State.RunID); err != nil {
							code := freewillTaskSurpriseErrorCode(err)
							if code != "not_applicable" {
								log.Printf("[WARN] Freewill optional task surprise pass skipped (code=%s)", code)
							}
						}
						if err := r.taskSurpriseRecovery.CaptureContradictionAuditAfterTerminalFact(ctx, record.State.RunID); err != nil {
							code := freewillTaskSurpriseErrorCode(err)
							log.Printf("[WARN] Freewill optional task contradiction audit skipped (code=%s)", code)
						}
					}
					if terminalFactErr != nil {
						return terminalFactErr
					}
				case "mutation_no_effect_reconciled":
					if err := r.approvals.RetryTerminalFactCapture(ctx, record.State.RunID); err != nil {
						return err
					}
				}
			}
			if record.Freewill != nil && record.Freewill.Lifecycle == freewill.StateProposed {
				if _, err := r.approvals.RequestApproval(ctx, record.State.RunID); err != nil {
					return err
				}
			} else if record.Freewill != nil && (record.Freewill.Lifecycle == freewill.StateApprovalPending || (record.Freewill.Lifecycle == freewill.StatePaused && record.Freewill.PauseReason == "execution_disabled" && record.Freewill.ApprovalRef != "" && record.Freewill.ApprovalReceipt == nil)) {
				if _, err := r.approvals.RecoverApprovalDecision(ctx, record.State.RunID); err != nil {
					return err
				}
			}
			if r.executionEnabled {
				recovered, err := r.store.Load(ctx, record.State.RunID)
				if err != nil {
					return err
				}
				if recovered.Freewill != nil && recovered.Freewill.ApprovalReceipt != nil &&
					recovered.Freewill.ApprovalReceipt.Decision == multiagent.FreewillApprovalGranted {
					dispatchStatus := ""
					if recovered.Freewill.ExecutionDispatch != nil {
						dispatchStatus = recovered.Freewill.ExecutionDispatch.Status
					}
					switch dispatchStatus {
					case "accepted":
						if err := r.resumeAcceptedChild(ctx, recovered.State.RunID, recovered.Freewill.ExecutionDispatch.ChildRunID); err != nil {
							return err
						}
					case "completed", "execution_failed":
						// The terminal child outcome is already durable on the parent.
					case "execution_cancelled":
						// Operator halt permanently canceled this approved child.
					case "dispatching", "failed":
						if _, err := r.approvals.DispatchApproved(ctx, record.State.RunID); err != nil {
							return err
						}
					default:
						// A granted record without a durable dispatch marker was
						// decision-only; enabling execution later cannot dispatch it.
					}
				}
			}
		}
		after = page[len(page)-1].State.RunID
		if len(page) < 500 {
			return nil
		}
	}
}

func (r *freewillServeRuntime) resumeAcceptedChild(ctx context.Context, parentRunID, childRunID string) error {
	if strings.TrimSpace(r.runDir) == "" || strings.TrimSpace(r.configPath) == "" || strings.TrimSpace(childRunID) == "" {
		return errors.New("freewill: accepted child recovery paths are unavailable")
	}
	parent, err := r.store.Load(ctx, parentRunID)
	if err != nil {
		return fmt.Errorf("freewill: load accepted parent %s: %w", parentRunID, err)
	}
	if parent.Freewill == nil || parent.State.RequestIdentity == nil || parent.Freewill.ExecutionDispatch == nil ||
		parent.Freewill.ExecutionDispatch.Status != "accepted" || parent.Freewill.ExecutionDispatch.ChildRunID != childRunID {
		return multiagent.ErrFreewillApprovalStale
	}
	agentID, scope, generation := parent.State.RequestIdentity.RequesterID, parent.Freewill.Scope, parent.Freewill.ApprovalGeneration
	terminal, err := freewillReferenceChildIsTerminal(ctx, r.runDir, childRunID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && r.approvals != nil {
			_, quarantineErr := r.approvals.QuarantineExecutionRecovery(ctx, parentRunID, "child_manifest_missing")
			if quarantineErr != nil {
				return quarantineErr
			}
			return errors.New("freewill: accepted child manifest is missing; recovery quarantined")
		}
		return fmt.Errorf("freewill: inspect accepted child %s before recovery: %w", childRunID, err)
	}
	uncertain, err := freewillReferenceChildHasUnresolvedMutation(ctx, r.runDir, childRunID)
	if err != nil {
		return fmt.Errorf("freewill: inspect accepted child %s mutation ledger before recovery: %w", childRunID, err)
	}
	if uncertain {
		if r.approvals == nil {
			return errors.New("freewill: approval service is unavailable for recovery quarantine")
		}
		_, err := r.approvals.QuarantineExecutionRecovery(ctx, parentRunID, "child_mutation_possible")
		if err != nil {
			return err
		}
		return errors.New("freewill: accepted child has unresolved mutation evidence; recovery quarantined")
	}
	if terminal {
		if r.approvals == nil {
			return errors.New("freewill: approval service is unavailable for terminal child reconciliation")
		}
		result, resultErr := freewillTerminalResultFromReferenceRun(ctx, r.runDir, childRunID)
		if resultErr != nil {
			reason := "terminal_report_invalid"
			if errors.Is(resultErr, os.ErrNotExist) {
				reason = "terminal_report_unavailable"
			}
			_, quarantineErr := r.approvals.QuarantineExecutionRecovery(ctx, parentRunID, reason)
			if quarantineErr != nil {
				return errors.Join(resultErr, quarantineErr)
			}
			return fmt.Errorf("freewill: terminal child evidence is uncertain; recovery quarantined: %w", resultErr)
		}
		_, err := r.approvals.ReconcileExecutionTerminal(ctx, parentRunID, result)
		return err
	}
	if !terminal {
		if r.approvals == nil {
			return errors.New("freewill: approval service is unavailable for child recovery")
		}
		if err := r.approvals.ValidateAcceptedExecutionResume(ctx, parentRunID); err != nil {
			if !errors.Is(err, multiagent.ErrFreewillHalted) && !errors.Is(err, multiagent.ErrFreewillApprovalRevoked) {
				return fmt.Errorf("freewill: operator controls block resuming accepted child %s: %w", childRunID, err)
			}
			reason := "freewill approval generation revoked"
			if errors.Is(err, multiagent.ErrFreewillHalted) {
				reason = "freewill operator emergency halt"
			}
			_, cancelErr := cancelFreewillReferenceChild(ctx, r.runDir, childRunID, reason)
			if cancelErr != nil {
				return fmt.Errorf("freewill: durably cancel accepted child %s after control change: %w", childRunID, cancelErr)
			}
			// Resume consumes an interrupted child cancellation request through the
			// workflow's existing reconciliation path. If another process still
			// owns the claim, its registered context callback is already canceled.
			terminal = true
		}
	}
	if !terminal {
		pendingResource, inspectErr := freewillReferenceChildHasPendingResourceExtension(ctx, r.runDir, childRunID)
		if inspectErr != nil {
			return fmt.Errorf("freewill: inspect accepted child %s resource decision before recovery: %w", childRunID, inspectErr)
		}
		if pendingResource {
			// Do not claim the in-memory resume marker for an owner-decision wait.
			// The resource decision path will call recovery again after it durably
			// clears this exact wait, and must be able to claim the marker then.
			return nil
		}
	}
	r.resumeMu.Lock()
	if r.resumingChildren == nil {
		r.resumingChildren = make(map[string]bool)
	}
	if r.resumingChildren[childRunID] {
		r.resumeMu.Unlock()
		return nil
	}
	r.resumingChildren[childRunID] = true
	r.resumeMu.Unlock()
	if err := resumeFreewillReferenceChildWithExecutionControl(r.runDir, r.configPath, childRunID,
		func(ctx context.Context, actualChildRunID string) error {
			return (freewillReferenceExecutionDispatcher{runDir: r.runDir, configPath: r.configPath, approvals: r.approvals,
				control: r.control}).reconcileChild(ctx, parentRunID, actualChildRunID)
		}, func() {
			r.resumeMu.Lock()
			delete(r.resumingChildren, childRunID)
			r.resumeMu.Unlock()
		}, r.control, agentID, scope, generation); err != nil {
		r.resumeMu.Lock()
		delete(r.resumingChildren, childRunID)
		r.resumeMu.Unlock()
		return fmt.Errorf("freewill: recover accepted child for parent %s: %w", parentRunID, err)
	}
	return nil
}

func freewillReferenceChildHasUnresolvedMutation(ctx context.Context, runDir, childRunID string) (bool, error) {
	manifest, err := loadReferenceManifest(runDir, childRunID)
	if err != nil {
		return false, err
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return false, err
	}
	record, inspectErr := inspection.runtime.Inspect(ctx, childRunID)
	closeErr := inspection.close()
	if err := errors.Join(inspectErr, closeErr); err != nil {
		return false, err
	}
	unsafe := func(state *multiagent.RetryState) bool {
		return state != nil && (state.MutationPossible || state.UnresolvedMutationFailure || state.UnattributedMutation)
	}
	return unsafe(record.Retry), nil
}

func freewillReferenceChildReconcileNoEffectFailure(ctx context.Context, runDir, childRunID string) (multiagent.FreewillMutationNoEffectReceipt, bool, error) {
	manifest, err := loadReferenceManifest(runDir, childRunID)
	if err != nil {
		return multiagent.FreewillMutationNoEffectReceipt{}, false, err
	}
	if manifest.RunID != childRunID {
		return multiagent.FreewillMutationNoEffectReceipt{}, false, errors.New("freewill: child manifest identity does not match accepted run")
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return multiagent.FreewillMutationNoEffectReceipt{}, false, err
	}
	receipt, reconcileErr := inspection.runtime.ReconcileChildMutationNoEffectFailure(ctx, childRunID)
	closeErr := inspection.close()
	if errors.Is(reconcileErr, multiagent.ErrMutationNoEffectUnproven) {
		return multiagent.FreewillMutationNoEffectReceipt{}, false, errors.Join(closeErr)
	}
	if err := errors.Join(reconcileErr, closeErr); err != nil {
		return multiagent.FreewillMutationNoEffectReceipt{}, false, err
	}
	return receipt, true, nil
}

func freewillReferenceChildHasPendingResourceExtension(ctx context.Context, runDir, childRunID string) (bool, error) {
	manifest, err := loadReferenceManifest(runDir, childRunID)
	if err != nil {
		return false, err
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return false, err
	}
	record, inspectErr := inspection.runtime.Inspect(ctx, childRunID)
	closeErr := inspection.close()
	if err := errors.Join(inspectErr, closeErr); err != nil {
		return false, err
	}
	return record.Waiting != nil && record.Waiting.Kind == "resource_extension", nil
}

func cancelFreewillReferenceChild(ctx context.Context, runDir, childRunID, reason string) (multiagent.RunState, error) {
	manifest, err := loadReferenceManifest(runDir, childRunID)
	if err != nil {
		return multiagent.RunState{}, err
	}
	inspection, err := openInspectionReferenceRuntime(runDir, manifest)
	if err != nil {
		return multiagent.RunState{}, err
	}
	state, cancelErr := inspection.runtime.Cancel(ctx, childRunID, reason)
	return state, errors.Join(cancelErr, inspection.close())
}

func freewillFirstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func resolveConfigPath(configPath, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(filepath.Dir(configPath), path))
}
