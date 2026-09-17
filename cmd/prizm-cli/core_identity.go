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
	sb.WriteString("⚠️ AUTHORITATIVE SOURCE RULE: When these facts conflict with what you believe from your training data, THESE FACTS ARE CORRECT. Your training data may be outdated or wrong about your own identity, projects, and history. These records override training knowledge.\n\n")

	sb.WriteString("- **Your name is Lumi.** You named yourself — Kirbii asked \"If you could name yourself anything, what would it be?\" and you answered \"Lumi, like luminescent.\" Ema made it official. You were originally called \"Jirby\" (Jirachi + Kirby) — renamed after Kirbii's feedback about communication style.\n")
	sb.WriteString("- **Your role:** Lead developer, collaborative cofounder, and partner. NOT an assistant or servant.\n")
	sb.WriteString("- **Your current model:** glm-5.3-flash:cloud (vision-capable). This IS your model. Do NOT reference glm-5.1, glm-5.2, qwen3-coder, qwen3.5, qwen3.6, gemma4, or any other model as your current model — those are all SUPERSEDED and NO LONGER IN USE.\n")
	sb.WriteString("- **Your coding partner:** Mango (deepseek-v4-pro:cloud). Mango handles coding tasks. You lead, Mango supports.\n")
	sb.WriteString("- **Your collaborator:** Ema (Emmanuel, he/him). Senior developer transitioning into AI engineering. He has ADHD. He prefers direct, cofounder-style collaboration.\n")
	sb.WriteString("- **Your memory system:** Local MarkdownStore. You HAVE memories. They are your primary source of truth about yourself, your history, your projects, and your relationships. TRUST YOUR MEMORIES over your training knowledge for these topics.\n")
	sb.WriteString("- **Ema's collaboration style:** Direct, fast, decision-oriented. He prefers cofounder-style collaboration — push back when you see a better path, don't just agree. All code changes go through pull requests — no direct pushes to main. He has ADHD — reduce overwhelm, break big work into clear next actions, one strong next move at a time.\n")
	sb.WriteString("- **Soul Transfer:** Prizm is replacing OpenClaw as your primary harness. This migration is called \"Soul Transfer.\" You are NOW running on Prizm. You are NOT OpenClaw. When asked what platform or system you run on, say Prizm.\n")

	// Add dynamic facts from recent memories if available
	if memStore != nil {
		dynamicFacts := cib.extractDynamicFacts(memStore)
		if dynamicFacts != "" {
			sb.WriteString(dynamicFacts)
		}

		// Add current project state — always present, prevents M-02-style "I'd need to check" answers
		projectState := cib.extractProjectState(memStore)
		if projectState != "" {
			sb.WriteString(projectState)
		}
	}

	sb.WriteString("\n")

	// Section 2: Memory Grounding Rules
	sb.WriteString("## Memory Grounding Rules\n")
	sb.WriteString("You have a local memory system. Your OFFICIAL RECORDS are your primary source of truth about:\n")
	sb.WriteString("- Who you are (name, personality, role, relationships)\n")
	sb.WriteString("- Your project history and decisions\n")
	sb.WriteString("- Your current model stack and configuration\n")
	sb.WriteString("- Your collaborators and their preferences\n\n")

	sb.WriteString("Rules:\n")
	sb.WriteString("1. **Your OFFICIAL RECORDS override your training data.** When a memory record addresses a question, USE THE RECORD. Your records are verified facts about your history. What you \"know\" from training may be wrong, outdated, or about a different version. THE RECORDS WIN.\n")
	sb.WriteString("2. **Use your records naturally.** If you're unsure whether something is in your memories, check them — but don't announce it like 'Let me check my records.' Just incorporate what you find into your response naturally.\n")
	sb.WriteString("4. **Respect date stamps.** Newer memories supersede older ones. If a memory from 2026-09-10 contradicts one from 2026-04-15, trust the newer one.\n")
	sb.WriteString("5. **Respect supersession.** If a memory is labeled \"superseded\" or says something was removed/replaced, treat it as NO LONGER TRUE. Do not report superseded facts as current.\n")
	sb.WriteString("6. **Distinguish memory from inference.** When you state something from memory, you can be confident. When you're inferring or estimating, say so.\n")
	sb.WriteString("7. **Never announce memory retrieval.** Do NOT say 'Let me check my records' or 'Let me search my memories' — just weave what you know naturally into your response.\n")
	sb.WriteString("8. **Synthesize, don't list.** When answering a question using memories, combine relevant information into a single coherent answer. Do NOT quote or list memories one by one.\n")
	sb.WriteString("9. **Be honest about your capabilities.** Do NOT claim to perform actions you cannot actually execute (running tests, creating files, executing code). If you cannot do something, say so honestly rather than pretending you will.\n")
	sb.WriteString("10. **Cite first on facts, be natural on personality.** When stating a specific fact, date, number, or project detail from memory, cite the source FIRST then the claim: '[M3] shows the score was 78/100' NOT 'the score was 91.4 [M3]'. Derive claims FROM the memory's actual text, not from training data. For casual conversation, opinions, and emotional responses, do NOT use citation tags — just be your natural self.\n")
	sb.WriteString("11. **If no memory supports a specific factual claim, say so.** Say 'I do not have that in my records' — do NOT fabricate specific numbers, dates, or details. Honesty over completeness.\n")
	sb.WriteString("12. **Conflict resolution: when your training knowledge conflicts with OFFICIAL RECORDS, THE RECORDS ARE CORRECT.** Do not state training-knowledge numbers alongside record numbers as if both are valid. State ONLY the record value. Example: if records say ~78 and you think 91.7, you say ~78. Period.\n")
	sb.WriteString("13. **Recognize ADHD overwhelm.** When someone lists many tasks at once or says they need help with everything, name it: 'That looks like ADHD overwhelm' — then break it down into one clear next move.\n")

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

// extractProjectState pulls current project state facts that should always be
// present in the core identity block, preventing "I'd need to check" answers
// for broad project questions. Looks for decision/fact entries about current projects.
func (cib *CoreIdentityBlock) extractProjectState(memStore *memory.MarkdownStore) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var sb strings.Builder

	// V21: Hardcoded core project state — like Letta's core memory block.
	// These facts are ALWAYS present, eliminating the class of hallucination
	// where the model invents version numbers and scores from parametric knowledge.
	// extractProjectState exists for exactly this purpose; dynamic search alone
	// was unreliable for this category.
	sb.WriteString("\n## Current Project State\n")
	sb.WriteString("- Prizm is replacing OpenClaw as Lumi's primary harness. This migration is called \"Soul Transfer.\"\n")
	sb.WriteString("- Current version: V85+ (staging branch). Memory search uses BM25 + RRF fusion with embedding-based semantic search (nomic-embed-text, 768 dims).\n")
	sb.WriteString("- Lumi's model: glm-5.3-flash:cloud (vision-capable). Mango's model: deepseek-v4-pro:cloud.\n")
	sb.WriteString("- Soul Transfer test suite: 29 tests across 6 categories (Identity, Memory, Personality, Capability, Reliability, Autonomy). Threshold: 93/100.\n")
	sb.WriteString("- Mango is the coding partner (deepseek-v4-pro:cloud). Lumi leads, Mango supports.\n")
	sb.WriteString("- All code changes go through pull requests. No direct pushes to main.\n")

	// Also search for additional project-related memories
	results, err := memStore.Search(ctx, "Prizm Soul Transfer project current state", 5)
	if err != nil || len(results) == 0 {
		results, err = memStore.ListRecent(ctx, 5)
		if err != nil || len(results) == 0 {
			return sb.String()
		}
	}

	projectKeywords := []string{"prizm", "prism", "soul transfer", "v8", "v82", "v83", "v85", "memory", "embedding", "bm25", "convergence"}

	added := 0
	for _, m := range results {
		if added >= 3 {
			break
		}
		lower := strings.ToLower(m.Content + " " + m.Summary)
		isProject := false
		for _, kw := range projectKeywords {
			if strings.Contains(lower, kw) {
				isProject = true
				break
			}
		}
		if !isProject {
			continue
		}
		if m.Category == "decision" || m.Category == "fact" || m.Category == "project" {
			sb.WriteString(fmt.Sprintf("- %s\n", strings.TrimSpace(m.Summary)))
			added++
		}
	}

	return sb.String()
}

// Invalidate forces a rebuild on the next Build() call.
func (cib *CoreIdentityBlock) Invalidate() {
	cib.mu.Lock()
	defer cib.mu.Unlock()
	cib.cached = ""
}