package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/policy"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/provider/codexcli"
	multiagent "github.com/emaharmony/prizm/internal/workflow/multiagent"
	"github.com/nats-io/nats.go"
)

const freewillScheduleSubject = "prizm.freewill.scheduled"

type freewillServeRuntime struct {
	wake              *multiagent.FreewillWakeService
	approvals         *multiagent.FreewillApprovalService
	store             *multiagent.SQLiteDurableRunStore
	control           *multiagent.FreewillOperatorControl
	events            *event.SQLiteEventStore
	schedulingEnabled bool
	readOnly          bool
	mu                sync.Mutex
	started           bool
	closed            bool
	sub               *nats.Subscription
	cancel            context.CancelFunc
	done              chan struct{}
	beforeSubscribe   func()
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

func openFreewillServeRuntime(cfg *orchestrator.Config, providers *provider.ProviderRegistry, _ string, configPath string) (*freewillServeRuntime, error) {
	if cfg == nil || providers == nil || !cfg.Freewill.Enabled {
		return nil, errors.New("freewill: enabled host configuration and provider registry are required")
	}
	freewillCfg := cfg.Freewill
	if err := freewillCfg.ValidateHostBinding(cfg.Agents, cfg.Projects); err != nil {
		return nil, err
	}
	var projectPath string
	for _, project := range cfg.Projects {
		if project.ID == freewillCfg.ProjectID {
			projectPath = project.RepoPath
			break
		}
	}
	if !filepath.IsAbs(projectPath) {
		return nil, errors.New("freewill: configured project repository path must be absolute")
	}
	projectPath, err := filepath.EvalSymlinks(projectPath)
	if err != nil {
		return nil, errors.New("freewill: configured project repository is unavailable")
	}
	projectInfo, err := os.Stat(projectPath)
	if err != nil || !projectInfo.IsDir() {
		return nil, errors.New("freewill: configured project repository is unavailable")
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
	chatProvider, err := resolveFreewillSelectorProvider(cfg, providers, *selectorAgent, model)
	if err != nil {
		return nil, fmt.Errorf("freewill: configured selector provider is unavailable: %w", err)
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
	policyApproval, err := multiagent.NewFreewillApprovalService(runtime, store,
		approval.NewStore(resolveConfigPath(configPath, cfg.Prizm.RunsDir)), policy.NewEvaluator(policyRegistry), "freewill-policy")
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, err
	}
	control, err := multiagent.NewFreewillOperatorControl(store, nil)
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, fmt.Errorf("freewill: compose durable operator control: %w", err)
	}
	wake, err := multiagent.NewFreewillWakeService(runtime, store,
		multiagent.ChatFreewillSelector{Provider: chatProvider, AgentID: selectorAgent.ID, Model: model,
			ReasoningEffort: freewillFirstNonEmpty(freewillCfg.ReasoningEffort, selectorAgent.ReasoningEffort)},
		multiagent.FreewillWakeServiceConfig{Enabled: true, SchedulingEnabled: freewillCfg.Scheduling.Enabled,
			AgentID: selectorAgent.ID, Scope: freewill.Scope{ProjectID: freewillCfg.ProjectID, Workspace: projectPath},
			Owner: "prizm-freewill-scheduler", LeaseFor: 2 * time.Minute, OperatorControl: control}, nil)
	if err != nil {
		_ = events.Close()
		_ = store.Close()
		return nil, err
	}
	return &freewillServeRuntime{wake: wake, approvals: policyApproval, store: store, control: control, events: events,
		schedulingEnabled: freewillCfg.Scheduling.Enabled}, nil
}

func resolveFreewillSelectorProvider(cfg *orchestrator.Config, providers *provider.ProviderRegistry, agent orchestrator.AgentConfig, model string) (provider.ChatProvider, error) {
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
	sub, cancel, done := r.sub, r.cancel, r.done
	r.sub, r.cancel, r.done = nil, nil, nil
	r.mu.Unlock()
	if sub != nil { _ = sub.Unsubscribe() }
	if cancel != nil { cancel() }
	if done != nil { <-done }
	if r.events != nil {
		_ = r.events.Close()
	}
	if r.store != nil {
		_ = r.store.Close()
	}
}

func (r *freewillServeRuntime) Start(ctx context.Context, nc *nats.Conn, schedulingEnabled bool) error {
	if r == nil {
		return errors.New("freewill: serve runtime is incomplete")
	}
	if r.readOnly {
		return nil
	}
	if r.wake == nil || r.approvals == nil || (schedulingEnabled && nc == nil) {
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
	workerCtx, cancel := context.WithCancel(ctx)
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
	if schedulingEnabled {
		if err := nc.Flush(); err != nil {
			return fail(fmt.Errorf("freewill: NATS is not ready: %w", err))
		}
		if r.beforeSubscribe != nil {
			r.beforeSubscribe()
		}
		sub, err := nc.Subscribe(freewillScheduleSubject, func(msg *nats.Msg) {
			select {
			case <-workerCtx.Done():
				return
			default:
			}
			if err := r.recoverPending(workerCtx); err != nil {
				log.Printf("[FREEWILL] recovery scan failed: %v", err)
			}
			// The scheduler message carries no scope, identity, candidate, or policy
			// authority. Those values come only from the validated host config.
			run, runErr := r.wake.HandleScheduledTick(ctx, time.Now().UTC())
			if runErr != nil {
				log.Printf("[FREEWILL] scheduled wake failed: %v", runErr)
				return
			}
			if run.Freewill != nil && run.Freewill.Lifecycle == freewill.StateProposed {
				if _, approvalErr := r.approvals.RequestApproval(ctx, run.State.RunID); approvalErr != nil {
					log.Printf("[FREEWILL] approval proposal could not be recorded: %v", approvalErr)
				}
			}
		})
		if err != nil {
			return fail(fmt.Errorf("freewill: subscribe to scheduled wake: %w", err))
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			_ = sub.Unsubscribe()
			cancel()
			close(done)
			return errors.New("freewill: serve runtime closed during start")
		}
		r.sub = sub
		r.mu.Unlock()
		if err := nc.Flush(); err != nil {
			_ = sub.Unsubscribe()
			return fail(fmt.Errorf("freewill: scheduled wake subscription did not become ready: %w", err))
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

func (r *freewillServeRuntime) recoverPending(ctx context.Context) error {
	if r.schedulingEnabled {
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
			if record.Freewill != nil && record.Freewill.Lifecycle == freewill.StateProposed {
				if _, err := r.approvals.RequestApproval(ctx, record.State.RunID); err != nil {
					return err
				}
			} else if record.Freewill != nil && (record.Freewill.Lifecycle == freewill.StateApprovalPending || (record.Freewill.Lifecycle == freewill.StatePaused && record.Freewill.PauseReason == "execution_disabled" && record.Freewill.ApprovalRef != "" && record.Freewill.ApprovalReceipt == nil)) {
				if _, err := r.approvals.RecoverApprovalDecision(ctx, record.State.RunID); err != nil {
					return err
				}
			}
		}
		after = page[len(page)-1].State.RunID
		if len(page) < 500 {
			return nil
		}
	}
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
