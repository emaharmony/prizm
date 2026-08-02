// Package main implements the `prism serve` subcommand — the persistent daemon
// that runs Prism as a live service.
//
// Usage:
//
//	prism serve [--config prism.yaml] [--port 8321]
//
// The serve command:
//  1. Loads prism.yaml configuration
//  2. Starts the embedded NATS server
//  3. Registers agents from config
//  4. Starts the session manager
//  5. Sets up the agent router
//  6. Connects channel adapters (Discord, etc.)
//  7. Registers action triggers
//  8. Starts the health check server
//  9. Blocks until SIGINT/SIGTERM
//
// V20-respond: The serve command now wires the full conversation pipeline:
// Discord message → debounce → route → session → LLM → response → Discord.
package main

import (
	ctxcontext "context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/emaharmony/prism/internal/action"
	"github.com/emaharmony/prism/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prism/internal/agent"
	"github.com/emaharmony/prism/internal/api"
	"github.com/emaharmony/prism/internal/approval"
	"github.com/emaharmony/prism/internal/autopatch"
	"github.com/emaharmony/prism/internal/bus"
	"github.com/emaharmony/prism/internal/claudecli"
	"github.com/emaharmony/prism/internal/claudeworker"
	"github.com/emaharmony/prism/internal/codesummary"
	"github.com/emaharmony/prism/internal/codexworker"
	"github.com/emaharmony/prism/internal/commitments"
	"github.com/emaharmony/prism/internal/context"
	"github.com/emaharmony/prism/internal/cost"
	"github.com/emaharmony/prism/internal/crossprism"
	"github.com/emaharmony/prism/internal/dashboard"
	"github.com/emaharmony/prism/internal/debounce"
	"github.com/emaharmony/prism/internal/delegation"
	"github.com/emaharmony/prism/internal/factory"
	"github.com/emaharmony/prism/internal/factorymonitor"
	"github.com/emaharmony/prism/internal/governance"
	"github.com/emaharmony/prism/internal/guard"
	"github.com/emaharmony/prism/internal/improve"
	"github.com/emaharmony/prism/internal/orchestrator"
	"github.com/emaharmony/prism/internal/plan"
	"github.com/emaharmony/prism/internal/provider"
	"github.com/emaharmony/prism/internal/provider/anthropic"
	"github.com/emaharmony/prism/internal/provider/claudecode"
	"github.com/emaharmony/prism/internal/provider/codexcli"
	"github.com/emaharmony/prism/internal/provider/gemini"
	"github.com/emaharmony/prism/internal/provider/ollama"
	"github.com/emaharmony/prism/internal/provider/openai"
	"github.com/emaharmony/prism/internal/remembrance"
	"github.com/emaharmony/prism/internal/router"
	"github.com/emaharmony/prism/internal/runtrack"
	"github.com/emaharmony/prism/internal/safety"
	"github.com/emaharmony/prism/internal/scheduler"
	"github.com/emaharmony/prism/internal/session"
	"github.com/emaharmony/prism/internal/skill"
	"github.com/emaharmony/prism/internal/stage"
	"github.com/emaharmony/prism/internal/state"
	"github.com/emaharmony/prism/internal/task"
	"github.com/emaharmony/prism/internal/tool"
	"github.com/emaharmony/prism/internal/tool/mcp"
	"github.com/emaharmony/prism/internal/usage"
	"github.com/emaharmony/prism/internal/workflow/multiagent"

	"github.com/nats-io/nats.go"
)

// natsPublisherAdapter wraps a nats.Conn to implement stage.NatsPublisher.
type natsPublisherAdapter struct {
	conn *nats.Conn
}

func (n *natsPublisherAdapter) Publish(subject string, data []byte) error {
	if n.conn == nil {
		return fmt.Errorf("NATS not connected")
	}
	return n.conn.Publish(subject, data)
}

// discordBotClient abstracts the Discord operations needed by the handler,
// making it testable without a real Discord connection.
type discordBotClient interface {
	Typing(channelID string) error
	Send(msg *discordbot.OutboundMessage) error
	SendPlaceholder(channelID, content string) (string, error)
	EditMessage(channelID, messageID, content string) error
	SelfID() string
	GetRecentMessages(channelID string, limit int) []discordbot.RecentMessage
}

// conversationContext holds all the dependencies needed to process a
// Discord message through the full pipeline. It's closed over by the
// OnMessage handler so each message has access to routing, sessions,
// LLM providers, and the Discord bot for responses.
type conversationContext struct {
	router        *router.Router
	sessMgr       *session.Manager
	cfg           *orchestrator.Config
	providers     *provider.ProviderRegistry
	bot           discordBotClient
	debounce      *debounce.Tracker
	eventLog      *runtrack.EventLogger
	cancelReg     *runtrack.CancelRegistry
	ctxBuilder    *context.Builder         // V21: workspace context injection
	natsConn      *nats.Conn               // V21: NATS bus connection for event publishing
	natsURL       string                   // V21: NATS bus URL
	actionReg     *action.Registry         // V21: action registry for event-triggered actions
	remClient     *remembrance.Client      // V21: Remembrance client for memory auto-save
	remSem        chan struct{}            // V21: Semaphore limiting concurrent Remembrance goroutines (max 4)
	remCache      *remembranceCache        // V26: TTL cache for BuildContext results
	summarySem    chan struct{}            // Long-running codebase summary concurrency guard
	delegEngine   *delegation.Engine       // V22: Delegation engine for agent-to-agent task delegation
	taskStore     *task.Store              // V22: Task store for delegation tracking
	crossCoord    *crossprism.Coordinator  // Cross-Prism NATS delegation coordinator
	autopatcher   *autopatch.Service       // Diagnose-and-propose patch tasks
	toolExec      *tool.Executor           // V27: Tool executor for file system access
	stateMgr      *state.Manager           // V32: Working state manager for adaptive context
	planMgr       *plan.Manager            // V32: Plan manager for plan-first pipeline
	improveMgr    *improve.Manager         // V32: Self-improvement loop
	guardian      *guard.Guard             // V32: Guard rail for plan enforcement
	toolPolicy    *tool.PolicyConfig       // V27: Tool policy configuration (pointer so free mode can mutate it live)
	rateLimiter   *safety.UserRateLimiter  // V28: Per-user rate limiting
	toolGate      *stage.ToolRelevanceGate // P-008: Tool relevance gate
	commitStore   *commitments.Store       // V61: Commitments store for promise tracking
	pendingWorkMu sync.Mutex
	pendingWork   map[string]pendingWorkStart

	// Cached static system content — built once, reused every message.
	staticSystemText string // For text-based provider path
	staticSystemChat string // For ChatProvider path (includes toolUsageGuidance)
	hasSoulContent   bool   // True when SOUL.md identity was loaded — SOUL.md takes precedence over postfix
}

func executeServe(args []string) {
	serveCmd := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := serveCmd.String("config", "prism.yaml", "Path to prism.yaml configuration file")
	portFlag := serveCmd.Int("port", 0, "Health check server port (default: prism.port from config, then 8321)")
	busURL := serveCmd.String("bus-url", "", "NATS bus URL (empty = embedded)")
	// Same flag name and default every other subcommand that touches
	// multi-agent/workflow run output uses (see main.go's runCmd/workflowRunCmd
	// etc. and cmd_workflow_multiagent.go's runDir parameter) — the dashboard's
	// read-only multi-agent endpoints (GET /api/v1/multiagent/runs...) locate
	// runs under this same directory.
	runDir := serveCmd.String("run-dir", "./runs", "Directory for run outputs")
	// PR6: the multi-agent workflow definition registry's own SQLite
	// database, separate from any single run's per-run multiagent.db. Not
	// derived from *runDir at flag-declaration time (flag defaults must be
	// static string literals; *runDir isn't populated until Parse() below
	// runs) — a caller overriding --run-dir should override this too if they
	// want it colocated.
	definitionsDBPath := serveCmd.String("definitions-db", "./runs/multiagent_definitions.db", "Path to the multi-agent workflow definition registry database")

	serveCmd.Parse(args)

	fmt.Println("🔮 Starting Prism...")

	// 1. Load configuration
	cfg, err := orchestrator.LoadConfig(*configPath)

	// V22: Task store and delegation engine (hoisted for API server)
	var (
		taskStore   *task.Store
		delegEngine *delegation.Engine
		orch        *orchestrator.Orchestrator
		remClient   *remembrance.Client
		codexWorker *codexworker.Worker
	)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: config file %q not found. Create one with 'prism init' or specify --config\n", *configPath)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Override NATS URL if provided
	if *busURL != "" {
		cfg.Prism.NATSURL = *busURL
	}

	// Port precedence: --port flag → prism.port from config → 8321. Clients
	// (prism status/watch) build URLs from cfg.Prism.Port, so the server must
	// honor the same knob or they point at the wrong port.
	servePort := *portFlag
	if servePort == 0 {
		servePort = cfg.Prism.Port
	}
	if servePort == 0 {
		servePort = 8321
	}

	fmt.Printf("  Agents: %v\n", agentNames(cfg))
	fmt.Printf("  Primary: %s\n", primaryName(cfg))
	fmt.Printf("  Sessions: max=%d, idle=%dm, compaction=%s\n",
		cfg.Sessions.MaxContextMessages,
		cfg.Sessions.IdleTimeoutMinutes,
		cfg.Sessions.CompactionStrategy,
	)

	// 2. Start embedded NATS
	natsURL := cfg.Prism.NATSURL
	var natsCleanup func()
	if natsURL == "" {
		url, cleanup, err := bus.StartEmbeddedBus(0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting embedded bus: %v\n", err)
			os.Exit(1)
		}
		natsURL = url
		natsCleanup = cleanup
		fmt.Printf("  NATS: embedded at %s\n", natsURL)
	} else {
		fmt.Printf("  NATS: external at %s\n", natsURL)
	}

	// 2b. Connect to NATS
	natsConn, err := bus.ConnectToBus(natsURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error connecting to NATS: %v\n", err)
		os.Exit(1)
	}
	defer natsConn.Close()
	fmt.Println("  NATS: connected")

	// 3. Register agents
	agentReg := agent.NewRegistry()
	if err := cfg.RegisterAgents(agentReg); err != nil {
		fmt.Fprintf(os.Stderr, "Error registering agents: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Registered %d agents\n", len(cfg.Agents))

	orch, err = orchestrator.New(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating orchestrator: %v\n", err)
		os.Exit(1)
	}
	orch.Agents = agentReg

	// 4. Set up LLM providers from agent configs
	provReg := provider.NewProviderRegistry()
	if err := registerProviders(cfg, provReg); err != nil {
		fmt.Fprintf(os.Stderr, "Error registering providers: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  Providers: %d models registered\n", len(providerModelIDs(provReg)))

	// 5. Start session manager
	if err := os.MkdirAll(cfg.Prism.DataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating data directory: %v\n", err)
		os.Exit(1)
	}
	// Apply any per-model pricing overrides from config so cost estimates
	// reflect current provider prices without a recompile.
	cost.SetPricingOverrides(cfg.CostPricingOverrides())

	// Token-usage tracker: persist every LLM call routed through the registry so
	// the dashboard can graph usage over time and surface where tokens go.
	usageStore, err := usage.NewStore(filepath.Join(cfg.Prism.DataDir, "usage.db"))
	if err != nil {
		fmt.Printf("  Warning: usage tracker failed: %v\n", err)
		usageStore = nil
	} else {
		provReg.SetUsageRecorder(usage.NewRecorder(usageStore))
		defer usageStore.Close()
		fmt.Println("  Usage tracker: ready")
	}

	dbPath := filepath.Join(cfg.Prism.DataDir, "sessions.db")
	sessMgr, err := session.NewManager(
		dbPath,
		cfg.Sessions.MaxContextMessages,
		time.Duration(cfg.Sessions.IdleTimeoutMinutes)*time.Minute,
		cfg.Sessions.DailyResetHour,
		cfg.Sessions.CompactionStrategy,
		session.WithPersistence(cfg.Sessions.Persistence),
		session.WithResumeAfterIdle(cfg.Sessions.ResumeAfterIdle),
		session.WithKeepArchivedMessages(cfg.Sessions.KeepArchivedMessages),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error starting session manager: %v\n", err)
		os.Exit(1)
	}
	defer sessMgr.Close()
	fmt.Println("  Session manager: ready")

	taskStore, err = task.NewStore(filepath.Join(cfg.Prism.DataDir, "tasks.db"))
	if err != nil {
		fmt.Printf("  Warning: task store failed: %v\n", err)
	} else {
		delegEngine = delegation.NewEngine(taskStore, natsConn)
		fmt.Println("  Task store: ready")
	}

	commitStore := func() *commitments.Store {
		s, e := commitments.NewStoreFromPath(filepath.Join(cfg.Prism.DataDir, "commitments.db"))
		if e != nil {
			fmt.Printf("  Warning: commitments store failed: %v\n", e)
			return nil
		}
		fmt.Println("  Commitments: ready")
		return s
	}()
	if cfg.Codex.Enabled {
		codexCfg := codexConfigFromOrchestrator(cfg.Codex, cfg)
		codexWorker, err = codexworker.New(codexCfg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error configuring Codex worker: %v\n", err)
			os.Exit(1)
		}
		if delegEngine != nil {
			if err := delegEngine.Subscribe("codex", func(ctx ctxcontext.Context, t *task.Task) error {
				result, runErr := codexWorker.RunTask(ctx, t.ID, t.Description, t.Context)
				if runErr != nil {
					return runErr
				}
				return delegEngine.Complete(ctxcontext.Background(), t.ID, result)
			}); err != nil {
				fmt.Fprintf(os.Stderr, "Error subscribing Codex worker: %v\n", err)
				os.Exit(1)
			}
		}
		fmt.Println("  Codex worker: enabled")
	}

	autopatcher := buildAutoPatchService(cfg, taskStore, provReg, natsConn)
	if autopatcher != nil && autopatcher.Enabled() {
		fmt.Println("  Autopatch: enabled")
		startAutoPatchValidationSubscriber(autopatcher, natsConn)
	}

	// 6. Set up agent router
	rtr := router.New(agentReg, cfg)
	fmt.Println("  Router: ready")

	// 7. Set up action registry
	actionReg := action.NewRegistry()
	for _, a := range cfg.Actions {
		act := action.Action{
			Trigger: a.Trigger,
			Action:  a.Action,
			Enabled: a.Enabled,
		}
		if err := actionReg.RegisterAction(act); err != nil {
			fmt.Fprintf(os.Stderr, "Error registering action: %v\n", err)
			os.Exit(1)
		}
	}
	fmt.Printf("  Registered %d actions\n", len(cfg.Actions))

	// 8. Set up debounce, event logger, and cancel registry
	msgDebounce := debounce.New(
		debounce.WithInterval(500*time.Millisecond),
		debounce.WithOnDrop(func(key string) {
			log.Printf("[DEBOUNCE] dropped message from %s", key)
		}),
	)
	eventLog := &runtrack.EventLogger{}
	cancelReg := runtrack.NewCancelRegistry()

	// 9. Connect channel adapters (Discord, etc.)
	ctx, cancel := ctxcontext.WithCancel(ctxcontext.Background())
	defer cancel()

	var crossCoord *crossprism.Coordinator
	var crossSvc *crossprism.Service
	if cfg.Bridge.Enabled {
		secret := bridgeSecret(cfg)
		if secret == "" {
			fmt.Fprintf(os.Stderr, "Error: bridge enabled but %s is not set and bridge.secret is empty\n", cfg.Bridge.SecretEnv)
			os.Exit(1)
		}
		allowedSubjects := cfg.Bridge.AllowedSubjects
		if len(allowedSubjects) == 0 {
			allowedSubjects = crossprism.DefaultSubjects()
		}

		var factoryOp *factory.Operator
		if cfg.Bridge.Factory.Enabled {
			factoryOp, err = factory.NewOperator(factoryConfigFromBridge(cfg.Bridge.Factory))
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error configuring Roblox Factory bridge: %v\n", err)
				os.Exit(1)
			}
		}

		crossCoord = crossprism.NewCoordinator(crossprism.CoordinatorConfig{
			InstanceID:             cfg.Prism.InstanceID,
			LeaderInstance:         cfg.Bridge.LeaderInstance,
			Secret:                 secret,
			NATS:                   natsConn,
			Store:                  taskStore,
			FactoryAdapter:         factoryOp,
			CodexAdapter:           codexWorker,
			TargetProfiles:         crossProfilesFromBridge(cfg.Bridge.TargetProfiles),
			ConfidenceThreshold:    cfg.Bridge.ConfidenceThreshold,
			MaxClarificationRounds: cfg.Bridge.MaxClarificationRounds,
			ReportOnly:             true,
		})

		crossSvc, err = crossprism.NewService(natsConn, crossprism.ServiceConfig{
			InstanceID:      cfg.Prism.InstanceID,
			Secret:          secret,
			AllowedSubjects: allowedSubjects,
			MaxAge:          5 * time.Minute,
		}, func(ctx ctxcontext.Context, subject string, msg crossprism.Message) (*crossprism.Message, error) {
			return crossCoord.Handle(ctx, subject, msg)
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating cross-Prism bridge: %v\n", err)
			os.Exit(1)
		}
		if err := crossSvc.Start(ctx); err != nil {
			fmt.Fprintf(os.Stderr, "Error starting cross-Prism bridge: %v\n", err)
			os.Exit(1)
		}
		defer crossSvc.Close()
		fmt.Printf("  Cross-Prism: %s listening on %d signed subject(s)\n", cfg.Prism.InstanceID, len(allowedSubjects))
	}

	var discordBots []*discordbot.BotAdapter
	var factoryMon *factorymonitor.Monitor

	// V32: State manager, context builder, and plan manager — shared across all channels
	var stateMgr *state.Manager
	var ctxBuildr *context.Builder
	var planMgr *plan.Manager
	var improveMgr *improve.Manager
	// V35: Tool registry and executor — hoisted for wake handler access
	var toolReg *tool.Registry
	var skillReg *skill.Registry
	var toolExec *tool.Executor

	for _, ch := range cfg.Channels {
		switch ch.Type {
		case "discord":
			bot := discordbot.NewBotAdapter(ch.Token)

			// V21: Build workspace context injection
			ctxBuildr = nil
			if cfg.Prism.Workspace != "" {
				ctxBuildr = context.NewBuilder(cfg.Prism.Workspace)
			} else {
				// Default: use home directory + .openclaw/workspace
				ctxBuildr = context.NewBuilder(filepath.Join(os.Getenv("HOME"), ".openclaw", "workspace"))
			}

			// V21: Create Remembrance client if enabled
			if cfg.Remembrance.Enabled {
				remClient = remembrance.NewClientWithTimeout(
					cfg.Remembrance.URL,
					remembranceTimeout(cfg),
				)
				if remClient.IsAvailable() {
					fmt.Println("  Remembrance: connected")
				} else {
					log.Printf("[WARN] Remembrance enabled but not reachable at %s", cfg.Remembrance.URL)
					remClient = nil // Disable gracefully
				}
			}

			// V22: Register agent subscriptions against the shared task store.
			if delegEngine != nil {
				// Register agent subscriptions
				for i := range cfg.Agents {
					a := &cfg.Agents[i]
					for _, sub := range a.Subscriptions {
						agentID := a.ID
						// Subscribe this agent to its configured NATS subjects
						// The handler runs the agent's pipeline when a task.created event arrives
						handler := func(agentID string, sub string) func(ctxcontext.Context, *task.Task) error {
							return func(ctx ctxcontext.Context, t *task.Task) error {
								log.Printf("[DELEGATION] agent %s received task %s via %s (type: %s)", agentID, t.ID, sub, t.Type)
								// Task processing will be wired in M3.1d (DelegationStage)
								return nil
							}
						}(agentID, sub)
						if err := delegEngine.Subscribe(agentID, handler); err != nil {
							log.Printf("[WARN] failed to subscribe agent %s to %s: %v", agentID, sub, err)
						}
					}
				}
			}

			// V27/V28: Set up tool executor with full tool suite
			workspaceRoot := cfg.Prism.Workspace
			if workspaceRoot == "" {
				workspaceRoot = "."
			}

			readRoots := configuredReadRoots(cfg)
			writeRoots := configuredWriteRoots(cfg)

			toolReg = tool.NewRegistry()
			tool.RegisterBuiltinsWithRoots(toolReg, workspaceRoot, 10*1024*1024, readRoots, writeRoots) // all read-only + project tools
			toolReg.Register(&tool.WriteFileProposal{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})
			toolReg.Register(&tool.CreateDirectoryProposal{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})
			// V35: Direct write tool for autonomous wake actions (auto-approved via policy)
			toolReg.Register(&tool.WriteFileDirect{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})
			toolReg.Register(&tool.CreateDirectoryDirect{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})
			// V28: Git mutation tools (require approval)
			protectedBranch := cfg.ProtectedBranch()
			toolReg.Register(&tool.GitAddTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}})
			toolReg.Register(&tool.GitCommitTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}, ProtectedBranch: protectedBranch})
			toolReg.Register(&tool.GitPushTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}, ProtectedBranch: protectedBranch})
			toolReg.Register(&tool.GitCreatePRTool{})
			// V32: State management tools
			// V60: Shell tool for free mode (registered with tier_1 policy for gated mode)
			shellTool := &tool.ShellTool{
				Policy:         tool.BuildShellPolicyFromConfig("tier_1", cfg.Shell.Allowlists, cfg.Shell.Defaults.BlockedPatterns),
				DefaultTimeout: cfg.Shell.Defaults.TimeoutSeconds,
				MaxOutputBytes: cfg.Shell.Defaults.MaxOutputBytes,
				MaxStderrBytes: cfg.Shell.Defaults.MaxOutputBytes / 2,
			}
			toolReg.Register(shellTool)

			stateMgr = state.NewManager(workspaceRoot)
			stateMgr.EnsureDir()
			tool.RegisterStateTools(toolReg, stateMgr)

			// V32: Plan-First Pipeline tools
			planMgr = plan.NewManager(workspaceRoot)
			planMgr.EnsureDir()
			tool.RegisterPlanTools(toolReg, planMgr)

			// Gated loop: RESEARCH-phase tools (web_search + memory_search).
			// Pass the Remembrance client only when available so memory_search
			// reports "disabled" instead of dereferencing a nil client.
			var memSearcher tool.MemorySearcher
			if remClient != nil {
				memSearcher = remClient
			}
			tool.RegisterResearchTools(toolReg, memSearcher, tool.WebSearchConfig{})

			// Researcher reference-image tools: fetch/generate/analyze/collect.
			// Images save under <workspace>/references by default and may target
			// configured write roots via output_dir.
			tool.RegisterImageTools(toolReg, imageToolsConfigFromPrismConfig(cfg, workspaceRoot, writeRoots))

			// V34: Cross-Prism bridge tool — send messages to remote Prism instances
			if crossSvc != nil {
				toolReg.Register(tool.NewSendCrossMessageTool(crossSvc))
			}

			// V49: External MCP tool servers — register their tools into the
			// policy-gated registry so agents can use them like any built-in.
			if specs := mcpServerSpecs(cfg); len(specs) > 0 {
				for _, res := range mcp.RegisterServers(ctx, toolReg, specs, mcp.ProcessClientFactory) {
					if res.Err != nil {
						fmt.Printf("  MCP %s: error: %v\n", res.Server, res.Err)
						continue
					}
					fmt.Printf("  MCP %s: %d tool(s) registered\n", res.Server, len(res.Tools))
				}
			}

			// V54: Skills — discover SKILL.md skills (Claude Code / OpenClaw) under
			// the workspace and expose them via the use_skill tool + prompt.
			skillReg = skill.NewRegistry()
			if n, serr := skillReg.LoadDefault(workspaceRoot); n > 0 || serr != nil {
				if serr != nil {
					fmt.Printf("  Skills: %d loaded (%v)\n", n, serr)
				} else {
					fmt.Printf("  Skills: %d loaded\n", n)
				}
			}
			tool.RegisterSkillTool(toolReg, skillReg)

			// V32: Self-Improvement Loop
			improveMgr = improve.NewManager(workspaceRoot)
			improveMgr.EnsureDir()

			// V32: Guard rail (plan-first enforcement)
			guardian := guard.NewGuard(planMgr, ctxBuildr)

			toolPolicy := tool.DefaultPolicyConfig()
			// Mutation operations require approval
			toolPolicy.MaxFileSize = 10 * 1024 * 1024 // 10MB for serve mode
			toolPolicy.WorkspaceRoot = workspaceRoot
			toolPolicy.AllowedPaths = cfg.Prism.AllowedPaths
			toolPolicy.ReadRoots = readRoots
			toolPolicy.WriteRoots = writeRoots
			toolPolicy.OrchestratorAgentID = configuredOrchestratorAgentID(cfg)
			toolPolicy.AutoApproveMCP = cfg.MCPAutoApprove // unattended MCP execution (default off)
			// V61: Load governance docs and populate frozen paths in tool policy
			govLoader := governance.NewLoader(cfg.Prism.Workspace, nil)
			govLoader.Load()
			for _, doc := range govLoader.Docs() {
				for _, fp := range doc.Frontmatter.Governance.FrozenPaths {
					toolPolicy.FrozenPaths = append(toolPolicy.FrozenPaths, fp)
					reason := doc.Frontmatter.Governance.Reason
					if reason == "" {
						reason = fmt.Sprintf("Path %s is frozen per %s", fp, doc.Name)
					}
					if toolPolicy.FrozenPathReasons == nil {
						toolPolicy.FrozenPathReasons = make(map[string]string)
					}
					toolPolicy.FrozenPathReasons[fp] = reason
				}
			}
			toolExec = tool.NewExecutor(toolReg, &toolPolicy)
			toolExec.SetApprovalStore(approval.NewStore(cfg.Prism.RunsDir))
			toolExec.SetEmitter(func(eventType, source string, payload map[string]any) {
				log.Printf("[TOOL-EVENT] %s: %v", eventType, payload)
				// Forward file approval requests to the Discord channel
				if eventType == "prism.approval.file_requested" {
					approvalID, _ := payload["approval_id"].(string)
					runID, _ := payload["run_id"].(string)
					targetPath, _ := payload["target_path"].(string)
					agentName, _ := payload["agent"].(string)
					preview, _ := payload["preview"].(string)
					if approvalID != "" && runID != "" {
						// Send approval card to the channel where the conversation is happening
						// The channel ID is passed via the payload if available
						channelID, _ := payload["_channel_id"].(string)
						if channelID != "" && bot != nil {
							sendFileApprovalCard(bot, channelID, approvalID, runID, targetPath, agentName, preview)
						}
					}
				}
			})

			convCtx := &conversationContext{
				router:      rtr,
				sessMgr:     sessMgr,
				cfg:         cfg,
				providers:   provReg,
				bot:         bot,
				debounce:    msgDebounce,
				eventLog:    eventLog,
				cancelReg:   cancelReg,
				ctxBuilder:  ctxBuildr,
				natsConn:    natsConn,
				natsURL:     natsURL,
				actionReg:   actionReg,
				remClient:   remClient,
				remSem:      make(chan struct{}, 4),
				remCache:    newRemembranceCache(60 * time.Second),
				summarySem:  make(chan struct{}, 1),
				delegEngine: delegEngine,
				taskStore:   taskStore,
				crossCoord:  crossCoord,
				autopatcher: autopatcher,
				toolExec:    toolExec,
				toolPolicy:  &toolPolicy,
				rateLimiter: safety.NewUserRateLimiter(
					10, // max 10 messages per burst per user
					1,  // refill 1 token/sec per user
					60, // global max 60 concurrent requests
					10, // global refill 10 tokens/sec
				),
				toolGate:    stage.NewToolRelevanceGate(true), // P-008: enabled by default
				commitStore: commitStore,
				stateMgr:    stateMgr,   // V32: shared state manager (same instance as tools)
				planMgr:     planMgr,    // V32: plan manager
				improveMgr:  improveMgr, // V32: improvement manager
				guardian:    guardian,   // V32: guard rail
				pendingWork: make(map[string]pendingWorkStart),
			}

			// Pre-build static system content for all agents
			for _, a := range cfg.Agents {
				convCtx.rebuildStaticSystemContent(&a)
			}
			bot.OnMessage(func(msg *discordbot.InboundMessage) {
				convCtx.handleDiscordMessage(msg)
			})
			// Rich approval cards: a button click publishes the same feedback-response
			// the typed approve/changes/reject commands do.
			if natsConn != nil {
				nc := natsConn
				bot.OnButton(func(customID, userID, userName string) {
					log.Printf("[BUTTON] clicked: customID=%q user=%q(%s)", customID, userName, userID)

					// File approval buttons (prismapprove: prefix)
					if approvalID, runID, action, ok := decodeFileApprovalButtonID(customID); ok {
						log.Printf("[BUTTON] file approval: approvalID=%s runID=%s action=%s", approvalID, runID, action)
						store := approval.NewStore(cfg.Prism.RunsDir)
						a, err := store.Load(runID, approvalID)
						if err != nil {
							log.Printf("[BUTTON] file approval: failed to load approval: %v", err)
							return
						}
						if action == "approve" {
							// Write the file to disk
							if err := os.MkdirAll(filepath.Dir(a.TargetPath), 0755); err != nil {
								log.Printf("[BUTTON] file approval: mkdir failed: %v", err)
								return
							}
							if err := os.WriteFile(a.TargetPath, []byte(a.Content), 0644); err != nil {
								log.Printf("[BUTTON] file approval: write failed: %v", err)
								return
							}
							a.Status = approval.StatusApproved
							a.ApprovedBy = firstNonEmptyCommandArg(userName, userID, "discord")
							store.Save(a)
							log.Printf("[BUTTON] file approval: APPROVED and written to %s by %s", a.TargetPath, a.ApprovedBy)
						} else if action == "deny" {
							a.Status = approval.StatusDenied
							a.ApprovedBy = firstNonEmptyCommandArg(userName, userID, "discord")
							store.Save(a)
							log.Printf("[BUTTON] file approval: DENIED by %s", a.ApprovedBy)
						}
						return
					}

					// Workflow feedback buttons (prismfb: prefix)
					payload, ok := feedbackButtonPayload(customID, firstNonEmptyCommandArg(userName, userID, "discord"))
					if !ok {
						log.Printf("[BUTTON] payload decode failed for customID=%q", customID)
						return
					}
					data, mErr := json.Marshal(payload)
					if mErr != nil {
						log.Printf("[BUTTON] marshal failed: %v", mErr)
						return
					}
					if pErr := nc.Publish("prism.workflow.feedback.response", data); pErr != nil {
						log.Printf("[BUTTON] NATS publish failed: %v", pErr)
						return
					}
					log.Printf("[BUTTON] published feedback response for workflow %s: decision=%s", payload["workflow_id"], payload["decision"])
				})
			}

			go func() {
				if err := bot.Start(ctx); err != nil {
					log.Printf("Discord bot error: %v", err)
				}
			}()

			discordBots = append(discordBots, bot)
			fmt.Printf("  Discord: connecting\n")

		default:
			fmt.Fprintf(os.Stderr, "Warning: unknown channel type %q\n", ch.Type)
		}
	}

	// 10. Start API server
	var approvalMgr *delegation.ApprovalManager
	var delegTracker *delegation.Tracker
	if taskStore != nil && delegEngine != nil {
		approvalMgr = delegation.NewApprovalManager(taskStore, delegEngine)
		delegTracker = delegation.NewTracker(taskStore, delegEngine, delegation.TrackerConfig{
			TaskTimeout:   10 * time.Minute,
			CheckInterval: 1 * time.Minute,
		})
		go delegTracker.Start(ctxcontext.Background())
	}

	apiPort := servePort + 1 // API on port+1 (default 8322)
	// Serve the dashboard UI from the API server so config/cron editing is
	// same-origin (no separate `prism dashboard` process, no CORS).
	var staticUI http.Handler
	if h, uiErr := dashboard.StaticFileServer(); uiErr != nil {
		log.Printf("[WARN] dashboard UI unavailable: %v", uiErr)
	} else {
		staticUI = h
	}

	// PR6: the multi-agent workflow definition registry. A failure to open
	// it is logged, not fatal — every /api/v1/multiagent/definitions... route
	// degrades to 503 (definitionStore == nil), matching how other optional
	// subsystems in this server already degrade rather than blocking startup.
	var definitionStore *multiagent.DefinitionStore
	if store, defErr := multiagent.NewDefinitionStore(*definitionsDBPath); defErr != nil {
		log.Printf("[WARN] multi-agent definition registry unavailable: %v", defErr)
	} else {
		definitionStore = store
	}
	graphRunWorkspace := cfg.Prism.Workspace
	if strings.TrimSpace(graphRunWorkspace) == "" {
		graphRunWorkspace = "."
	}

	apiServer := api.NewServer(api.Config{
		Addr:                 cfg.BindAddr(apiPort),
		Orch:                 orch,
		Store:                taskStore,
		Sessions:             sessMgr,
		Engine:               delegEngine,
		Approval:             approvalMgr,
		Tracker:              delegTracker,
		AutoPatch:            autopatcher,
		NATS:                 natsConn,
		Providers:            provReg,
		InvokeIdleTimeout:    time.Duration(cfg.Sessions.InvokeIdleTimeoutHours) * time.Hour,
		AuthToken:            cfg.API.ResolveAuthToken(),
		AllowedOrigins:       cfg.API.AllowedOrigins,
		ConfigDir:            filepath.Dir(*configPath),
		WorkflowConfigPath:   cfg.Prism.WorkflowConfig,
		ConfigPath:           *configPath,
		SchedulerActions:     schedulerActionList(),
		StaticUI:             staticUI,
		Usage:                usageStore,
		UsageWindows:         usageWindowsFromConfig(cfg),
		Workspace:            cfg.Prism.Workspace,
		MultiAgentRuns:       multiagent.RunLocator{Root: *runDir, DefinitionStore: definitionStore},
		MultiAgentController: newReferenceMultiAgentController(*runDir, *configPath),
		DefinitionStore:      definitionStore,
		WorkflowRunStarter:   newGraphRunStarter(*runDir, *definitionsDBPath, *configPath, graphRunWorkspace),

		MaxRequestBytes:       cfg.API.MaxRequestBytes,
		MaxWorkspaceFileBytes: cfg.API.MaxWorkspaceFileBytes,
	})
	go func() {
		if err := apiServer.Start(); err != nil {
			log.Printf("[WARN] API server failed: %v", err)
		}
	}()
	displayHost := cfg.Prism.BindHost
	if strings.TrimSpace(displayHost) == "" {
		displayHost = "127.0.0.1"
	}
	fmt.Printf("  API:    http://%s:%d/api/v1/status\n", displayHost, apiPort)
	if staticUI != nil {
		fmt.Printf("  Dashboard: http://%s:%d/  (config, cron, editors)\n", displayHost, apiPort)
	}

	// 11. Start health check server
	go startHealthServer(servePort, cfg, agentReg, sessMgr, discordBots)
	fmt.Printf("  Health: http://%s:%d/health\n", displayHost, servePort)

	if cfg.FactoryMonitor.Enabled {
		if len(discordBots) == 0 {
			log.Printf("[FACTORY-MONITOR] WARN enabled but no Discord bot is configured")
		} else if natsConn == nil {
			log.Printf("[FACTORY-MONITOR] WARN enabled but NATS is not connected")
		} else {
			factoryMon = factorymonitor.New(factorymonitor.Config{
				Root:       cfg.FactoryMonitor.Root,
				PollEvery:  time.Duration(cfg.FactoryMonitor.PollSeconds) * time.Second,
				StuckAfter: time.Duration(cfg.FactoryMonitor.StuckAfterMinutes) * time.Minute,
			}, &natsPublisherAdapter{conn: natsConn})
			startFactoryMonitorNotifications(natsConn, discordBots[0], cfg.FactoryMonitor.NotifyChannelID)
			go factoryMon.Start(ctx)
			fmt.Printf("  Factory monitor: %s -> Discord channel %s\n", cfg.FactoryMonitor.Root, cfg.FactoryMonitor.NotifyChannelID)
		}
	}

	fmt.Println()
	fmt.Println("🔮 Prism is running. Press Ctrl+C to stop.")

	// 11. Start dream cycle scheduler (3AM nightly + event-triggered)
	if remClient != nil {
		go startDreamScheduler(remClient)
	}

	// V32: Start cron-style task scheduler if configured
	if cfg.Prism.Scheduler.Enabled {
		sched := scheduler.NewScheduler(&natsPublisherAdapter{conn: natsConn})
		for _, jobCfg := range cfg.Prism.Scheduler.Jobs {
			schedule, err := scheduler.ParseCron(jobCfg.Schedule)
			if err != nil {
				log.Printf("[SCHEDULER] ERROR parsing cron for job %q: %v", jobCfg.Name, err)
				continue
			}
			sched.AddJob(&scheduler.Job{
				Name:     jobCfg.Name,
				Schedule: schedule,
				Event:    jobCfg.Event,
				Payload:  jobCfg.Payload,
				Enabled:  jobCfg.Enabled,
			})
		}
		sched.StartInBackground()
		log.Printf("[SCHEDULER] started with %d job(s)", len(cfg.Prism.Scheduler.Jobs))
	}

	// V32/V36: Start wake handler to process scheduled events and interactive
	// workflow starts. This must run even when the cron scheduler is disabled.
	var primaryDiscordBot discordBotClient
	if len(discordBots) > 0 {
		primaryDiscordBot = discordBots[0]
	}
	if natsConn != nil && toolExec != nil && toolReg != nil {
		wakeHandler := NewWakeHandler(
			cfg,
			provReg,
			sessMgr,
			stateMgr,
			natsConn,
			primaryDiscordBot,
			ctxBuildr,
			planMgr,
			improveMgr,
			factoryMon,
			remClient,
			toolExec, // V35: Tool executor for project_work
			toolReg,  // V35: Tool registry for project_work
		)
		wakeHandler.SetSkills(skillReg) // V54: advertise discovered skills in the loop prompt
		if err := wakeHandler.Start(); err != nil {
			log.Printf("[WAKE] WARN failed to start wake handler: %v", err)
		} else {
			log.Printf("[WAKE] handler started, listening for scheduled and workflow events")
		}
		// V58: generic sub-agent worker — runs delegated task packets as
		// autonomous sub-agents. Feature-flagged (PRISM_SUBAGENT_WORKER); a
		// no-op until enabled, so this changes nothing by default.
		startSubAgentWorker(natsConn, provReg, toolExec, toolReg, cfg)
		if primaryDiscordBot != nil {
			if err := startWorkflowFeedbackNotifier(natsConn, primaryDiscordBot, cfg); err != nil {
				log.Printf("[WORKFLOW-NOTIFY] WARN failed to start: %v", err)
			}
		}
	}

	// Claude Code sub-agent reviewer — fulfills gated-loop feedback gates that
	// require the configured reviewer name. Independent of the scheduler.
	if cfg.ClaudeCode.Enabled && natsConn != nil {
		claudeExe, err := claudecli.ResolveExecutable(cfg.ClaudeCode.Executable, exec.LookPath)
		if err != nil {
			log.Printf("[CLAUDE-REVIEW] WARN disabled: %v", err)
		} else {
			reviewer := claudeworker.New(claudeworker.Config{
				Enabled:        true,
				Executable:     claudeExe,
				Model:          cfg.ClaudeCode.Model,
				ReviewerName:   cfg.ClaudeCode.ReviewerName,
				TimeoutMinutes: cfg.ClaudeCode.TimeoutMinutes,
				AllowedTools:   cfg.ClaudeCode.AllowedTools,
				ExtraArgs:      cfg.ClaudeCode.ExtraArgs,
			})
			if err := startClaudeReviewer(natsConn, reviewer, cfg); err != nil {
				log.Printf("[CLAUDE-REVIEW] WARN failed to start: %v", err)
			}
		}
	}

	// 12. Wait for shutdown signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	fmt.Println("\n🛑 Shutting down Prism...")

	// Cleanup
	for _, bot := range discordBots {
		bot.Stop()
	}
	if natsCleanup != nil {
		natsCleanup()
	}

	fmt.Println("✅ Prism stopped.")
}

// handleDiscordMessage processes an incoming Discord message through the
// full conversation pipeline.
//
// V21-5: The handler is now an adapter layer — it handles platform-specific
// concerns (debounce, typing, session management, Discord delivery) while
// delegating the domain-agnostic core to the stage pipeline:
//
//	Pipeline: LLMStage → PersistenceStage → EventPublishStage
//	Handler: debounce, session, typing, prompt build, StreamCallback, Remembrance
//
// The StreamCallback bridges LLMStage streaming to Discord:
// LLMStage calls callback(token) → callback sends to Discord via placeholder + edits.
func (cc *conversationContext) handleDiscordMessage(msg *discordbot.InboundMessage) {
	// Step 0: Skip empty or whitespace-only messages
	trimmed := strings.TrimSpace(msg.Content)
	if trimmed == "" {
		return
	}

	if cc.handleWorkflowFeedbackCommand(msg) {
		return
	}

	// Step 0a: Handle plan approval commands ("approve P-XXX" / "reject P-XXX")
	if cc.planMgr != nil {
		if strings.HasPrefix(trimmed, "approve ") || strings.HasPrefix(trimmed, "reject ") {
			if handled := cc.handlePlanApproval(msg); handled {
				return
			}
		}
	}

	if cc.handlePrismCommand(msg) {
		return
	}

	if msg.IsBot {
		if isAgentBot(cc.cfg.Agents, msg.UserID) {
			// Message from a listened-to agent — allow through pipeline for capture
			log.Printf("[AGENT] processing agent message from %s (%s)", msg.UserName, msg.UserID)
		} else {
			log.Printf("[AGENT] ignoring Discord bot message from %s (%s); cross-Prism agents communicate over NATS", msg.UserName, msg.UserID)
			return
		}
	}

	// Tagged-only mode: if the channel has tagged_only=true, skip unless the bot is mentioned
	channelRoleConfig := cc.cfg.ResolveChannelRoleConfig(msg.ChannelID)
	if channelRoleConfig != nil && channelRoleConfig.TaggedOnly {
		selfID := cc.bot.SelfID()
		mentioned := false
		if selfID != "" {
			mentioned = strings.Contains(msg.Content, "<@"+selfID+">") ||
				strings.Contains(msg.Content, "<@&"+selfID+">")
		}
		if !mentioned {
			return
		}
	}

	// V60: Free mode — check if this channel is in free mode and the sender is the master user.
	// Free mode skips phase gates, registers all tools including shell at the channel's tier,
	// and allows direct mutations without proposal/approval.
	freeMode := false
	if channelRoleConfig != nil && channelRoleConfig.Mode == "free" {
		masterUserID := cc.cfg.Shell.MasterUserID
		if masterUserID != "" && msg.UserID == masterUserID {
			freeMode = true
			log.Printf("[FREE-MODE] activated for master user %s in channel %s", msg.UserID, msg.ChannelID)

			// Set auto-approve mutations so write_file, git mutations, etc. execute directly
			cc.toolPolicy.AutoApproveMutations = true

			// Update shell tool policy to the channel's configured tier
			shellTier := channelRoleConfig.ShellPolicy
			if shellTier == "" {
				shellTier = "tier_3" // default to full access in free mode
			}
			if shell, err := cc.toolExec.Registry.Resolve("shell"); err == nil {
				if st, ok := shell.(*tool.ShellTool); ok {
					st.Policy = tool.BuildShellPolicyFromConfig(shellTier, cc.cfg.Shell.Allowlists, cc.cfg.Shell.Defaults.BlockedPatterns)
					log.Printf("[FREE-MODE] shell policy set to tier %s", shellTier)
				}
			}

			// Emit audit event
			cc.publishEvent("prism.free.action", map[string]any{
				"user_id":    msg.UserID,
				"channel_id": msg.ChannelID,
				"shell_tier": shellTier,
			})
		} else {
			log.Printf("[FREE-MODE] channel %s is free mode but sender %s is not master user %s — falling back to gated",
				msg.ChannelID, msg.UserID, masterUserID)
		}
	}

	// V60: Reset auto-approve mutations after free mode message is processed.
	// This ensures gated mode messages still require approval.
	if freeMode {
		defer func() {
			cc.toolPolicy.AutoApproveMutations = false
			// Reset shell tool policy back to tier_1 for gated mode
			if shell, err := cc.toolExec.Registry.Resolve("shell"); err == nil {
				if st, ok := shell.(*tool.ShellTool); ok {
					st.Policy = tool.BuildShellPolicyFromConfig("tier_1", cc.cfg.Shell.Allowlists, cc.cfg.Shell.Defaults.BlockedPatterns)
				}
			}
		}()
	}

	// Step 1: Debounce — drop rapid-fire messages from the same user
	debounceKey := msg.UserID + ":" + msg.ChannelID
	if !cc.debounce.Allow(debounceKey) {
		return
	}

	// Step 1b: Rate limiting — prevent abuse from rapid-fire users
	if cc.rateLimiter != nil {
		if !cc.rateLimiter.Allow(msg.UserID) {
			log.Printf("[RATE] user %s rate limited in channel %s", msg.UserID, msg.ChannelID)
			cc.bot.Send(&discordbot.OutboundMessage{
				ChannelID: msg.ChannelID,
				Content:   "⚠️ Slow down! You're sending messages too fast. Please wait a moment.",
			})
			return
		}
	}

	// Step 1c: Prompt injection defense — scan and sanitize user input
	injectionCheck := safety.CheckPromptInjection(msg.Content)
	if injectionCheck.Severity == "critical" {
		log.Printf("[SECURITY] blocked critical injection attempt from user %s: flags=%v", msg.UserID, injectionCheck.Flags)
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   "⚠️ That message contains potentially dangerous content and was blocked for safety.",
		})
		return
	}
	sanitizedContent := msg.Content
	if injectionCheck.Severity == "high" {
		sanitizedContent = safety.SanitizeInput(msg.Content)
		log.Printf("[SECURITY] sanitized high-severity input from user %s: flags=%v", msg.UserID, injectionCheck.Flags)
	}
	if injectionCheck.Severity == "medium" {
		log.Printf("[SECURITY] medium-severity flags in input from user %s: flags=%v", msg.UserID, injectionCheck.Flags)
	}

	if cc.handlePendingWorkStartReply(msg) {
		return
	}
	if codesummary.RequestMatches(sanitizedContent) {
		cc.handleCodebaseSummaryRequest(msg, sanitizedContent)
		return
	}
	if autopatch.RequestMatches(sanitizedContent) {
		cc.handleAutoPatchRequest(msg, sanitizedContent)
		return
	}
	if cc.maybeStartDetectedWork(msg, sanitizedContent) {
		return
	}

	// Emit channel received event
	cc.publishEvent("prism.channel.received", map[string]any{
		"user_id":    msg.UserID,
		"channel_id": msg.ChannelID,
	})

	// Step 2: Route the sanitized message to the appropriate agent
	result := cc.router.Route(sanitizedContent)

	// Step 2b: Response gate — skip low-signal messages that don't warrant a full LLM call
	// Manager-room and build-room always respond. Fun channel always responds.
	gateRole := cc.cfg.ResolveChannelRole(msg.ChannelID)
	gateDecision := stage.ShouldRespond(sanitizedContent, gateRole)
	if gateDecision == stage.Skip {
		log.Printf("[GATE] skipping low-signal message from %s in %s", msg.UserName, gateRole)
		return
	}
	if gateDecision == stage.RespondLightly {
		log.Printf("[GATE] light acknowledgment for message from %s in %s", msg.UserName, gateRole)
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   "👍",
		})
		return
	}

	finalSent := false
	finalStatus := "failed"
	finalMessage := "I stopped before completing this task."
	runID := ""
	var runCtx ctxcontext.Context
	sendFinal := func(status, message string) {
		if finalSent {
			return
		}
		finalSent = true
		cc.sendFinalReport(msg.ChannelID, status, runID, message)
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			log.Printf("[ERROR] panic while handling Discord message: %v", recovered)
			sendFinal("failed", fmt.Sprintf("Task failed unexpectedly: %v", recovered))
			return
		}
		if finalSent {
			return
		}
		if runCtx != nil && runCtx.Err() == ctxcontext.DeadlineExceeded {
			sendFinal("timed_out", "Task timed out before I could finish. No completion was confirmed.")
			return
		}
		sendFinal(finalStatus, finalMessage)
	}()

	// Step 3: Find or create an owner-scoped session while preserving channel metadata.
	sess, ownerID, err := getOrCreateSessionForMessage(cc.sessMgr, cc.cfg, result.AgentID, "discord", msg.ChannelID, msg.UserID)
	if err != nil {
		log.Printf("[ERROR] load session: %v", err)
		finalMessage = "I could not load the conversation session."
		return
	}

	// Add the sanitized user message to the session
	_, err = cc.sessMgr.AddMessage(sess.ID, "user", sanitizedContent, "")
	if err != nil {
		log.Printf("[ERROR] add user message: %v", err)
		finalMessage = "I could not save your message to the session."
		return
	}

	// Step 4: Look up the agent's provider and model (handler-level)
	agentCfg := cc.findAgentConfig(result.AgentID)
	if agentCfg == nil {
		log.Printf("[ERROR] no config for agent %s", result.AgentID)
		sendFinal("failed", "I'm not configured properly. Please contact the administrator.")
		return
	}

	llmProvider, err := cc.providers.GetForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		log.Printf("[ERROR] no provider for model %s: %v", agentCfg.Model, err)
		sendFinal("failed", "I can't reach my language model right now. Please try again in a moment.")
		return
	}

	// Step 5: Create a run with proper context for cancellation and timeout
	runCtx, runCancel := ctxcontext.WithTimeout(ctxcontext.Background(), serveLLMTimeout(cc.cfg))
	run := runtrack.NewRun(result.AgentID, sess.ID, agentCfg.Model, agentCfg.Provider)
	runID = run.ID
	run.Cancel = runCancel
	cc.cancelReg.Register(sess.ID, runCancel)
	defer func() {
		runCancel() // Release context timer immediately, not after 60s
		cc.cancelReg.Unregister(sess.ID)
	}()

	// Step 6: Send typing indicator (Discord-specific)
	if err := cc.bot.Typing(msg.ChannelID); err != nil {
		log.Printf("[WARN] typing indicator failed: %v", err)
	}

	// Step 7: Build the full prompt (session history + workspace context)
	// Resolve the channel role for state action injection
	stateActionKey := cc.cfg.ResolveChannelRole(msg.ChannelID)
	channelRole := cc.cfg.ResolveChannelRoleConfig(msg.ChannelID)
	promptSession := sess
	prompt := cc.buildPrompt(promptSession, agentCfg, stateActionKey, channelRole)

	// Step 7b: Inject Remembrance context (if available, with 60s TTL cache)
	// NOTE: This uses the same remembrance.Client as RemembranceStage but applies
	// session-aware caching and prompt injection that the generic stage can't do.
	// RemembranceStage is the reusable pipeline component; this is the runtime integration.
	if cc.remClient != nil {
		cacheKey := fmt.Sprintf("%s:%s", agentCfg.ID, sess.ID)
		remCtx := cc.remCache.Get(cacheKey)
		if remCtx == nil {
			var remCtxErr error
			remCtx, remCtxErr = cc.remClient.BuildContextWithOptions(remembrance.BuildContextRequest{
				Task:               sanitizedContent,
				ProjectID:          "prism",
				AgentID:            agentCfg.ID,
				OwnerID:            ownerID,
				LocalRecentSummary: localRecentSummary(sess),
				ChannelContext:     channelRoleContext(channelRole),
				MaxTokens:          remembrance.DefaultContextMaxTokens,
			})
			if remCtxErr != nil {
				log.Printf("[REMEMBRANCE] context build failed: %v", remCtxErr)
			} else if remCtx != nil {
				cc.remCache.Set(cacheKey, remCtx)
			}
		}
		if remCtx != nil {
			if memoryBlock := remembranceMemoryBlock(remCtx); memoryBlock != "" {
				promptSession = cloneSessionWithSystemMemory(sess, memoryBlock)
				prompt = cc.buildPrompt(promptSession, agentCfg, stateActionKey, channelRole)
				log.Printf("[REMEMBRANCE] injected %d memory sources into shared prompt layer", len(remCtx.SelectedMemories))
			}
		}
	}

	// P-008: Evaluate tool relevance gate BEFORE building the prompt
	// This determines whether to include tools in the LLM request
	toolNames := cc.toolExec.Registry.List()

	// V33: Channel tool filtering — some channels restrict tools
	var gateResult *stage.GateResult
	channelRoleConfig = cc.cfg.ResolveChannelRoleConfig(msg.ChannelID)
	if channelRoleConfig != nil && channelRoleConfig.Tools == "none" {
		// Fun channel: no tools at all. The agent is purely conversational.
		log.Printf("[TOOL-CHANNEL] tools excluded by channel role %q", channelRoleConfig.Role)
		gateResult = &stage.GateResult{Decision: stage.ToolDecisionExclude, Reason: "channel role excludes all tools"}
	} else if agentCfg.FirstClassTools {
		// V61: First-class tools — bypass the tool relevance gate. All tools are
		// always available, and tool calls auto-approve without per-call policy
		// evaluation. This gives the agent direct, low-latency tool access like
		// OpenClaw's first-class tool model.
		log.Printf("[TOOL-CHANNEL] first-class tools enabled for agent %q — bypassing gate", agentCfg.ID)
		gateResult = &stage.GateResult{Decision: stage.ToolDecisionInclude, Reason: "first-class tools: all tools available"}
		cc.toolPolicy.AutoApproveMutations = true
		defer func() { cc.toolPolicy.AutoApproveMutations = false }()
	} else {
		evalResult := cc.toolGate.Evaluate(msg.Content, toolNames)
		gateResult = &evalResult
		log.Printf("[TOOL-GATE] decision=%d reason=%q tools=%v", gateResult.Decision, gateResult.Reason, gateResult.ToolFilter)
	}

	// Step 7c: Append tool instructions to prompt so the LLM knows it has tools
	// Skip tool instructions if the gate excluded tools for this message
	//
	// V61: First-class tools — when agent.FirstClassTools is true, skip the
	// tool relevance gate (all tools are always available) and set auto-approve
	// so tool calls execute directly without per-call policy evaluation.
	if cc.toolExec != nil && gateResult.Decision != stage.ToolDecisionExclude {
		toolInfos := cc.toolExec.Registry.ListWithDescriptions()
		toolInfos = filterToolInfosByAgentPolicy(toolInfos, *cc.toolPolicy, agentCfg.ID)
		// V33: Filter tools based on channel role (read-only channels get limited tools)
		toolInfos = filterToolInfosByChannelRole(toolInfos, channelRoleConfig)
		if len(toolInfos) > 0 {
			prompt += agent.BuildToolPromptSuffix(toolInfos, cc.ctxBuilder.WorkspaceRoot, (*cc.toolPolicy).ReadAllowedPaths()...)
		}
	}

	// Step 8: Set up streaming — create placeholder and StreamCallback
	// Send a typing indicator instead of a placeholder message.
	// Placeholders ("✧ ...") were visible to other bots and caused false responses.
	// Now we show only the Discord "typing..." indicator and send the complete
	// response as a new message when the LLM is done.
	var placeholderMsgID string // Always empty — SendPlaceholder now returns ""
	var streamMu sync.Mutex
	var accumulatedText string
	var lastTypingTime time.Time

	cc.bot.Typing(msg.ChannelID) // Initial typing indicator

	_, placeholderErr := cc.bot.SendPlaceholder(msg.ChannelID, "")
	if placeholderErr != nil {
		log.Printf("[STREAM] placeholder/typing failed: %v", placeholderErr)
	}

	streamCallback := func(token string, index int, finished bool) error {
		streamMu.Lock()
		defer streamMu.Unlock()

		accumulatedText += token
		now := time.Now()

		// Refresh typing indicator every 8 seconds (Discord shows it for ~10s)
		if now.Sub(lastTypingTime) >= 8*time.Second {
			lastTypingTime = now
			go func() {
				if err := cc.bot.Typing(msg.ChannelID); err != nil {
					log.Printf("[STREAM] typing refresh failed: %v", err)
				}
			}()
		}

		// No placeholder to edit — streaming tokens accumulate in memory
		// and are sent as a complete message after the LLM finishes
		return nil
	}

	// Step 9: Construct and run the stage pipeline
	natsAdapter := &natsPublisherAdapter{conn: cc.natsConn}

	pipeline := stage.NewPipeline(
		&stage.LLMStage{},
		&stage.DelegationStage{Engine: cc.delegEngine, StripMarkers: true, AgentConfigs: cc.buildAgentConfigMap()},
		&stage.PersistenceStage{BusURL: cc.natsURL},
		&stage.EventPublishStage{Publisher: natsAdapter, BusURL: cc.natsURL},
	)

	rc := &stage.RunContext{
		RunID:          run.ID,
		Task:           prompt, // Full formatted prompt (not raw user input)
		Agent:          result.AgentID,
		Provider:       llmProvider,
		ProviderName:   agentCfg.Provider,
		Model:          agentCfg.Model,
		SessionID:      sess.ID,
		CleanedContent: result.CleanedContent,
		RouteMethod:    result.Method,
		StreamCallback: streamCallback,
	}

	finalRC, err := pipeline.Run(runCtx, rc)
	if err != nil {
		log.Printf("[ERROR] pipeline failed (run %s): %v", run.ID, err)
		sendFinal("failed", "I'm having trouble thinking right now. Please try again in a moment.")
		return
	}

	// Step 9b: Tool execution loop — branch on ChatProvider vs text-based
	if cc.toolExec != nil && gateResult.Decision != stage.ToolDecisionExclude {
		// Check if the provider supports native tool calling (ChatProvider)
		chatProv, chatErr := cc.providers.GetChatProviderForAgent(agentCfg.ID, agentCfg.Model)
		supportsChat := chatErr == nil

		if supportsChat {
			_ = chatProv // used via cc.runToolLoopChat which calls cc.providers.GetChatProvider internally
			// Native tool calling path: use ChatProvider with structured messages
			// Build messages array and tools list
			messages := cc.buildMessages(promptSession, agentCfg)
			chatTools := cc.buildChatTools()
			chatTools = filterChatToolsByAgentPolicy(chatTools, *cc.toolPolicy, agentCfg.ID)

			// V33: Filter tools based on channel role (none, read-only, all)
			chatTools = filterChatToolsByChannelRole(chatTools, channelRoleConfig)

			// P-008: Filter tools based on gate result
			if gateResult.Decision == stage.ToolDecisionSubset && len(gateResult.ToolFilter) > 0 {
				chatTools = filterChatTools(chatTools, gateResult.ToolFilter)
				log.Printf("[TOOL-GATE] subset: %d tools after filtering", len(chatTools))
			}

			log.Printf("[TOOL-CHAT] entering native tool loop with %d tools", len(chatTools))

			finalResponse, toolSummaries, toolErr := cc.runToolLoopChat(
				runCtx,
				messages,
				chatTools,
				agentCfg,
				msg.ChannelID,
				placeholderMsgID,
				run.ID,
			)
			if toolErr != nil {
				log.Printf("[TOOL-CHAT] tool loop failed: %v", toolErr)
				finalRC.LLMResponse = "I had trouble processing that — the AI service returned an error. Please try again in a moment."
			} else if finalResponse != "" {
				finalRC.LLMResponse = finalResponse
			}

			// Log tool call summaries
			for _, ts := range toolSummaries {
				log.Printf("[TOOL-CHAT] %s: %s (%s)", ts.Tool, ts.Status, truncateStr(ts.Result, 100))
			}

			// Save tool interactions to session
			for _, ts := range toolSummaries {
				toolMsg := fmt.Sprintf("[Tool: %s] %s", ts.Tool, ts.Status)
				if ts.Error != "" {
					toolMsg += " — " + ts.Error
				}
				cc.sessMgr.AddMessage(sess.ID, "tool", toolMsg, ts.Tool)
			}
		} else {
			// Text-based tool calling path (fallback for providers without ChatProvider)
			// P-008: Skip tool loop if gate excluded tools for this message
			parsed := agent.ParseAgentOutputWithFallback(finalRC.LLMResponse)
			if parsed.Type == agent.ResponseToolRequest && gateResult.Decision != stage.ToolDecisionExclude {
				log.Printf("[TOOL] LLM requested tool %q, entering tool loop", parsed.ToolName)

				finalResponse, toolSummaries, toolErr := cc.runToolLoop(
					runCtx,
					prompt,
					agentCfg,
					msg.ChannelID,
					placeholderMsgID,
					run.ID,
				)
				if toolErr != nil {
					log.Printf("[TOOL] tool loop failed: %v", toolErr)
					finalRC.LLMResponse = "I had trouble processing that — the AI service returned an error. Please try again in a moment."
				}

				for _, ts := range toolSummaries {
					log.Printf("[TOOL] %s: %s (%s)", ts.Tool, ts.Status, truncateStr(ts.Result, 100))
				}

				if finalResponse != "" {
					finalRC.LLMResponse = finalResponse
				}

				for _, ts := range toolSummaries {
					toolMsg := fmt.Sprintf("[Tool: %s] %s", ts.Tool, ts.Status)
					if ts.Error != "" {
						toolMsg += " — " + ts.Error
					}
					cc.sessMgr.AddMessage(sess.ID, "tool", toolMsg, ts.Tool)
				}
			}
		}
	}
	// Check pipeline results for stage failures
	for stageName, stageResult := range finalRC.Results {
		if stageResult != nil && !stageResult.Success {
			log.Printf("[ERROR] stage %s failed (run %s): %s", stageName, run.ID, stageResult.Error)
			switch stageName {
			case "routing":
				sendFinal("failed", "I'm not configured properly. Please contact the administrator.")
			case "llm":
				if runCtx.Err() == ctxcontext.DeadlineExceeded {
					sendFinal("timed_out", "Task timed out before I could finish. No completion was confirmed.")
				} else {
					sendFinal("failed", "I'm having trouble thinking right now. Please try again in a moment.")
				}
			default:
				sendFinal("failed", "Something went wrong processing your message. Please try again.")
			}
			return
		}
	}

	// Step 10: Post-pipeline — handle response delivery and session save
	responseText := finalRC.LLMResponse

	// Use accumulated text from streaming if the final response is empty
	if responseText == "" {
		streamMu.Lock()
		if accumulatedText != "" {
			responseText = accumulatedText
		}
		streamMu.Unlock()
	}

	// Deliver the response to Discord as a new message
	// (typing-only approach: no placeholder message, just send the complete response)
	if responseText != "" {
		err := cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   responseText,
		})
		if err != nil {
			log.Printf("[ERROR] failed to send Discord response: %v", err)
			finalMessage = "I completed the task but could not send the result to Discord."
		} else {
			finalStatus = "completed"
			finalSent = true
		}
	}

	// Save agent response to session
	if responseText != "" {
		_, err := cc.sessMgr.AddMessage(sess.ID, "agent", responseText, result.AgentID)
		if err != nil {
			log.Printf("[WARN] failed to save agent response to session %s: %v", sess.ID, err)
		}
	}

	// V61: Background commitment extraction
	if cc.commitStore != nil && sanitizedContent != "" && responseText != "" {
		go func(userText, assistantText, agentID, sessionKey, channel, senderID string) {
			cc.extractCommitments(userText, assistantText, agentID, sessionKey, channel, senderID)
		}(sanitizedContent, responseText, result.AgentID, sess.ID, "discord", msg.UserID)
	}
	// Log and publish completion events
	llmResult := finalRC.Results["llm"]
	streamed := false
	if llmResult != nil && llmResult.Data != nil {
		if s, ok := llmResult.Data["streamed"].(bool); ok {
			streamed = s
		}
	}

	cc.eventLog.Log("run.completed", run.ID, finalRC.Agent, map[string]any{
		"latency_ms": run.Elapsed().Milliseconds(),
	})
	cc.publishEvent(finalRC.Agent+".run.completed", map[string]any{
		"run_id":        run.ID,
		"latency_ms":    run.Elapsed().Milliseconds(),
		"output_length": len(responseText),
		"streamed":      streamed,
	})
	cc.publishEvent(finalRC.Agent+".channel.sent", map[string]any{
		"run_id":     run.ID,
		"channel_id": msg.ChannelID,
		"user_id":    msg.UserID,
	})

	// Step 11: Update local summary and optionally send curated candidates to Remembrance.
	if responseText != "" {
		enqueueLocalMemoryUpdate(cc.sessMgr, cc.cfg, cc.remClient, cc.remSem, cc.remCache, ownerID, msg.UserID, finalRC.Agent, sess.ID, run.ID)
	}

	log.Printf("[RUN] %s completed in %s", run, run.Elapsed().Round(time.Millisecond))
} // sendError sends a user-friendly error message to a Discord channel.
//lint:ignore U1000 retained for channel error reporting integration
func (cc *conversationContext) sendError(channelID, message string) {
	err := cc.bot.Send(&discordbot.OutboundMessage{
		ChannelID: channelID,
		Content:   "⚠️ " + message,
	})
	if err != nil {
		log.Printf("[ERROR] failed to send error message to Discord: %v", err)
	}
}

func (cc *conversationContext) sendFinalReport(channelID, status, runID, message string) {
	statusLabel := strings.ToUpper(strings.TrimSpace(status))
	if statusLabel == "" {
		statusLabel = "FAILED"
	}
	content := "Task " + statusLabel
	if runID != "" {
		content += " (run " + runID + ")"
	}
	if strings.TrimSpace(message) != "" {
		content += ": " + strings.TrimSpace(message)
	}
	if err := cc.bot.Send(&discordbot.OutboundMessage{ChannelID: channelID, Content: content}); err != nil {
		log.Printf("[ERROR] failed to send final report to Discord: %v", err)
	}
}

func serveLLMTimeout(cfg *orchestrator.Config) time.Duration {
	if cfg != nil && cfg.Prism.LLMTimeoutSeconds > 0 {
		return time.Duration(cfg.Prism.LLMTimeoutSeconds) * time.Second
	}
	return 1200 * time.Second
}

// V21: Events use per-agent namespace prefixes (<agent-id>.*).
// System events use the prism.* namespace.
// buildAgentConfigMap creates a map of agent ID → AgentConfig for the
// DelegationStage capability checks.
func (cc *conversationContext) buildAgentConfigMap() map[string]*orchestrator.AgentConfig {
	configs := make(map[string]*orchestrator.AgentConfig, len(cc.cfg.Agents)+1)
	for i := range cc.cfg.Agents {
		a := &cc.cfg.Agents[i]
		configs[a.ID] = a
	}
	if cc.cfg.Codex.Enabled {
		configs["codex"] = &orchestrator.AgentConfig{
			ID:           "codex",
			Role:         "coder",
			Provider:     "codex_cli",
			Model:        cc.cfg.Codex.Model,
			Capabilities: []string{"code", "test", "review", "report"},
		}
	}
	return configs
}

// If NATS is not connected, the event is logged but not published.
// All events include a schema version field for forward compatibility.
func (cc *conversationContext) publishEvent(subject string, payload map[string]any) {
	// Add schema version to all events (don't mutate caller's map)
	eventPayload := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		eventPayload[k] = v
	}
	eventPayload["v"] = 1
	if cc.natsConn == nil || !cc.natsConn.IsConnected() {
		log.Printf("[EVENT] %s (NATS not connected, skipped)", subject)
		return
	}

	data, err := json.Marshal(eventPayload)
	if err != nil {
		log.Printf("[EVENT] marshal error for %s: %v", subject, err)
		return
	}

	if err := cc.natsConn.Publish(subject, data); err != nil {
		log.Printf("[EVENT] publish error for %s: %v", subject, err)
		return
	}

	log.Printf("[EVENT] → %s", subject)
}

// findAgentConfig looks up the AgentConfig for a given agent ID.
func (cc *conversationContext) findAgentConfig(agentID string) *orchestrator.AgentConfig {
	for i := range cc.cfg.Agents {
		if cc.cfg.Agents[i].ID == agentID {
			return &cc.cfg.Agents[i]
		}
	}
	return nil
}

// rebuildStaticSystemContent pre-builds the static portion of the system prompt
// for the given agent. V33: Assembles in explicit layers with priority labels.
// Layers: Identity → Context Files → Postfix → Tools
// Called once at startup and cached for reuse.
func (cc *conversationContext) rebuildStaticSystemContent(agentCfg *orchestrator.AgentConfig) {
	var sb strings.Builder

	// --- Layer 1: IDENTITY ---
	// V33: Derive identity from workspace files (SOUL.md, IDENTITY.md) instead of
	// generic "You are {id}, a {role} assistant". If workspace files have identity
	// content, use it. Fall back to config id/role only if no identity files exist.
	identityContent := ""
	contextIdentity := ""
	if cc.ctxBuilder != nil {
		builder := context.NewBuilder(cc.ctxBuilder.WorkspaceRoot).WithNamedContexts([]string{"soul", "identity"})
		injected, err := builder.Build()
		if err == nil {
			for _, f := range injected.Files {
				if f.Name == "soul" && f.Content != "" {
					contextIdentity = f.Content
				}
			}
		}
	}

	if contextIdentity != "" {
		// Use workspace identity content — it's the real source of truth
		identityContent = contextIdentity
		cc.hasSoulContent = true
	} else {
		// Fall back to config id/role — better than nothing
		identityContent = fmt.Sprintf("You are %s, a %s assistant.", agentCfg.ID, agentCfg.Role)
	}

	sb.WriteString("## Who You Are\n")
	sb.WriteString(identityContent + "\n\n")

	// --- Layer 2: WORKSPACE CONTEXT ---
	// Full context files (AGENTS.md, USER.md, MEMORY.md, etc.)
	// Excluding soul and identity which are already in Layer 1
	if len(agentCfg.Context) > 0 && cc.ctxBuilder != nil {
		budget := cc.cfg.Prism.ContextTokenBudget
		if budget <= 0 {
			budget = 128000
		}

		// Build context without soul/identity (already in Layer 1)
		otherContexts := make([]string, 0, len(agentCfg.Context))
		for _, c := range agentCfg.Context {
			if c != "soul" && c != "identity" {
				otherContexts = append(otherContexts, c)
			}
		}

		if len(otherContexts) > 0 {
			builder := context.NewBuilder(cc.ctxBuilder.WorkspaceRoot).
				WithNamedContexts(otherContexts).
				WithTokenBudget(budget)
			injected, err := builder.BuildCached()
			if err == nil && injected.FormattedString != "" {
				sb.WriteString("## Context\n")
				sb.WriteString(injected.FormattedString + "\n")
			}
		}
	}

	// Layer 3 (Behavior/"How You Respond") and Layer 4 (Tools) are NOT
	// included in the text-path static cache below. Layer 3 depends on the
	// resolved ChannelRole's Personality, which is only known per-message —
	// both are computed dynamically in buildPrompt instead (see its doc
	// comment for the full 1-9 layer list).

	cc.staticSystemText = sb.String()

	// --- ChatProvider path: same layered format ---
	// NOTE: buildMessages() (tool_loop_chat.go) does not currently receive a
	// ChannelRole at all, so this path cannot honor per-channel Personality
	// yet — it keeps the prior agent-level-only postfix resolution below
	// unchanged. Fixing that is a separate, larger change (giving the
	// ChatProvider-native tool-calling path the same channel-context layers
	// buildPrompt already has), not done here.
	var sbChat strings.Builder
	sbChat.WriteString("## Who You Are\n")
	sbChat.WriteString(identityContent + "\n\n")

	if len(agentCfg.Context) > 0 && cc.ctxBuilder != nil {
		budget := cc.cfg.Prism.ContextTokenBudget
		if budget <= 0 {
			budget = 128000
		}

		otherContexts := make([]string, 0, len(agentCfg.Context))
		for _, c := range agentCfg.Context {
			if c != "soul" && c != "identity" {
				otherContexts = append(otherContexts, c)
			}
		}

		if len(otherContexts) > 0 {
			builder := context.NewBuilder(cc.ctxBuilder.WorkspaceRoot).
				WithNamedContexts(otherContexts).
				WithTokenBudget(budget)
			injected, err := builder.BuildCached()
			if err == nil && injected.FormattedString != "" {
				sbChat.WriteString("## Context\n")
				sbChat.WriteString(injected.FormattedString + "\n")
			}
		}
	}

	postfix := resolveConversationPostfix(agentCfg, nil, cc.hasSoulContent)
	sbChat.WriteString("\n## How You Respond\n")
	sbChat.WriteString(postfix + "\n")
	sbChat.WriteString("\n## Tool Usage\n" + toolUsageGuidance + "\n")

	cc.staticSystemChat = sbChat.String()
}

// buildPrompt constructs the LLM prompt from session history and injected context.
// V33: Layered prompt assembly with explicit priority labels.
//  1. Who You Are (identity from SOUL.md/IDENTITY.md)
//  2. Context (workspace files, excluding identity)
//  3. How You Respond (conversation postfix; agent postfix > channel
//     Personality > harness default — see resolveConversationPostfix)
//  4. Tools (tool usage guidance)
//  5. Working State (active task, decisions, blocked items)
//  6. Active Plan (plan-first pipeline)
//  7. Channel Context (where, who, what project, how to behave)
//  8. Session Awareness (message count, age)
//  9. Conversation History
//
// Layers 1-2 are cached (staticSystemText); layers 3-4 are computed fresh
// each call because layer 3 depends on the resolved ChannelRole, which is
// only known per-message.
func (cc *conversationContext) buildPrompt(sess *session.Session, agentCfg *orchestrator.AgentConfig, stateActionKey string, channelRole *orchestrator.ChannelRole) string {
	var sb strings.Builder

	// Static system content (cached, built once at startup)
	// Layers 1-2: Identity, Context
	sb.WriteString(cc.staticSystemText)

	// --- Layer 3: BEHAVIOR ---
	sb.WriteString("## How You Respond\n" + resolveConversationPostfix(agentCfg, channelRole, cc.hasSoulContent) + "\n\n")

	// --- Layer 4: TOOLS (text-based path) ---
	sb.WriteString("## Tool Usage\n" + toolUsageGuidance + "\n\n")

	// --- Layer 5: Working state injection ---
	if cc.stateMgr != nil {
		if statePrompt := cc.stateMgr.FormatStateForPrompt(); statePrompt != "" {
			sb.WriteString("\n" + statePrompt + "\n")
			log.Printf("[STATE] injected working state into prompt")
		}
	}

	// --- Layer 6: Active plan injection ---
	if cc.planMgr != nil {
		if plans, err := cc.planMgr.LoadPlans(); err == nil {
			activePlan := plan.ActivePlan(plans)
			if activePlan != nil {
				sb.WriteString("\n" + plan.FormatPlanForPrompt(activePlan) + "\n")
				log.Printf("[PLAN] injected active plan %s into prompt", activePlan.ID)
			}
		}
	}

	// --- Layer 7: Channel context injection ---
	// V33: Structured channel context replaces raw state_actions.inject.
	// When a ChannelRole has a Context field, use that.
	// When it doesn't, fall back to state_actions.inject for backward compatibility.
	// Personality is handled in Layer 3 above, not here — see resolveConversationPostfix.
	if channelRole != nil && channelRole.Context != "" {
		sb.WriteString("\n## Channel: #" + channelRole.Role + "\n")
		sb.WriteString(channelRole.Context + "\n\n")
		log.Printf("[CHANNEL] injected channel context for %q (tools=%s, personality=%s)",
			channelRole.Role, channelRole.Tools, channelRole.Personality)
	} else {
		// Backward compatibility: fall back to state_actions.inject
		if sa := cc.cfg.ResolveStateAction(agentCfg.ID, stateActionKey); sa != nil && sa.Inject != "" {
			sb.WriteString("\n## Context\n")
			sb.WriteString(sa.Inject)
			sb.WriteString("\n\n")
			log.Printf("[STATE] injected state action %q for agent %s", stateActionKey, agentCfg.ID)
		}
	}

	// --- Session awareness (V29) ---

	// --- Layer 8: Commitment delivery (V61) ---
	if cc.commitStore != nil {
		if commitPrompt := cc.deliverCommitments(agentCfg.ID, sess.ID, "discord"); commitPrompt != "" {
			sb.WriteString("\n" + commitPrompt + "\n")
			log.Printf("[COMMITMENTS] injected pending commitments into prompt")
		}
	}
	sessionAge := time.Since(sess.StartedAt).Round(time.Second)
	sessionMsgCount := len(sess.Messages)
	sb.WriteString(fmt.Sprintf("[Session: %d messages, started %v ago]\n", sessionMsgCount, sessionAge))

	// --- Conversation history ---
	for _, msg := range sess.Messages {
		switch msg.Role {
		case "user":
			sb.WriteString(fmt.Sprintf("User: %s\n", msg.Content))
		case "agent":
			sb.WriteString(fmt.Sprintf("%s: %s\n", msg.AgentID, msg.Content))
		case "system":
			sb.WriteString(fmt.Sprintf("System: %s\n", msg.Content))
		}
	}

	sb.WriteString(fmt.Sprintf("%s:", agentCfg.ID))
	return sb.String()
}

// registerProviders creates LLM providers from agent configs and registers them.
func registerProviders(cfg *orchestrator.Config, reg *provider.ProviderRegistry) error {
	seen := make(map[string]string) // model ID → provider name
	for _, agentCfg := range cfg.Agents {
		targets := append([]orchestrator.ModelFallback{{Provider: agentCfg.Provider, Model: agentCfg.Model}}, agentCfg.Fallbacks...)
		for _, target := range targets {
			if prev, ok := seen[target.Model]; ok {
				if prev != target.Provider {
					return fmt.Errorf("agent %s: model %q is already registered with provider %q; agents sharing a model ID must use the same provider", agentCfg.ID, target.Model, prev)
				}
				continue
			}
			targetCfg := orchestrator.AgentConfig{Provider: target.Provider, Model: target.Model}
			p, info, err := createProvider(targetCfg, cfg)
			if err != nil {
				return fmt.Errorf("agent %s: %w", agentCfg.ID, err)
			}
			reg.Register(target.Model, p, info)
			seen[target.Model] = target.Provider
		}
	}

	for _, agentCfg := range cfg.Agents {
		specs := append([]orchestrator.ModelFallback{{Provider: agentCfg.Provider, Model: agentCfg.Model}}, agentCfg.Fallbacks...)
		targets := make([]provider.FailoverTarget, 0, len(specs))
		for _, spec := range specs {
			p, err := reg.Get(spec.Model)
			if err != nil {
				return fmt.Errorf("agent %s: resolve %s: %w", agentCfg.ID, spec.Model, err)
			}
			info, err := reg.ModelInfo(spec.Model)
			if err != nil {
				return fmt.Errorf("agent %s: inspect %s: %w", agentCfg.ID, spec.Model, err)
			}
			if info.ProviderName != spec.Provider {
				return fmt.Errorf("agent %s: fallback %q resolves to provider %q, not %q", agentCfg.ID, spec.Model, info.ProviderName, spec.Provider)
			}
			targets = append(targets, provider.FailoverTarget{Provider: p, ProviderName: spec.Provider, Model: spec.Model})
		}
		reg.RegisterAgent(agentCfg.ID, provider.NewFailoverProvider(targets))
	}
	return nil
}

// createProvider creates a provider instance for an agent config.
// V20 supports: ollama, openai, anthropic, gemini.
// V34 adds openai_responses as an additive OpenAI Responses API option.
func createProvider(agentCfg orchestrator.AgentConfig, cfg *orchestrator.Config) (provider.Provider, provider.ModelInfo, error) {
	info := provider.ModelInfo{
		ID:           agentCfg.Model,
		ProviderName: agentCfg.Provider,
	}

	switch agentCfg.Provider {
	case "ollama":
		// Base URL precedence: OLLAMA_BASE_URL env → prism.ollama_url →
		// provider default (localhost:11434).
		p, err := createOllamaProvider(agentCfg.Model, resolveOllamaURL("", cfg.Prism.OllamaURL))
		if err != nil {
			return nil, info, fmt.Errorf("ollama provider: %w", err)
		}
		return p, info, nil

	case "openai":
		p, err := createOpenAIProvider(agentCfg.Model)
		if err != nil {
			return nil, info, fmt.Errorf("openai provider: %w", err)
		}
		return p, info, nil

	case "openai_responses":
		p, err := createOpenAIResponsesProvider(agentCfg.Model)
		if err != nil {
			return nil, info, fmt.Errorf("openai responses provider: %w", err)
		}
		return p, info, nil

	case "anthropic":
		p, err := createAnthropicProvider(agentCfg.Model)
		if err != nil {
			return nil, info, fmt.Errorf("anthropic provider: %w", err)
		}
		return p, info, nil

	case "gemini":
		p, err := createGeminiProvider(agentCfg.Model)
		if err != nil {
			return nil, info, fmt.Errorf("gemini provider: %w", err)
		}
		return p, info, nil

	case "claude_code":
		// Claude Code subscription brain: shells out to the `claude` CLI.
		// Reuses the top-level claude_code: block for executable/timeout.
		p, err := createClaudeCodeProvider(agentCfg, cfg.ClaudeCode)
		if err != nil {
			return nil, info, fmt.Errorf("claude_code provider: %w", err)
		}
		return p, info, nil

	case "codex":
		// Codex subscription: shells out to the `codex` CLI (codex exec).
		// Reuses the top-level codex: block for executable/model/sandbox.
		p, err := createCodexProvider(agentCfg, cfg.Codex, cfg)
		if err != nil {
			return nil, info, fmt.Errorf("codex provider: %w", err)
		}
		return p, info, nil

	default:
		return nil, info, fmt.Errorf("unsupported provider: %s (supported: ollama, openai, openai_responses, anthropic, gemini, claude_code, codex)", agentCfg.Provider)
	}
}

// createClaudeCodeProvider builds a Claude Code CLI provider. Auth comes from the
// installed `claude` binary's subscription session — no API key required. The
// executable and timeout are taken from the top-level claude_code: config block;
// the model from the agent config (falling back to the block's model).
func createClaudeCodeProvider(agentCfg orchestrator.AgentConfig, ccCfg orchestrator.ClaudeCodeConfig) (provider.Provider, error) {
	model := agentCfg.Model
	if model == "" {
		model = ccCfg.Model
	}
	executable, err := claudecli.ResolveExecutable(ccCfg.Executable, exec.LookPath)
	if err != nil {
		return nil, err
	}
	return claudecode.New(claudecode.Config{
		Executable:     executable,
		Model:          model,
		TimeoutMinutes: ccCfg.TimeoutMinutes,
		ExtraArgs:      ccCfg.ExtraArgs,
	}), nil
}

// createCodexProvider builds a Codex CLI provider. Auth comes from the installed
// `codex` binary's subscription session — no API key required. Executable,
// model, sandbox, and profile come from the top-level codex: config block; the
// agent's Model field is only the provider-registry label. The --cd workspace
// resolves from codex.workspace → prism.workspace → cwd so `codex exec` always
// gets a valid directory.
func createCodexProvider(agentCfg orchestrator.AgentConfig, cxCfg orchestrator.CodexConfig, cfg *orchestrator.Config) (provider.Provider, error) {
	c := codexcli.Config{
		Executable:     cxCfg.Executable,
		Model:          cxCfg.Model,
		Profile:        cxCfg.Profile,
		Sandbox:        cxCfg.Sandbox,
		ApprovalPolicy: cxCfg.ApprovalPolicy,
		TimeoutMinutes: cxCfg.TimeoutMinutes,
		ExtraArgs:      cxCfg.ExtraArgs,
		Workspace:      codexcli.ResolveWorkspace(cxCfg.Workspace, cfg.Prism.Workspace),
	}
	c = codexcli.Normalize(c)
	if _, err := exec.LookPath(c.Executable); err != nil {
		//lint:ignore ST1005 Codex is a product name in a user-facing diagnostic.
		return nil, fmt.Errorf("Codex CLI executable %q not found (install Codex or set codex.executable): %w", c.Executable, err)
	}
	return codexcli.New(c), nil
}

func factoryConfigFromBridge(cfg orchestrator.FactoryBridgeConfig) factory.Config {
	return factory.Config{
		Enabled:            cfg.Enabled,
		Root:               cfg.Root,
		Project:            cfg.Project,
		ProjectPath:        cfg.ProjectPath,
		ApprovalMode:       cfg.ApprovalMode,
		RunCodex:           cfg.RunCodex,
		VisionReview:       cfg.VisionReview,
		PlaytestMode:       cfg.PlaytestMode,
		EnableUIGeneration: cfg.EnableUIGeneration,
		UIGenerationDryRun: cfg.UIGenerationDryRun,
	}
}

func crossProfilesFromBridge(profiles []orchestrator.BridgeTargetProfile) []crossprism.TargetProfile {
	out := make([]crossprism.TargetProfile, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, crossprism.TargetProfile{
			Name:         profile.Name,
			InstanceID:   profile.InstanceID,
			Adapter:      profile.Adapter,
			Capabilities: append([]string(nil), profile.Capabilities...),
		})
	}
	return out
}

func codexConfigFromOrchestrator(cfg orchestrator.CodexConfig, root *orchestrator.Config) codexworker.Config {
	workspace := cfg.Workspace
	if workspace == "" && root != nil {
		workspace = root.Prism.Workspace
	}
	if workspace == "" {
		workspace = "."
	}
	dataDir := filepath.Join(".", ".prism", "data", "codex")
	if root != nil && root.Prism.DataDir != "" {
		dataDir = filepath.Join(root.Prism.DataDir, "codex")
	}
	return codexworker.Config{
		Enabled:        cfg.Enabled,
		Executable:     cfg.Executable,
		Model:          cfg.Model,
		Profile:        cfg.Profile,
		Workspace:      workspace,
		Sandbox:        cfg.Sandbox,
		ApprovalPolicy: cfg.ApprovalPolicy,
		TimeoutMinutes: cfg.TimeoutMinutes,
		MaxConcurrency: cfg.MaxConcurrency,
		CaptureDiff:    cfg.CaptureDiff,
		ExtraArgs:      append([]string(nil), cfg.ExtraArgs...),
		DataDir:        dataDir,
	}
}

// usageWindowsFromConfig builds the usage tracker's range→window/bucket map,
// starting from the built-in defaults and applying any prism.yaml overrides.
// Durations were already validated by Config.Validate, so parse failures here
// are treated defensively (the offending field is left at its default).
func usageWindowsFromConfig(cfg *orchestrator.Config) map[string]api.WindowSpec {
	windows := api.DefaultUsageWindows()
	for _, o := range cfg.Usage.Windows {
		spec, ok := windows[o.Range]
		if !ok {
			continue
		}
		if o.Bucket != "" {
			if d, err := time.ParseDuration(o.Bucket); err == nil && d > 0 {
				spec.BucketMs = d.Milliseconds()
				spec.Bucket = o.Bucket
			}
		}
		// window applies only to fixed ranges (session/lifetime derive their lower
		// bound from process start / epoch).
		if o.Window != "" && spec.Kind == "fixed" {
			if d, err := time.ParseDuration(o.Window); err == nil && d > 0 {
				spec.WindowMs = d.Milliseconds()
			}
		}
		windows[o.Range] = spec
	}
	return windows
}

// createOllamaProvider creates an Ollama provider instance.
// baseURL comes from resolveOllamaURL (env → config); empty means the
// provider's default localhost:11434.
// V30: Returns both Provider and ChatProvider — Ollama supports both interfaces.
func createOllamaProvider(model, baseURL string) (provider.Provider, error) {
	p := ollama.New(baseURL)
	// The Ollama Provider also implements ChatProvider via ollama.ChatProvider.
	// It is registered under the same model ID and the runtime uses interface
	// assertions to detect chat capability: prov.(provider.ChatProvider)
	return p, nil
}

// createOpenAIProvider creates an OpenAI provider instance.
func createOpenAIProvider(model string) (provider.Provider, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable not set")
	}
	return openai.New(apiKey), nil
}

// createOpenAIResponsesProvider creates an OpenAI Responses API provider.
func createOpenAIResponsesProvider(model string) (provider.Provider, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY environment variable not set")
	}
	return openai.NewResponses(apiKey), nil
}

// createAnthropicProvider creates an Anthropic provider instance.
func createAnthropicProvider(model string) (provider.Provider, error) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("ANTHROPIC_API_KEY environment variable not set")
	}
	return anthropic.New(apiKey), nil
}

// createGeminiProvider creates a Gemini provider instance.
func createGeminiProvider(model string) (provider.Provider, error) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("GEMINI_API_KEY environment variable not set")
	}
	return gemini.New(apiKey), nil
}

// providerModelIDs returns all registered model IDs (for logging).
func providerModelIDs(reg *provider.ProviderRegistry) []string {
	return reg.ListModels()
}

// startHealthServer starts a simple HTTP server for health checks.
func startHealthServer(port int, cfg *orchestrator.Config, agentReg *agent.Registry, sessMgr *session.Manager, discordBots []*discordbot.BotAdapter) {
	http.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		discordReady := false
		for _, bot := range discordBots {
			if bot.IsReady() {
				discordReady = true
				break
			}
		}
		fmt.Fprintf(w, `{"status":"ok","agents":%d,"discord_ready":%v}`, len(cfg.Agents), discordReady)
	})

	if err := http.ListenAndServe(cfg.BindAddr(port), nil); err != nil {
		log.Printf("Health server error: %v", err)
	}
}

func agentNames(cfg *orchestrator.Config) []string {
	names := make([]string, len(cfg.Agents))
	for i, a := range cfg.Agents {
		names[i] = a.ID
	}
	return names
}

func primaryName(cfg *orchestrator.Config) string {
	p := cfg.PrimaryAgent()
	if p == nil {
		return "(none)"
	}
	return p.ID
}

// filterToolInfosByChannelRole filters ToolInfo slices based on channel role configuration.
func filterToolInfosByChannelRole(toolInfos []tool.ToolInfo, channelRole *orchestrator.ChannelRole) []tool.ToolInfo {
	mode := resolveToolMode(channelRole)
	switch mode {
	case ToolModeNone:
		return nil
	case ToolModeReadOnly:
		var filtered []tool.ToolInfo
		for _, t := range toolInfos {
			if readOnlyTools[t.Name] {
				filtered = append(filtered, t)
			}
		}
		return filtered
	default:
		return toolInfos
	}
}

func filterToolInfosByAgentPolicy(toolInfos []tool.ToolInfo, policy tool.PolicyConfig, agentID string) []tool.ToolInfo {
	if policy.CanAgentProposeWrites(agentID) {
		return toolInfos
	}
	filtered := make([]tool.ToolInfo, 0, len(toolInfos))
	for _, t := range toolInfos {
		if !mutationProposalTools[t.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

func (cc *conversationContext) checkChannelToolAccess(toolName, channelID string) error {
	channelRole := cc.cfg.ResolveChannelRoleConfig(channelID)
	mode := resolveToolMode(channelRole)
	switch mode {
	case ToolModeNone:
		return fmt.Errorf("tool %q denied: channel role disables tools", toolName)
	case ToolModeReadOnly:
		if !readOnlyTools[toolName] {
			return fmt.Errorf("tool %q denied: channel role allows read-only tools only", toolName)
		}
	}
	return nil
}

// remembranceTimeout returns the configured Remembrance timeout duration,
// falling back to the default if not set.
func remembranceTimeout(cfg *orchestrator.Config) time.Duration {
	if cfg.Remembrance.TimeoutSeconds > 0 {
		return time.Duration(cfg.Remembrance.TimeoutSeconds) * time.Second
	}
	return remembrance.DefaultTimeout
}

func bridgeSecret(cfg *orchestrator.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.Bridge.SecretEnv != "" {
		if secret := os.Getenv(cfg.Bridge.SecretEnv); secret != "" {
			return secret
		}
	}
	return cfg.Bridge.Secret
}

// filterChatTools filters a ChatTool slice to only include tools whose names
// are in the filter list. Used by P-008 ToolRelevanceGate for subset decisions.
func filterChatTools(tools []provider.ChatTool, filter []string) []provider.ChatTool {
	filterSet := make(map[string]bool, len(filter))
	for _, f := range filter {
		filterSet[f] = true
	}
	var filtered []provider.ChatTool
	for _, t := range tools {
		if filterSet[t.Function.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// readOnlyTools is the set of tool names that are safe for read-only channels.
var readOnlyTools = map[string]bool{
	"echo":                     true,
	"list_dir":                 true,
	"read_file":                true,
	"read_project":             true,
	"search_files":             true,
	"project_overview":         true,
	"git_status":               true,
	"git_log":                  true,
	"git_diff":                 true,
	"git_branch_list":          true,
	"web_search":               true,
	"memory_search":            true,
	"fetch_image":              true,
	"generate_image":           true,
	"analyze_image":            true,
	"collect_reference_images": true,
	"plan_list":                true,
	"state_get":                true,
}

var mutationProposalTools = map[string]bool{
	"write_file":                true,
	"write_file_proposal":       true,
	"create_directory":          true,
	"create_directory_proposal": true,
	"git_add":                   true,
	"git_commit":                true,
	"git_push":                  true,
	"create_pr":                 true,
	"shell":                     true,
}

// ToolMode represents the tool access level for a channel.
type ToolMode int

const (
	ToolModeAll      ToolMode = iota // All tools available
	ToolModeReadOnly                 // Only read-only tools
	ToolModeNone                     // No tools at all
)

// resolveToolMode determines the tool access level from a channel role config.
func resolveToolMode(channelRole *orchestrator.ChannelRole) ToolMode {
	if channelRole == nil {
		return ToolModeAll
	}
	switch channelRole.Tools {
	case "none":
		return ToolModeNone
	case "read-only":
		return ToolModeReadOnly
	default:
		return ToolModeAll
	}
}

// filterChatToolsByChannelRole filters tools based on channel role configuration.
// "none" → empty list (no tools), "read-only" → only read tools, "" or "all" → all tools.
func filterChatToolsByChannelRole(tools []provider.ChatTool, channelRole *orchestrator.ChannelRole) []provider.ChatTool {
	mode := resolveToolMode(channelRole)
	switch mode {
	case ToolModeNone:
		return nil
	case ToolModeReadOnly:
		var filtered []provider.ChatTool
		for _, t := range tools {
			if readOnlyTools[t.Function.Name] {
				filtered = append(filtered, t)
			}
		}
		return filtered
	default:
		return tools
	}
}

func filterChatToolsByAgentPolicy(tools []provider.ChatTool, policy tool.PolicyConfig, agentID string) []provider.ChatTool {
	if policy.CanAgentProposeWrites(agentID) {
		return tools
	}
	filtered := make([]provider.ChatTool, 0, len(tools))
	for _, t := range tools {
		if !mutationProposalTools[t.Function.Name] {
			filtered = append(filtered, t)
		}
	}
	return filtered
}

// mcpServerSpecs maps configured MCP servers to transport-neutral specs for
// mcp.RegisterServers.
func mcpServerSpecs(cfg *orchestrator.Config) []mcp.ServerSpec {
	if cfg == nil {
		return nil
	}
	specs := make([]mcp.ServerSpec, 0, len(cfg.MCPServers))
	for _, s := range cfg.MCPServers {
		specs = append(specs, mcp.ServerSpec{
			Name:    s.Name,
			Command: s.Command,
			Args:    s.Args,
			Env:     s.Env,
			Enabled: s.Enabled,
		})
	}
	return specs
}

// sendFileApprovalCard sends a Discord message with Approve/Deny buttons for a file write approval.
func sendFileApprovalCard(bot *discordbot.BotAdapter, channelID, approvalID, runID, targetPath, agentName, preview string) {
	if bot == nil || channelID == "" {
		return
	}
	buttons := buildFileApprovalButtons(approvalID, runID)
	msgButtons := make([]discordbot.MessageButton, len(buttons))
	for i, b := range buttons {
		msgButtons[i] = discordbot.MessageButton{
			Label:    b.Label,
			Style:    int(b.Style),
			CustomID: b.CustomID,
		}
	}
	content := fmt.Sprintf("📋 **File write approval requested**\n**Agent:** %s\n**Path:** `%s`\n**Preview:**\n```\n%s\n```", agentName, targetPath, preview)
	if err := bot.Send(&discordbot.OutboundMessage{
		ChannelID: channelID,
		Content:   content,
		Buttons:   msgButtons,
	}); err != nil {
		log.Printf("[APPROVAL-CARD] failed to send: %v", err)
	}
}
