package memory

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// ConversationTurn represents a single exchange between user and agent.
// It is the input to AutoExtract.
type ConversationTurn struct {
	UserMessage    string   // what the user said
	AgentResponse  string   // what the agent replied
	ToolSummaries  []string // tool calls made during this turn (optional)
	AgentID        string   // which agent handled this
	SessionID      string   // session context
	ProjectID      string   // project context
}

// AutoExtractor combines GateExtractor, MemoryStore, and EventEmitter into
// a single fire-and-forget call. It runs the full gate → extract → store
// pipeline automatically after each conversation turn.
//
// All errors are logged but non-fatal — auto-extraction must never break
// the conversation. The caller should run AutoExtract in a goroutine.
type AutoExtractor struct {
	Gate   *GateExtractor
	Store  MemoryStore
	Events *EventEmitter
}

// NewAutoExtractor creates an AutoExtractor from the given components.
func NewAutoExtractor(gate *GateExtractor, store MemoryStore, events *EventEmitter) *AutoExtractor {
	return &AutoExtractor{Gate: gate, Store: store, Events: events}
}

// AutoExtract runs the full memory pipeline on a conversation turn:
//  1. Concatenate the turn into a single text
//  2. Gate — decide if it's worth remembering
//  3. Extract — get structured memory (category, tier, summary, topics, content)
//  4. Store — persist to the MemoryStore
//  5. Emit events for observability
//
// Returns nil if the gate rejects the turn. Returns an error only if the
// store step fails (gate and extract errors are logged and non-fatal).
// The caller should run this in a goroutine — it must never block the conversation.
func (a *AutoExtractor) AutoExtract(ctx context.Context, turn ConversationTurn) error {
	if a == nil || a.Gate == nil || a.Store == nil {
		return nil
	}

	// Step 1: Build the conversation text for gate + extract
	text := buildTurnText(turn)
	if strings.TrimSpace(text) == "" {
		return nil
	}

	// Step 2: Gate — is this worth remembering?
	gateResult, err := a.Gate.Gate(ctx, text)
	if err != nil {
		log.Printf("[MEMORY-AUTO] gate error (non-fatal): %v", err)
		// Gate failure is non-fatal — proceed without gate check
		gateResult = &GateResult{ShouldRemember: true, Reasoning: "gate error, proceeding"}
	}

	if !gateResult.ShouldRemember {
		log.Printf("[MEMORY-AUTO] gate rejected: %s", gateResult.Reasoning)
		if a.Events != nil {
			_ = a.Events.EmitGateRejected(gateResult.Reasoning, "")
		}
		return nil
	}

	// Step 3: Extract structured memory
	extractResult, err := a.Gate.Extract(ctx, text)
	if err != nil {
		log.Printf("[MEMORY-AUTO] extract error (non-fatal): %v", err)
		// Use the raw text as content if extraction fails
		extractResult = &ExtractResult{
			Category:  "reference",
			Tier:      "active",
			Summary:   truncateText(text, 100),
			KeyTopics: []string{},
			Content:   text,
		}
	}

	// Step 4: Build memory and store
	mem := Memory{
		ID:         "", // Store will generate ULID
		Content:    extractResult.Content,
		Category:   extractResult.Category,
		Tier:       extractResult.Tier,
		Summary:    extractResult.Summary,
		KeyTopics:  extractResult.KeyTopics,
		Source:     "prizm:auto-extract",
		AgentID:    turn.AgentID,
		SessionID:  turn.SessionID,
		ProjectID:  turn.ProjectID,
		CreatedAt:  time.Now(),
		AccessedAt: time.Now(),
	}

	id, err := a.Store.Store(ctx, mem)
	if err != nil {
		log.Printf("[MEMORY-AUTO] store error (non-fatal): %v", err)
		return fmt.Errorf("auto-extract store: %w", err)
	}
	mem.ID = id

	log.Printf("[MEMORY-AUTO] extracted memory %s (category=%s, tier=%s): %s",
		id, extractResult.Category, extractResult.Tier, extractResult.Summary)

	// Step 5: Emit events for observability
	if a.Events != nil {
		if err := a.Events.EmitMemoryWrite(mem, ""); err != nil {
			log.Printf("[MEMORY-AUTO] event emission error (non-fatal): %v", err)
		}
	}

	return nil
}

// buildTurnText concatenates a conversation turn into a single text suitable
// for gate and extract prompts.
func buildTurnText(turn ConversationTurn) string {
	var b strings.Builder
	if turn.UserMessage != "" {
		b.WriteString("User: ")
		b.WriteString(turn.UserMessage)
	}
	if turn.AgentResponse != "" {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("Agent: ")
		b.WriteString(turn.AgentResponse)
	}
	if len(turn.ToolSummaries) > 0 {
		b.WriteString("\n\nTools used: ")
		b.WriteString(strings.Join(turn.ToolSummaries, ", "))
	}
	return b.String()
}

// truncateText returns the first n characters of s, with "..." if truncated.
func truncateText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	// Truncate at rune boundary to avoid splitting multi-byte UTF-8
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "..."
}