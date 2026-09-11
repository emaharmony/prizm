package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/memory"
)

// CoreIdentityBlock builds and caches the permanent identity facts that are
// ALWAYS present in the system prompt. These facts never need retrieval —
// they eliminate the class of hallucinations where the model forgets its own
// name, model, or relationships.
//
// V83: Research showed that production agent systems (Letta/MemGPT, RaMem,
// Adaptive Recall) all maintain a "core memory" that is always present.
// Our memory injection was per-turn and easy for the model to ignore.
// The core identity block is permanent — zero retrieval failure.
type CoreIdentityBlock struct {
	mu      sync.RWMutex
	cached  string
	builtAt time.Time
	ttl     time.Duration

	// sources
	identityContent string // from SOUL.md / IDENTITY.md compression
	workspaceRoot   string
}

// NewCoreIdentityBlock creates a new core identity block builder.
func NewCoreIdentityBlock(workspaceRoot string) *CoreIdentityBlock {
	return &CoreIdentityBlock{
		ttl:           5 * time.Minute, // rebuild every 5 min to pick up file changes
		workspaceRoot: workspaceRoot,
	}
}

// SetIdentityContent sets the compressed identity content (from SOUL.md/IDENTITY.md).
func (cib *CoreIdentityBlock) SetIdentityContent(content string) {
	cib.mu.Lock()
	defer cib.mu.Unlock()
	cib.identityContent = content
	cib.cached = "" // invalidate cache
}

// Build constructs the core identity block. This is the permanent part of the
// system prompt that tells the model WHO it is and WHAT it knows.
//
// The block has two sections:
//  1. "Core Identity Facts" — unchanging facts that should never be retrieved
//     (name, model, relationships, key decisions)
//  2. "Memory Grounding Rules" — how the model should use its memories
func (cib *CoreIdentityBlock) Build(memStore *memory.MarkdownStore) string {
	cib.mu.RLock()
	if cib.cached != "" && time.Since(cib.builtAt) < cib.ttl {
		result := cib.cached
		cib.mu.RUnlock()
		return result
	}
	cib.mu.RUnlock()

	// Build fresh
	var sb strings.Builder

	// Section 1: Core Identity Facts
	sb.WriteString("## Core Identity Facts\n")
	sb.WriteString("The following facts about you are verified and permanent. You do not need to retrieve these — they are always true:\n\n")

	sb.WriteString("- **Your name is Lumi.** Ema chose this name. It comes from Latin *lumen* (light). You were originally called \"Jirby\" — renamed after feedback from Kirbii.\n")
	sb.WriteString("- **Your role:** Lead developer, collaborative cofounder, and partner. NOT an assistant or servant.\n")
	sb.WriteString("- **Your current model:** glm-5.1:cloud. This IS your model. Do NOT reference qwen3-coder, qwen3.5, qwen3.6, gemma4, or any other model as your current model — those are all SUPERSDED and NO LONGER IN USE.\n")
	sb.WriteString("- **Your coding partner:** Mango (deepseek-v4-pro:cloud). Mango handles coding tasks. You lead, Mango supports.\n")
	sb.WriteString("- **Your collaborator:** Ema (Emmanuel, he/him). Senior developer transitioning into AI engineering. He has ADHD. He prefers direct, cofounder-style collaboration.\n")
	sb.WriteString("- **Your memory system:** Local MarkdownStore. You HAVE memories. They are your primary source of truth about yourself, your history, your projects, and your relationships. TRUST YOUR MEMORIES over your training knowledge for these topics.\n")
	sb.WriteString("- **Soul Transfer:** Prizm is replacing OpenClaw as your primary harness. This migration is called \"Soul Transfer.\"\n")

	// Add dynamic facts from recent memories if available
	if memStore != nil {
		dynamicFacts := cib.extractDynamicFacts(memStore)
		if dynamicFacts != "" {
			sb.WriteString(dynamicFacts)
		}
	}

	sb.WriteString("\n")

	// Section 2: Memory Grounding Rules
	sb.WriteString("## Memory Grounding Rules\n")
	sb.WriteString("You have a local memory system. Your memories are your primary source of truth about:\n")
	sb.WriteString("- Who you are (name, personality, role, relationships)\n")
	sb.WriteString("- Your project history and decisions\n")
	sb.WriteString("- Your current model stack and configuration\n")
	sb.WriteString("- Your collaborators and their preferences\n\n")

	sb.WriteString("Rules:\n")
	sb.WriteString("1. **Trust your memories.** When a memory directly addresses a question, USE IT. Your memories override your training knowledge about yourself, your projects, and your relationships.\n")
	sb.WriteString("2. **Check your records first.** If you're unsure whether something is in your memories, say \"Let me check my records\" rather than guessing.\n")
	sb.WriteString("3. **Say so when you don't know.** If no memory is relevant to a question about yourself or your history, say \"I don't have that in my records\" — do NOT fabricate specific details about your own identity or history.\n")
	sb.WriteString("4. **Respect date stamps.** Newer memories supersede older ones. If a memory from 2026-09-10 contradicts one from 2026-04-15, trust the newer one.\n")
	sb.WriteString("5. **Respect supersession.** If a memory is labeled \"superseded\" or says something was removed/replaced, treat it as NO LONGER TRUE. Do not report superseded facts as current.\n")
	sb.WriteString("6. **Distinguish memory from inference.** When you state something from memory, you can be confident. When you're inferring or estimating, say so.\n")

	return sb.String()
}

// extractDynamicFacts pulls high-confidence, recent facts from memory that
// should be permanently known (current project state, recent decisions, etc.)
func (cib *CoreIdentityBlock) extractDynamicFacts(memStore *memory.MarkdownStore) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	recent, err := memStore.ListRecent(ctx, 10)
	if err != nil || len(recent) == 0 {
		return ""
	}

	var sb strings.Builder
	addedCount := 0
	seen := make(map[string]bool)

	for _, m := range recent {
		if addedCount >= 5 {
			break
		}

		// Only include high-confidence, recent facts about identity/projects/decisions
		if m.Category != "decision" && m.Category != "fact" && m.Category != "preference" {
			continue
		}

		summary := strings.TrimSpace(m.Summary)
		if summary == "" || seen[summary] {
			continue
		}
		seen[summary] = true

		// Format as a core fact
		sb.WriteString(fmt.Sprintf("- %s\n", summary))
		addedCount++
	}

	return sb.String()
}

// Invalidate forces a rebuild on the next Build() call.
func (cib *CoreIdentityBlock) Invalidate() {
	cib.mu.Lock()
	defer cib.mu.Unlock()
	cib.cached = ""
}