// Package main implements the `prizm chat` subcommand — an interactive
// terminal chat that connects to an LLM agent with full tool support,
// context injection, and session management.
//
// Usage:
//
//	prizm chat [--config prizm.yaml] [--agent <name>]
//
// The chat command:
//  1. Loads prizm.yaml configuration
//  2. Registers agents and providers
//  3. Creates a session for the terminal user
//  4. Enters a readline loop: user input → pipeline → response
//  5. Supports native tool calling (ChatProvider) and text-based fallback
//  6. Persists session history to the same DB as Discord sessions
package main

import (
	"bufio"
	ctxcontext "context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/emaharmony/prizm/internal/action"
	"github.com/emaharmony/prizm/internal/agent"
	"github.com/emaharmony/prizm/internal/approval"
	"github.com/emaharmony/prizm/internal/bus"
	"github.com/emaharmony/prizm/internal/context"
	"github.com/emaharmony/prizm/internal/delegation"
	"github.com/emaharmony/prizm/internal/guard"
	"github.com/emaharmony/prizm/internal/memory"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/plan"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/router"
	"github.com/emaharmony/prizm/internal/runtrack"
	"github.com/emaharmony/prizm/internal/safety"
	"github.com/emaharmony/prizm/internal/session"
	"github.com/emaharmony/prizm/internal/stage"
	"github.com/emaharmony/prizm/internal/state"
	"github.com/emaharmony/prizm/internal/task"
	"github.com/emaharmony/prizm/internal/tool"
	"github.com/nats-io/nats.go"
)

// chatContext holds all dependencies for the interactive chat loop.
// It mirrors conversationContext but without Discord-specific fields.
type chatContext struct {
	router      *router.Router
	sessMgr     *session.Manager
	cfg         *orchestrator.Config
	providers   *provider.ProviderRegistry
	ctxBuilder  *context.Builder
	toolExec    *tool.Executor
	toolPolicy  tool.PolicyConfig
	eventLog    *runtrack.EventLogger
	cancelReg   *runtrack.CancelRegistry
	actionReg   *action.Registry
	taskStore   *task.Store
	delegEngine *delegation.Engine
	natsConn    *nats.Conn     // Persistent NATS connection for event pipeline
	natsURL     string         // NATS URL (embedded or external)
	natsCleanup func()         // Cleanup for embedded NATS
	rateLimiter *chatRateLimit // Per-message rate limiting for CLI
	stateMgr    *state.Manager // V32: Working state manager for adaptive context
	planMgr     *plan.Manager  // V32: Plan manager for plan-first pipeline
	guardian    *guard.Guard   // V32: Guard rail for plan enforcement
	memInjector *MemoryInjector // V79: Smart memory injection
	memoryStore *memory.MarkdownStore // V77: Local memory store for recall
	hasSoulContent bool        // True when SOUL.md is loaded — takes precedence over postfix

	// Cached static system content — built once, reused every message.
	// Includes: agent identity, workspace context, postfix, tool instructions.
	// Does NOT include: session age/count (dynamic per message).
	staticSystemText string // For text-based provider path
	staticSystemChat string // For ChatProvider path (includes toolUsageGuidance)
}

// chatRateLimit provides simple rate limiting for CLI chat.
// Unlike Discord (per-user), this is per-process — prevents runaway LLM calls.
type chatRateLimit struct {
	mu       sync.Mutex
	lastCall time.Time
	minDelay time.Duration // Minimum time between messages
}

func (r *chatRateLimit) Allow() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if now.Sub(r.lastCall) < r.minDelay {
		return false
	}
	r.lastCall = now
	return true
}

// thinkingSpinner shows an animated "Thinking..." indicator on the terminal
// while the agent is working. It runs in its own goroutine and redraws the
// same line via a carriage return; Stop halts it and clears the line.
type thinkingSpinner struct {
	stop chan struct{}
	done chan struct{}
}

// startThinkingSpinner begins an animated "Thinking..." indicator and returns a
// handle used to stop it. Frames cycle the trailing dots so the user gets live
// feedback that the model is processing (which can take a while for local models).
func startThinkingSpinner() *thinkingSpinner {
	s := &thinkingSpinner{
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go func() {
		defer close(s.done)
		frames := []string{"Thinking.  ", "Thinking.. ", "Thinking..."}
		i := 0
		fmt.Print("\r" + frames[0])
		ticker := time.NewTicker(400 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-s.stop:
				return
			case <-ticker.C:
				i = (i + 1) % len(frames)
				fmt.Print("\r" + frames[i])
			}
		}
	}()
	return s
}

// Stop halts the spinner goroutine and clears the indicator line so the next
// output (a response, tool activity, or an error) starts on a clean line.
func (s *thinkingSpinner) Stop() {
	close(s.stop)
	<-s.done
	clearThinkingLine()
}

// clearThinkingLine erases the current terminal line. It is used both to wipe
// the animated "Thinking..." indicator and to keep tool-activity lines from
// merging with an in-flight spinner frame.
func clearThinkingLine() {
	fmt.Print("\r" + strings.Repeat(" ", 12) + "\r")
}

// isModelBackendCrash reports whether err came from the local model backend
// (Ollama's llama-server) crashing — typically a GPU/CUDA initialization
// failure rather than a problem in Prizm itself.
func isModelBackendCrash(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "llama-server process has terminated") ||
		strings.Contains(msg, "CUDA error") ||
		strings.Contains(msg, "shared object initialization failed")
}

// modelBackendCrashHint returns an actionable message for a local model backend
// crash, pointing the user at the GPU/CUDA cause instead of a raw HTTP 500.
func modelBackendCrashHint(agentCfg *orchestrator.AgentConfig) string {
	return fmt.Sprintf(
		"⚠️  The local model backend crashed while loading %q.\n"+
			"   This is a GPU/CUDA failure inside Ollama, not a Prizm bug.\n"+
			"   Try, in order:\n"+
			"     1. Confirm it happens outside Prizm: ollama run %s \"hi\"\n"+
			"     2. Update your NVIDIA driver and Ollama, then restart Ollama.\n"+
			"     3. Run on CPU to rule out the GPU: set OLLAMA_NO_GPU=1 (or CUDA_VISIBLE_DEVICES=\"\") and restart Ollama.\n"+
			"     4. Lower the context/VRAM use (e.g. OLLAMA_CONTEXT_LENGTH=8192) and retry.\n",
		agentCfg.Model, agentCfg.Model,
	)
}

func executeChat(args []string) {
	chatCmd := flag.NewFlagSet("chat", flag.ExitOnError)
	configPath := chatCmd.String("config", "prizm.yaml", "Path to prizm.yaml configuration file")
	agentName := chatCmd.String("agent", "", "Agent to chat with (defaults to primary agent)")

	chatCmd.Parse(args)

	fmt.Println("🔮 Prizm Chat")
	fmt.Println()

	// 1. Load configuration
	cfg, err := orchestrator.LoadConfig(*configPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "Error: config file %q not found. Create one with 'prizm init' or specify --config\n", *configPath)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}

	// Resolve agent
	agentID := *agentName
	if agentID == "" {
		agentID = primaryName(cfg)
	}
	if agentID == "" {
		fmt.Fprintln(os.Stderr, "Error: no agent specified and no primary agent in config")
		os.Exit(1)
	}

	agentCfg := findAgentConfig(cfg, agentID)
	if agentCfg == nil {
		fmt.Fprintf(os.Stderr, "Error: agent %q not found in config\n", agentID)
		os.Exit(1)
	}

	fmt.Printf("  Agent: %s (%s)\n", agentCfg.ID, agentCfg.Role)
	fmt.Printf("  Model: %s (%s)\n", agentCfg.Model, agentCfg.Provider)

	// 2. Start embedded NATS (for event pipeline)
	natsURL := cfg.Prizm.NATSURL
	var natsCleanup func()
	if natsURL == "" {
		url, cleanup, err := bus.StartEmbeddedBus(0)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error starting embedded bus: %v\n", err)
			os.Exit(1)
		}
		natsURL = url
		natsCleanup = cleanup
	}

	// Connect to NATS once for the entire session
	natsConn, err := bus.ConnectToBus(natsURL)
	if err != nil {
		log.Printf("[WARN] NATS connection failed (events disabled): %v", err)
	}
	if natsConn != nil {
		defer natsConn.Close()
	}
	if natsCleanup != nil {
		defer natsCleanup()
	}

	// 3. Register agents and providers
	agentReg := agent.NewRegistry()
	if err := cfg.RegisterAgents(agentReg); err != nil {
		fmt.Fprintf(os.Stderr, "Error registering agents: %v\n", err)
		os.Exit(1)
	}

	provReg := provider.NewProviderRegistry()
	if err := registerProviders(cfg, provReg); err != nil {
		fmt.Fprintf(os.Stderr, "Error registering providers: %v\n", err)
		os.Exit(1)
	}

	// 4. Start session manager
	if err := os.MkdirAll(cfg.Prizm.DataDir, 0755); err != nil {
		fmt.Fprintf(os.Stderr, "Error creating data directory: %v\n", err)
		os.Exit(1)
	}
	dbPath := cfg.Prizm.DataDir + "/sessions.db"
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

	// 5. Set up router
	rtr := router.New(agentReg, cfg)

	// 6. Set up tools
	registry := tool.NewRegistry()

	// Resolve workspace root
	workspaceRoot := cfg.Prizm.Workspace
	if workspaceRoot == "" {
		workspaceRoot = "."
	}

	readRoots := configuredReadRoots(cfg)
	writeRoots := configuredWriteRoots(cfg)

	tool.RegisterBuiltinsWithRoots(registry, workspaceRoot, 10*1024*1024, readRoots, writeRoots)
	registry.Register(&tool.WriteFileProposal{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})
	registry.Register(&tool.CreateDirectoryProposal{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots})
	protectedBranch := cfg.ProtectedBranch()
	registry.Register(&tool.GitAddTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}})
	registry.Register(&tool.GitCommitTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}, ProtectedBranch: protectedBranch})
	registry.Register(&tool.GitPushTool{ToolPaths: tool.ToolPaths{WorkspaceRoot: workspaceRoot, AllowedPaths: writeRoots}, ProtectedBranch: protectedBranch})
	tool.RegisterResearchTools(registry, nil, nil, tool.WebSearchConfig{})
	tool.RegisterImageTools(registry, imageToolsConfigFromPrizmConfig(cfg, workspaceRoot, writeRoots))
	// V32: State management tools
	chatStateMgr := state.NewManager(workspaceRoot)
	chatStateMgr.EnsureDir()
	tool.RegisterStateTools(registry, chatStateMgr)

	// V32: Plan-First Pipeline tools (guard created after ctxBuilder)

	toolPolicy := tool.DefaultPolicyConfig()
	toolPolicy.MaxFileSize = 10 * 1024 * 1024
	toolPolicy.WorkspaceRoot = workspaceRoot
	toolPolicy.AllowedPaths = cfg.Prizm.AllowedPaths
	toolPolicy.ReadRoots = readRoots
	toolPolicy.WriteRoots = writeRoots
	toolPolicy.OrchestratorAgentID = configuredOrchestratorAgentID(cfg)
	toolExec := tool.NewExecutor(registry, &toolPolicy)
	toolExec.SetApprovalStore(approval.NewStore(cfg.Prizm.RunsDir))

	// 7. Set up context builder
	var ctxBuilder *context.Builder
	if cfg.Prizm.Workspace != "" {
		ctxBuilder = context.NewBuilder(cfg.Prizm.Workspace)
	} else {
		ctxBuilder = context.NewBuilder(filepath.Join(os.Getenv("HOME"), ".openclaw", "workspace"))
	}

	// 8. Set up action registry (empty for chat mode)
	actionReg := action.NewRegistry()

	// V32: Plan-First Pipeline tools and guard
	chatPlanMgr := plan.NewManager(workspaceRoot)
	chatPlanMgr.EnsureDir()
	tool.RegisterPlanTools(registry, chatPlanMgr)
	var chatGuardian *guard.Guard
	if ctxBuilder != nil {
		chatGuardian = guard.NewGuard(chatPlanMgr, ctxBuilder)
	}

	// 9. Set up task store and delegation engine
	var taskStore *task.Store
	var delegEngine *delegation.Engine
	taskStore, taskErr := task.NewStore(filepath.Join(cfg.Prizm.DataDir, "tasks.db"))
	if taskErr != nil {
		log.Printf("[WARN] task store failed: %v (delegation disabled)", taskErr)
	} else {
		delegEngine = delegation.NewEngine(taskStore, natsConn)
	}

	// 9.5. Set up memory store and injector
	var chatMemStore *memory.MarkdownStore
	var chatMemInjector *MemoryInjector
	if cfg.Memory.StoreType != "" || cfg.Memory.StorePath != "" {
		memPath := filepath.Join(cfg.Prizm.DataDir, "memory")
		if cfg.Memory.StorePath != "" {
			memPath = cfg.Memory.StorePath
			if !filepath.IsAbs(memPath) {
				memPath = filepath.Join(cfg.Prizm.Workspace, memPath)
			}
		}
		chatMemStore = memory.NewMarkdownStore(memPath)

		// V80: Embedding index for semantic search
		if cfg.Memory.EmbeddingEnabled {
			embPath := cfg.Memory.EmbeddingIndexPath
			if embPath == "" {
				embPath = filepath.Join(memPath, "embeddings.json")
			}
			if !filepath.IsAbs(embPath) {
				embPath = filepath.Join(cfg.Prizm.Workspace, embPath)
			}
			embModel := cfg.Memory.EmbeddingModel
			if embModel == "" {
				embModel = "nomic-embed-text"
			}
			embURL := cfg.Memory.EmbeddingURL
			if embURL == "" {
				embURL = "http://localhost:11434"
			}
			embDims := cfg.Memory.EmbeddingDimensions
			if embDims == 0 {
				embDims = 768
			}
			embIdx := memory.NewEmbeddingIndex(memory.EmbeddingConfig{
				Enabled:          true,
				Model:           embModel,
				URL:             embURL,
				Dimensions:      embDims,
				IndexPath:       embPath,
				ReindexOnStartup: cfg.Memory.EmbeddingReindexOnStartup,
			})
			if err := embIdx.Load(); err != nil {
				log.Printf("[CHAT-EMBEDDING] failed to load index: %v", err)
			}
			chatMemStore.SetEmbeddingIndex(embIdx)
		}

		// V79: Smart memory injector — query planner + search + recent modes
		var queryPlanner *memory.QueryPlanner
		if cfg.Memory.QueryPlannerEnabled {
			queryPlanner = memory.NewQueryPlanner(memory.QueryPlanConfig{
				Enabled:  true,
				Model:    cfg.Memory.QueryPlannerModel,
				OllamaURL: cfg.Prizm.OllamaURL,
				Timeout:  time.Duration(cfg.Memory.QueryPlannerTimeoutS) * time.Second,
				Fallback: "heuristic",
			})
		}
		chatMemInjector = NewMemoryInjector(chatMemStore, queryPlanner)
	}

	// 10. Build chat context
	cc := &chatContext{
		router:      rtr,
		sessMgr:     sessMgr,
		cfg:         cfg,
		providers:   provReg,
		ctxBuilder:  ctxBuilder,
		toolExec:    toolExec,
		toolPolicy:  toolPolicy,
		eventLog:    &runtrack.EventLogger{},
		cancelReg:   runtrack.NewCancelRegistry(),
		actionReg:   actionReg,
		taskStore:   taskStore,
		delegEngine: delegEngine,
		natsConn:    natsConn,
		natsURL:     natsURL,
		natsCleanup: natsCleanup,
		rateLimiter: &chatRateLimit{minDelay: 500 * time.Millisecond}, // 2 msg/s max
		stateMgr:    chatStateMgr,
		planMgr:     chatPlanMgr,
		guardian:    chatGuardian,
		memInjector: chatMemInjector,
		memoryStore: chatMemStore,
	}

	// 10.5. Pre-build static system content (only needs to be done once)
	cc.buildStaticSystemContent(agentCfg)
	sess, ownerID, err := getOrCreateSessionForMessage(sessMgr, cfg, agentID, "cli", "terminal", "local-user")
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading session: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("  Session: %s\n", sess.ID[:8])
	fmt.Println()
	fmt.Println("Type your message and press Enter. Type /quit or Ctrl+C to exit.")
	fmt.Println("Type /reset to start a new session, /tools to list available tools.")
	fmt.Println()

	// 12. Enter chat loop with graceful shutdown
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1MB max input line

	done := make(chan struct{})
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sigChan:
			fmt.Println("\nGoodbye! ✨")
			close(done)
		case <-done:
			// Normal exit
		}
	}()

LOOP:
	for {
		select {
		case <-done:
			break LOOP
		default:
		}

		fmt.Print("\n> ")
		if !scanner.Scan() {
			break // EOF
		}
		input := strings.TrimSpace(scanner.Text())
		if input == "" {
			continue
		}

		// Handle commands
		switch {
		case input == "/quit" || input == "/exit":
			fmt.Println("Goodbye! ✨")
			return // deferred cleanup will run
		case input == "/reset":
			sess, err = sessMgr.Create(agentID, "cli", "terminal", ownerID)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error creating session: %v\n", err)
				continue
			}
			fmt.Printf("New session: %s\n\n", sess.ID[:8])
			continue
		case input == "/tools":
			toolInfos := toolExec.Registry.ListWithDescriptions()
			fmt.Printf("Available tools (%d):\n", len(toolInfos))
			for _, ti := range toolInfos {
				fmt.Printf("  %-20s %s\n", ti.Name, ti.Description)
			}
			continue
		case input == "/help":
			fmt.Println("Commands:")
			fmt.Println("  /quit, /exit  — Exit chat")
			fmt.Println("  /reset        — Start new session")
			fmt.Println("  /tools        — List available tools")
			fmt.Println("  /help         — Show this help")
			continue
		case strings.HasPrefix(input, "/"):
			fmt.Printf("Unknown command: %s (type /help for commands)\n", input)
			continue
		}

		// Rate limiting
		if !cc.rateLimiter.Allow() {
			fmt.Println("⚠️ Slow down! Please wait a moment between messages.")
			continue
		}

		// Security: prompt injection defense (same as Discord pipeline)
		injectionCheck := safety.CheckPromptInjection(input)
		if injectionCheck.Severity == "critical" {
			fmt.Println("⚠️ That message contains potentially dangerous content and was blocked for safety.")
			continue
		}
		sanitizedInput := input
		if injectionCheck.Severity == "high" {
			sanitizedInput = safety.SanitizeInput(input)
		}

		sess, ownerID, err = getOrCreateSessionForMessage(sessMgr, cfg, agentID, "cli", "terminal", "local-user")
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error refreshing session: %v\n", err)
			continue
		}

		// Add user message to session
		if _, err := sessMgr.AddMessage(sess.ID, "user", sanitizedInput, ""); err != nil {
			fmt.Fprintf(os.Stderr, "Error saving message: %v\n", err)
			continue
		}

		// Process through the pipeline
		fmt.Println() // blank line before response
		spinner := startThinkingSpinner()
		responseText, err := cc.processMessage(ctxcontext.Background(), sess, agentCfg, sanitizedInput)
		spinner.Stop() // stop and clear the "Thinking..." indicator
		if err != nil {
			switch {
			case errors.Is(err, ctxcontext.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded") || strings.Contains(err.Error(), "context deadline"):
				fmt.Fprintf(os.Stderr, "⏱️ Request timed out. The model took too long to respond. Please try again.\n")
			case isModelBackendCrash(err):
				fmt.Fprint(os.Stderr, modelBackendCrashHint(agentCfg))
			default:
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			}
			continue
		}

		// Save agent response to session
		if _, err := sessMgr.AddMessage(sess.ID, "agent", responseText, agentCfg.ID); err != nil {
			log.Printf("[WARN] failed to save agent message: %v", err)
		}
		enqueueLocalMemoryUpdate(sessMgr, cfg, nil, nil, nil, ownerID, "local-user", agentCfg.ID, sess.ID, "")

		// Display response
		fmt.Printf("%s: %s\n", agentCfg.ID, responseText)
	}
}

// processMessage runs the full pipeline for a user message and returns the response.
func (cc *chatContext) processMessage(
	parentCtx ctxcontext.Context,
	sess *session.Session,
	agentCfg *orchestrator.AgentConfig,
	userInput string,
) (string, error) {
	// 1. Route message
	result := cc.router.Route(userInput)

	// 2. Determine provider path — check ChatProvider availability per-call
	_, chatErr := cc.providers.GetChatProviderForAgent(agentCfg.ID, agentCfg.Model)
	chatAvailable := chatErr == nil

	// 3. Look up provider
	llmProvider, err := cc.providers.GetForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		return "", fmt.Errorf("no provider for model %s: %w", agentCfg.Model, err)
	}

	// 4. Set up run context with timeout
	runCtx, runCancel := ctxcontext.WithTimeout(parentCtx, 2*time.Minute)
	defer runCancel()

	run := runtrack.NewRun(result.AgentID, sess.ID, agentCfg.Model, agentCfg.Provider)
	run.Cancel = runCancel

	// 5. Branch on ChatProvider vs text-based
	if chatAvailable {
		return cc.processWithChatProvider(runCtx, sess, agentCfg, userInput)
	}

	// Text-based fallback path
	return cc.processWithTextProvider(runCtx, sess, agentCfg, userInput, llmProvider, run)
}

// processWithChatProvider uses the native ChatProvider interface for tool calling.
func (cc *chatContext) processWithChatProvider(
	ctx ctxcontext.Context,
	sess *session.Session,
	agentCfg *orchestrator.AgentConfig,
	userInput string,
) (string, error) {
	chatProv, err := cc.providers.GetChatProviderForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		return "", fmt.Errorf("chat provider unavailable: %w", err)
	}

	// Build messages from session
	messages := cc.buildChatMessages(sess, agentCfg, "", nil, userInput) // CLI chat has no channel context
	chatTools := cc.buildChatToolDefs()
	chatTools = filterChatToolsByAgentPolicy(chatTools, cc.toolPolicy, agentCfg.ID)

	finalResponse, toolSummaries, toolErr := cc.runChatToolLoop(
		ctx,
		messages,
		chatTools,
		chatProv,
		agentCfg,
	)
	if toolErr != nil {
		return "", fmt.Errorf("tool loop failed: %w", toolErr)
	}

	// Save tool interactions to session and display summaries
	for _, ts := range toolSummaries {
		status := "✓"
		if ts.Status == "error" {
			status = "✗"
		}
		clearThinkingLine()
		fmt.Printf("  [Tool: %s] %s\n", ts.Tool, status)

		// Persist tool call to session history
		toolMsg := fmt.Sprintf("[Tool: %s] %s", ts.Tool, ts.Status)
		if ts.Error != "" {
			toolMsg += " — " + ts.Error
		}
		cc.sessMgr.AddMessage(sess.ID, "tool", toolMsg, ts.Tool)
	}

	return finalResponse, nil
}

// processWithTextProvider uses the text-based Provider interface (fallback).
func (cc *chatContext) processWithTextProvider(
	ctx ctxcontext.Context,
	sess *session.Session,
	agentCfg *orchestrator.AgentConfig,
	userInput string,
	llmProvider provider.Provider,
	run *runtrack.Run,
) (string, error) {
	// Build the full prompt (same as Discord pipeline)
	prompt := cc.buildChatPrompt(sess, agentCfg, "", nil, userInput) // CLI chat has no channel context

	// Set up NATS for event pipeline (using persistent connection)
	var natsAdapter *natsPublisherAdapter
	if cc.natsConn != nil {
		natsAdapter = &natsPublisherAdapter{conn: cc.natsConn}
	}

	// Build pipeline stages — include DelegationStage like the serve path
	pipelineStages := []stage.Stage{
		&stage.LLMStage{},
	}
	if cc.delegEngine != nil {
		pipelineStages = append(pipelineStages, &stage.DelegationStage{
			Engine:       cc.delegEngine,
			StripMarkers: true,
			AgentConfigs: cc.buildAgentConfigMap(),
		})
	}
	if natsAdapter != nil {
		pipelineStages = append(pipelineStages,
			&stage.PersistenceStage{BusURL: cc.natsURL},
			&stage.EventPublishStage{Publisher: natsAdapter, BusURL: cc.natsURL},
		)
	}
	pipeline := stage.NewPipeline(pipelineStages...)

	rc := &stage.RunContext{
		RunID:          run.ID,
		Task:           prompt,
		Agent:          agentCfg.ID,
		Provider:       llmProvider,
		ProviderName:   agentCfg.Provider,
		Model:          agentCfg.Model,
		SessionID:      sess.ID,
		CleanedContent: strings.TrimSpace(userInput),
		RouteMethod:    "cli-chat",
	}

	finalRC, err := pipeline.Run(ctx, rc)
	if err != nil {
		return "", fmt.Errorf("pipeline failed: %w", err)
	}

	responseText := finalRC.LLMResponse

	// Check for text-based tool calls
	if cc.toolExec != nil {
		parsed := agent.ParseAgentOutputWithFallback(responseText)
		if parsed.Type == agent.ResponseToolRequest {
			finalResponse, toolSummaries, toolErr := cc.runTextToolLoop(
				ctx,
				prompt,
				agentCfg,
			)
			if toolErr != nil {
				return "", fmt.Errorf("text tool loop failed: %w", toolErr)
			}
			if finalResponse != "" {
				responseText = finalResponse
			}

			// Save tool interactions to session and display summaries
			for _, ts := range toolSummaries {
				status := "✓"
				if ts.Status == "error" {
					status = "✗"
				}
				clearThinkingLine()
				fmt.Printf("  [Tool: %s] %s\n", ts.Tool, status)

				toolMsg := fmt.Sprintf("[Tool: %s] %s", ts.Tool, ts.Status)
				if ts.Error != "" {
					toolMsg += " — " + ts.Error
				}
				cc.sessMgr.AddMessage(sess.ID, "tool", toolMsg, ts.Tool)
			}
		}
	}

	return responseText, nil
}

// buildAgentConfigMap creates a map of agent configs for the DelegationStage.
func (cc *chatContext) buildAgentConfigMap() map[string]*orchestrator.AgentConfig {
	m := make(map[string]*orchestrator.AgentConfig, len(cc.cfg.Agents)+1)
	for i := range cc.cfg.Agents {
		m[cc.cfg.Agents[i].ID] = &cc.cfg.Agents[i]
	}
	if cc.cfg.Codex.Enabled {
		m["codex"] = &orchestrator.AgentConfig{
			ID:           "codex",
			Role:         "coder",
			Provider:     "codex_cli",
			Model:        cc.cfg.Codex.Model,
			Capabilities: []string{"code", "test", "review", "report"},
		}
	}
	return m
}

// --- Helper methods (mirrors conversationContext methods for chat CLI) ---

// buildStaticSystemContent pre-builds the static portion of the system prompt.
// This includes agent identity, workspace context, conversation postfix, and tool instructions.
// It does NOT include dynamic session info (age, message count) which changes per message.
// Called once at startup. The result is reused for every message in the session.
// V33: Layered prompt assembly — identity from SOUL.md/IDENTITY.md, not generic config.
func (cc *chatContext) buildStaticSystemContent(agentCfg *orchestrator.AgentConfig) {
	var sb strings.Builder

	// --- Layer 1: IDENTITY ---
	// V33: Derive identity from workspace files, fall back to config id/role.
	identityContent := ""
	cc.hasSoulContent = false
	if cc.ctxBuilder != nil {
		builder := context.NewBuilder(cc.ctxBuilder.WorkspaceRoot).WithNamedContexts([]string{"soul", "identity"})
		injected, err := builder.Build()
		if err == nil {
			for _, f := range injected.Files {
				if f.Name == "soul" && f.Content != "" {
					identityContent = f.Content
					cc.hasSoulContent = true
				}
			}
		}
	}
	if identityContent == "" {
		identityContent = fmt.Sprintf("You are %s, a %s assistant.", agentCfg.ID, agentCfg.Role)
	}

	sb.WriteString("## Who You Are\n")
	sb.WriteString(identityContent + "\n\n")

	// --- Layer 2: WORKSPACE CONTEXT ---
	// Excluding soul/identity which are already in Layer 1
	if len(agentCfg.Context) > 0 && cc.ctxBuilder != nil {
		budget := cc.cfg.Prizm.ContextTokenBudget
		if budget <= 0 {
			budget = 4000
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
				sb.WriteString("## Context\n")
				sb.WriteString(injected.FormattedString + "\n")
			}
		}
	}

	// Layer 3 (Behavior/"How You Respond") and Layer 4 (Tools) are NOT
	// included in either static cache below — Layer 3 depends on the
	// resolved ChannelRole's Personality, which is only known per-message.
	// Both are computed dynamically in buildChatPrompt/buildChatMessages
	// instead, which unlike serve mode's ChatProvider-native path both
	// receive a ChannelRole.

	cc.staticSystemText = sb.String()

	// --- ChatProvider path: same layered format ---
	var sbChat strings.Builder
	sbChat.WriteString("## Who You Are\n")
	sbChat.WriteString(identityContent + "\n\n")

	if len(agentCfg.Context) > 0 && cc.ctxBuilder != nil {
		budget := cc.cfg.Prizm.ContextTokenBudget
		if budget <= 0 {
			budget = 4000
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

	cc.staticSystemChat = sbChat.String()
}

// buildChatPrompt builds a flat string prompt (for text-based providers).
// V33: Layered prompt with channel context support.
func (cc *chatContext) buildChatPrompt(sess *session.Session, agentCfg *orchestrator.AgentConfig, stateActionKey string, channelRole *orchestrator.ChannelRole, lastUserInput string) string {
	var sb strings.Builder

	// Static system content (cached, built once at startup)
	// Layers 1-2: Identity, Context
	sb.WriteString(cc.staticSystemText)

	// --- Layer 3: BEHAVIOR ---
	sb.WriteString("## How You Respond\n" + resolveConversationPostfix(agentCfg, channelRole, cc.hasSoulContent) + "\n\n")

	// --- Layer 4: TOOLS (text path) ---
	if cc.toolExec != nil {
		toolInfos := cc.toolExec.Registry.ListWithDescriptions()
		toolInfos = filterToolInfosByAgentPolicy(toolInfos, cc.toolPolicy, agentCfg.ID)
		if len(toolInfos) > 0 {
			sb.WriteString("## Tool Usage\n" + toolUsageGuidance + "\n\n")
			sb.WriteString(agent.BuildToolPromptSuffix(toolInfos, cc.ctxBuilder.WorkspaceRoot, cc.toolPolicy.ReadAllowedPaths()...))
		}
	}

	// V32: Working state injection (per-message, fresh state every time)
	if cc.stateMgr != nil {
		if statePrompt := cc.stateMgr.FormatStateForPrompt(); statePrompt != "" {
			sb.WriteString("\n" + statePrompt + "\n")
		}
	}

	// V32: Inject active plan into prompt
	if cc.planMgr != nil {
		if plans, err := cc.planMgr.LoadPlans(); err == nil {
			activePlan := plan.ActivePlan(plans)
			if activePlan != nil {
				sb.WriteString("\n" + plan.FormatPlanForPrompt(activePlan) + "\n")
			}
		}
	}

	// V33: Channel context injection. Personality is handled in Layer 3
	// above, not here — see resolveConversationPostfix.
	if channelRole != nil && channelRole.Context != "" {
		sb.WriteString("\n## Channel: #" + channelRole.Role + "\n")
		sb.WriteString(channelRole.Context + "\n\n")
	} else {
		// Backward compatibility: fall back to state_actions.inject
		if sa := cc.cfg.ResolveStateAction(agentCfg.ID, stateActionKey); sa != nil && sa.Inject != "" {
			sb.WriteString("\n## Context\n")
			sb.WriteString(sa.Inject)
			sb.WriteString("\n\n")
		}
	}

	// V79: Smart memory injection
	if cc.memInjector != nil {
		sessionAge := time.Since(sess.StartedAt)
		mode := ChooseMode(len(sess.Messages), sessionAge)
		memBlock := cc.memInjector.InjectMemories(ctxcontext.Background(), mode, lastUserInput, len(sess.Messages), 300)
		if memBlock != "" {
			sb.WriteString(memBlock + "\n")
		}
	} else if cc.memoryStore != nil {
		// Legacy fallback: inject recent memories
		recentMemories, memErr := cc.memoryStore.ListRecent(ctxcontext.Background(), 5)
		if memErr == nil && len(recentMemories) > 0 {
			var memSb strings.Builder
			memSb.WriteString("## Recent memories\n")
			memSb.WriteString("The following memories were automatically recalled from local storage:\n\n")
			for _, m := range recentMemories {
				memSb.WriteString(fmt.Sprintf("- %s\n", m.Content))
			}
			sb.WriteString(memSb.String() + "\n")
		}
	}

	// Dynamic session awareness
	sessionAge := time.Since(sess.StartedAt).Round(time.Second)
	sessionMsgCount := len(sess.Messages)
	sb.WriteString(fmt.Sprintf("\n[Session: %d messages, started %v ago]\n\n", sessionMsgCount, sessionAge))

	// History
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

// buildChatMessages builds structured ChatMessage array (for ChatProvider).
func (cc *chatContext) buildChatMessages(sess *session.Session, agentCfg *orchestrator.AgentConfig, stateActionKey string, channelRole *orchestrator.ChannelRole, lastUserInput string) []provider.ChatMessage {
	var messages []provider.ChatMessage

	// System message
	// Static system content (cached, built once at startup)
	// Layers 1-2: Identity, Context
	var systemContent string
	systemContent += cc.staticSystemChat

	// --- Layer 3: BEHAVIOR ---
	systemContent += "\n## How You Respond\n" + resolveConversationPostfix(agentCfg, channelRole, cc.hasSoulContent) + "\n"

	// --- Layer 4: TOOLS ---
	if cc.toolExec != nil {
		toolInfos := cc.toolExec.Registry.ListWithDescriptions()
		toolInfos = filterToolInfosByAgentPolicy(toolInfos, cc.toolPolicy, agentCfg.ID)
		if len(toolInfos) > 0 {
			systemContent += "\n## Tool Usage\n" + toolUsageGuidance + "\n"
		}
	}

	// V32: Working state injection (per-message, fresh state every time)
	if cc.stateMgr != nil {
		if statePrompt := cc.stateMgr.FormatStateForPrompt(); statePrompt != "" {
			systemContent += "\n" + statePrompt + "\n"
		}
	}

	// V32: Inject active plan into prompt
	if cc.planMgr != nil {
		if plans, err := cc.planMgr.LoadPlans(); err == nil {
			activePlan := plan.ActivePlan(plans)
			if activePlan != nil {
				systemContent += "\n" + plan.FormatPlanForPrompt(activePlan) + "\n"
			}
		}
	}

	// V33: Channel context injection. Personality is handled in Layer 3
	// above, not here — see resolveConversationPostfix.
	if channelRole != nil && channelRole.Context != "" {
		systemContent += "\n## Channel: #" + channelRole.Role + "\n"
		systemContent += channelRole.Context + "\n\n"
	} else {
		// Backward compatibility: fall back to state_actions.inject
		if sa := cc.cfg.ResolveStateAction(agentCfg.ID, stateActionKey); sa != nil && sa.Inject != "" {
			systemContent += "\n## Context\n" + sa.Inject + "\n\n"
		}
	}

	// V79: Smart memory injection
	if cc.memInjector != nil {
		sessionAge := time.Since(sess.StartedAt)
		mode := ChooseMode(len(sess.Messages), sessionAge)
		memBlock := cc.memInjector.InjectMemories(ctxcontext.Background(), mode, lastUserInput, len(sess.Messages), 300)
		if memBlock != "" {
			systemContent += memBlock + "\n"
		}
	} else if cc.memoryStore != nil {
		// Legacy fallback: inject recent memories
		recentMemories, memErr := cc.memoryStore.ListRecent(ctxcontext.Background(), 5)
		if memErr == nil && len(recentMemories) > 0 {
			systemContent += "## Recent memories\nThe following memories were automatically recalled from local storage:\n\n"
			for _, m := range recentMemories {
				systemContent += fmt.Sprintf("- %s\n", m.Content)
			}
			systemContent += "\n"
		}
	}

	// Dynamic session awareness
	sessionAge := time.Since(sess.StartedAt).Round(time.Second)
	sessionMsgCount := len(sess.Messages)
	systemContent += fmt.Sprintf("\n[Session: %d messages, started %v ago]\n", sessionMsgCount, sessionAge)

	messages = append(messages, provider.ChatMessage{
		Role:    "system",
		Content: systemContent,
	})

	// History
	for _, msg := range sess.Messages {
		switch msg.Role {
		case "user":
			messages = append(messages, provider.ChatMessage{
				Role:    "user",
				Content: msg.Content,
			})
		case "agent":
			messages = append(messages, provider.ChatMessage{
				Role:    "assistant",
				Content: msg.Content,
			})
		case "system":
			messages = append(messages, provider.ChatMessage{
				Role:    "system",
				Content: msg.Content,
			})
		}
	}

	return messages
}

// buildChatToolDefs converts tool registry to ChatTool format.
func (cc *chatContext) buildChatToolDefs() []provider.ChatTool {
	toolInfos := cc.toolExec.Registry.ListWithDescriptions()
	chatTools := make([]provider.ChatTool, 0, len(toolInfos))

	for _, ti := range toolInfos {
		params := map[string]any{
			"type":       "object",
			"properties": make(map[string]any),
		}
		required := make([]string, 0)

		for pname, spec := range ti.Schema.Input {
			props := map[string]any{
				"type":        spec.Type,
				"description": spec.Description,
			}
			params["properties"].(map[string]any)[pname] = props
			if spec.Required {
				required = append(required, pname)
			}
		}
		if len(required) > 0 {
			params["required"] = required
		}

		chatTools = append(chatTools, provider.ChatTool{
			Type: "function",
			Function: provider.FunctionDef{
				Name:        ti.Name,
				Description: ti.Description,
				Parameters:  params,
			},
		})
	}

	return chatTools
}

// runChatToolLoop is the CLI version of runToolLoopChat.
// It mirrors the Discord tool loop but prints to terminal instead of editing Discord messages.
func (cc *chatContext) runChatToolLoop(
	parentCtx ctxcontext.Context,
	messages []provider.ChatMessage,
	chatTools []provider.ChatTool,
	chatProv provider.ChatProvider,
	agentCfg *orchestrator.AgentConfig,
) (string, []toolCallSummary, error) {
	ctx, cancel := ctxcontext.WithTimeout(parentCtx, chatToolLoopTimeout)
	defer cancel()

	var summaries []toolCallSummary
	currentMessages := make([]provider.ChatMessage, len(messages))
	copy(currentMessages, messages)

	nudgeInjected := false
	var lastContent string

	for i := 0; i < maxChatToolIterations; i++ {
		if i >= 3 && !nudgeInjected {
			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "system",
				Content: "You have already used several tools. Please provide your final answer now based on the information you have gathered. Do not call any more tools.",
			})
			nudgeInjected = true
		}

		toolsForThisIteration := chatTools
		if i >= 6 {
			toolsForThisIteration = []provider.ChatTool{}
		}

		// Call the ChatProvider
		req := provider.ChatGenerateRequest{
			RunID:    fmt.Sprintf("chat-%d", i),
			Agent:    agentCfg.ID,
			Model:    agentCfg.Model,
			Messages: currentMessages,
			Tools:    toolsForThisIteration,
		}
		response, err := chatProv.ChatGenerate(ctx, req)
		if err != nil {
			return "", summaries, fmt.Errorf("LLM call failed iteration %d: %w", i+1, err)
		}

		if response.Content != "" {
			lastContent = response.Content
		}

		if !response.HasToolCalls() {
			return response.Content, summaries, nil
		}

		currentMessages = append(currentMessages, provider.ChatMessage{
			Role:      "assistant",
			Content:   response.Content,
			ToolCalls: response.ToolCalls,
		})

		for _, tc := range response.ToolCalls {
			// Format tool call arguments for display
			argsJSON, _ := json.Marshal(tc.Function.Arguments)
			clearThinkingLine()
			fmt.Printf("  🔧 %s(%s)\n", tc.Function.Name, string(argsJSON))
			toolResult, summary := cc.executeChatToolCLI(ctx, tc, agentCfg)
			fmt.Printf("     → %s\n", truncateStr(toolResult, 200))

			currentMessages = append(currentMessages, provider.ChatMessage{
				Role:    "tool",
				Content: toolResult,
				ToolID:  tc.ID,
			})
			summaries = append(summaries, summary)
		}
	}

	if lastContent != "" {
		return lastContent, summaries, nil
	}
	return "I gathered information but couldn't form a complete response. Please try again.", summaries, nil
}

// executeChatToolCLI executes a tool call and returns the result (CLI version).
func (cc *chatContext) executeChatToolCLI(
	ctx ctxcontext.Context,
	tc provider.ToolCall,
	agentCfg *orchestrator.AgentConfig,
) (string, toolCallSummary) {
	// tc.Function.Arguments is already map[string]any from the ChatProvider
	input := tc.Function.Arguments

	result, err := cc.toolExec.ExecuteWithPolicy(ctx, tc.Function.Name, agentCfg.ID, "prizm", "cli-chat", input)
	if err != nil {
		summary := toolCallSummary{
			Tool:   tc.Function.Name,
			Status: "error",
			Error:  err.Error(),
		}
		return fmt.Sprintf("Error executing tool: %v", err), summary
	}

	if !result.Success {
		summary := toolCallSummary{
			Tool:   tc.Function.Name,
			Status: "error",
			Error:  result.Error,
		}
		return fmt.Sprintf("Tool error: %s", result.Error), summary
	}

	resultJSON, _ := json.Marshal(result.Output)
	summary := toolCallSummary{
		Tool:   tc.Function.Name,
		Status: "success",
		Result: string(resultJSON),
	}
	return string(resultJSON), summary
}

// runTextToolLoop is the CLI version of runToolLoop for text-based providers.
func (cc *chatContext) runTextToolLoop(
	parentCtx ctxcontext.Context,
	prompt string,
	agentCfg *orchestrator.AgentConfig,
) (string, []toolCallSummary, error) {
	ctx, cancel := ctxcontext.WithTimeout(parentCtx, chatToolLoopTimeout)
	defer cancel()

	llmProvider, err := cc.providers.GetForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		return "", nil, fmt.Errorf("no provider for model %s: %w", agentCfg.Model, err)
	}

	currentPrompt := prompt
	var summaries []toolCallSummary
	nudgeInjected := false

	for i := 0; i < maxToolIterations; i++ {
		if i >= 3 && !nudgeInjected {
			currentPrompt += "\n\n[System: You have already used several tools. Please provide your final answer now based on the information you have gathered. Do not call any more tools.]"
			nudgeInjected = true
		}

		genResp, err := llmProvider.Generate(ctx, provider.GenerateRequest{
			RunID:  fmt.Sprintf("chat-text-%d", i),
			Agent:  agentCfg.ID,
			Model:  agentCfg.Model,
			Prompt: currentPrompt,
		})
		if err != nil {
			return "", summaries, fmt.Errorf("LLM call failed iteration %d: %w", i+1, err)
		}

		responseText := genResp.Text
		parsed := agent.ParseAgentOutputWithFallback(responseText)
		if parsed.Type != agent.ResponseToolRequest {
			return responseText, summaries, nil
		}

		var toolInput map[string]any
		if parsed.ToolInput != nil {
			toolInput = parsed.ToolInput
		} else {
			toolInput = map[string]any{}
		}

		result, err := cc.toolExec.ExecuteWithPolicy(ctx, parsed.ToolName, agentCfg.ID, "prizm", "cli-chat", toolInput)
		var resultStr string
		summary := toolCallSummary{Tool: parsed.ToolName}

		if err != nil {
			resultStr = fmt.Sprintf("Error executing tool: %v", err)
			summary.Status = "error"
			summary.Error = err.Error()
		} else if !result.Success {
			resultStr = fmt.Sprintf("Tool error: %s", result.Error)
			summary.Status = "error"
			summary.Error = result.Error
		} else {
			resultJSON, _ := json.Marshal(result.Output)
			resultStr = string(resultJSON)
			summary.Status = "success"
			summary.Result = resultStr
		}

		clearThinkingLine()
		fmt.Printf("  🔧 %s → %s\n", parsed.ToolName, truncateStr(resultStr, 200))
		summaries = append(summaries, summary)

		currentPrompt += fmt.Sprintf("\n\n[Tool Result for %s]: %s\n\n%s:", parsed.ToolName, resultStr, agentCfg.ID)
	}

	return "", summaries, fmt.Errorf("max tool iterations (%d) reached", maxToolIterations)
}

// findAgentConfig finds an agent config by ID.
func findAgentConfig(cfg *orchestrator.Config, agentID string) *orchestrator.AgentConfig {
	for i := range cfg.Agents {
		if cfg.Agents[i].ID == agentID {
			return &cfg.Agents[i]
		}
	}
	return nil
}
