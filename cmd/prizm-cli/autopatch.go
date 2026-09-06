package main

import (
	ctxcontext "context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/autopatch"
	"github.com/emaharmony/prizm/internal/orchestrator"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/task"
	"github.com/emaharmony/prizm/internal/validation"
	"github.com/nats-io/nats.go"
)

func buildAutoPatchService(cfg *orchestrator.Config, store *task.Store, providers *provider.ProviderRegistry, nc *nats.Conn) *autopatch.Service {
	if cfg == nil {
		return nil
	}
	clean := true
	if cfg.Autopatch.RequireCleanWorktree != nil {
		clean = *cfg.Autopatch.RequireCleanWorktree
	}
	artifactRoot := filepath.Join(".prizm", "data", "autopatch")
	if cfg.Prizm.DataDir != "" {
		artifactRoot = filepath.Join(cfg.Prizm.DataDir, "autopatch")
	}
	workers := map[string]autopatch.PatchWorker{}
	if cfg.Codex.Enabled {
		workers["codex"] = autopatch.NewCodexWorker(codexConfigFromOrchestrator(cfg.Codex, cfg))
	}
	if local := buildLocalAutoPatchWorker(cfg, providers); local != nil {
		workers["local_agent"] = local
	}
	return autopatch.NewService(autopatch.Config{
		Enabled:              cfg.Autopatch.Enabled,
		Mode:                 cfg.Autopatch.Mode,
		RequireCleanWorktree: clean,
		MaxAttempts:          cfg.Autopatch.MaxAttempts,
		ValidationProfiles:   append([]string(nil), cfg.Autopatch.ValidationProfiles...),
		WorkerOrder:          append([]string(nil), cfg.Autopatch.WorkerOrder...),
		LocalAgent:           cfg.Autopatch.LocalAgent,
		Root:                 ".",
		BaseBranch:           cfg.Autopatch.BaseBranch,
		WorktreeRoot:         cfg.Autopatch.WorktreeRoot,
		ArtifactRoot:         artifactRoot,
		Store:                store,
		Registry:             validation.NewRegistry(),
		Workers:              workers,
		Emit: func(eventType, source string, payload map[string]any) {
			if nc == nil || !nc.IsConnected() {
				return
			}
			data, err := json.Marshal(payload)
			if err != nil {
				return
			}
			_ = nc.Publish(eventType, data)
		},
	})
}

func buildLocalAutoPatchWorker(cfg *orchestrator.Config, providers *provider.ProviderRegistry) autopatch.PatchWorker {
	if cfg == nil || providers == nil {
		return nil
	}
	agentID := cfg.Autopatch.LocalAgent
	if agentID == "" {
		agentID = "forge"
	}
	var agentCfg *orchestrator.AgentConfig
	for i := range cfg.Agents {
		if cfg.Agents[i].ID == agentID {
			agentCfg = &cfg.Agents[i]
			break
		}
	}
	if agentCfg == nil {
		return nil
	}
	p, err := providers.GetForAgent(agentCfg.ID, agentCfg.Model)
	if err != nil {
		log.Printf("[AUTOPATCH] local agent provider unavailable for %s/%s: %v", agentID, agentCfg.Model, err)
		return nil
	}
	return autopatch.NewLocalAgentWorker(p, agentCfg.Model, agentID)
}

func (cc *conversationContext) handleAutoPatchRequest(msg ChannelMessage, content string) {
	if cc.autopatcher == nil || !cc.autopatcher.Enabled() {
		cc.sender.Send(msg.ChannelID, "Autopatch is not enabled. Add `autopatch.enabled: true` to prizm.yaml and configure Codex or a local patch agent.")
		return
	}
	tsk, err := cc.autopatcher.Start(ctxcontext.Background(), autopatch.Request{
		Description: content,
		Source:      "manual",
		SubmittedBy: "discord:" + msg.UserID,
	})
	if err != nil {
		status := "I couldn't start autopatch"
		if errors.Is(err, autopatch.ErrDirtyWorktree) {
			status = "Autopatch requires a clean git worktree before it can start"
		}
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   fmt.Sprintf("%s: %s", status, err.Error()),
		})
		return
	}
	cc.bot.Send(&discordbot.OutboundMessage{
		ChannelID: msg.ChannelID,
		Content:   fmt.Sprintf("Started autopatch task `%s`. I will diagnose the bug, propose a patch in an isolated worktree, run validation, and store review artifacts.", tsk.ID),
	})
	go cc.postAutoPatchCompletion(msg.ChannelID, tsk.ID)
}

func (cc *conversationContext) postAutoPatchCompletion(channelID, taskID string) {
	if cc.taskStore == nil {
		return
	}
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	deadline := time.After(45 * time.Minute)
	for {
		select {
		case <-deadline:
			cc.sender.Send(channelID, fmt.Sprintf("Autopatch task `%s` is still running. Check `/api/v1/tasks/%s` for status.", taskID, taskID))
			return
		case <-ticker.C:
			tsk, err := cc.taskStore.Get(taskID)
			if err != nil {
				continue
			}
			if tsk.Status == task.StatusCompleted || tsk.Status == task.StatusFailed || tsk.Status == task.StatusCancelled {
				cc.sender.Send(channelID, formatAutoPatchTaskMessage(tsk))
				return
			}
		}
	}
}

func formatAutoPatchTaskMessage(tsk *task.Task) string {
	if tsk == nil {
		return "Autopatch finished, but the task record is unavailable."
	}
	status, _ := tsk.Result["status"].(string)
	patchPath, _ := tsk.Result["patch_path"].(string)
	reviewPath, _ := tsk.Result["review_path"].(string)
	diffStat, _ := tsk.Result["diff_stat"].(string)
	if len(diffStat) > 700 {
		diffStat = strings.TrimSpace(diffStat[:700]) + "\n..."
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Autopatch task `%s` finished with status `%s`.", tsk.ID, firstNonEmpty(status, string(tsk.Status))))
	if patchPath != "" {
		b.WriteString("\nPatch: `" + patchPath + "`")
	}
	if reviewPath != "" {
		b.WriteString("\nReview: `" + reviewPath + "`")
	}
	if diffStat != "" {
		b.WriteString("\n```text\n" + diffStat + "\n```")
	}
	return b.String()
}

func startAutoPatchValidationSubscriber(svc *autopatch.Service, nc *nats.Conn) {
	if svc == nil || !svc.Enabled() || nc == nil || !nc.IsConnected() {
		return
	}
	var (
		mu   sync.Mutex
		seen = map[string]time.Time{}
	)
	_, err := nc.Subscribe("prizm.validation.*", func(msg *nats.Msg) {
		subject := msg.Subject
		if subject != "prizm.validation.failed" && subject != "prizm.validation.timeout" {
			return
		}
		var payload map[string]any
		if err := json.Unmarshal(msg.Data, &payload); err != nil {
			return
		}
		profile := stringValue(payload, "profile_name")
		if profile == "" {
			profile = stringValue(payload, "profile")
		}
		errText := stringValue(payload, "error")
		key := subject + "|" + profile + "|" + errText
		mu.Lock()
		if last, ok := seen[key]; ok && time.Since(last) < 10*time.Minute {
			mu.Unlock()
			return
		}
		seen[key] = time.Now()
		mu.Unlock()

		desc := fmt.Sprintf("Validation failure detected from %s profile %q. Error: %s", subject, profile, errText)
		if _, err := svc.Start(ctxcontext.Background(), autopatch.Request{
			Description:        desc,
			Source:             "validation",
			ValidationProfiles: []string{firstNonEmpty(profile, "go_test_all")},
			RunID:              stringValue(payload, "run_id"),
			SubmittedBy:        "validation:" + subject,
		}); err != nil {
			log.Printf("[AUTOPATCH] validation-triggered task skipped: %v", err)
		}
	})
	if err != nil {
		log.Printf("[AUTOPATCH] validation subscriber failed: %v", err)
	}
}

func stringValue(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if value, ok := payload[key].(string); ok {
		return value
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
