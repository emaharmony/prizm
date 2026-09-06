package main

import (
	ctxcontext "context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/adapter/builtin/discordbot"
	"github.com/emaharmony/prizm/internal/codesummary"
	"github.com/emaharmony/prizm/internal/provider"
	"github.com/emaharmony/prizm/internal/task"
	"github.com/rs/xid"
)

func (cc *conversationContext) handleCodebaseSummaryRequest(msg ChannelMessage, content string) {
	if cc.taskStore == nil {
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   "I can't start a codebase summary because the task store is unavailable.",
		})
		return
	}

	select {
	case cc.summarySem <- struct{}{}:
	default:
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   "A codebase summary is already running. Check `/api/v1/tasks?status=in_progress` for status.",
		})
		return
	}

	route := cc.router.Route(content)
	agentID := route.AgentID
	agentCfg := cc.findAgentConfig(agentID)
	if agentCfg == nil {
		agentCfg = cc.cfg.PrimaryAgent()
		if agentCfg != nil {
			agentID = agentCfg.ID
		}
	}

	var llm provider.Provider
	model := ""
	if agentCfg != nil {
		model = agentCfg.Model
		if p, err := cc.providers.GetForAgent(agentCfg.ID, agentCfg.Model); err == nil {
			llm = p
		} else {
			log.Printf("[SUMMARY] provider unavailable for %s: %v; using deterministic report only", agentCfg.Model, err)
		}
	}
	if agentID == "" {
		agentID = "prizm"
	}

	taskID := "summary-" + xid.New().String()
	now := time.Now()
	tsk := &task.Task{
		ID:          taskID,
		Type:        "codebase_summary",
		Status:      task.StatusCreated,
		DelegatedBy: "discord:" + msg.UserID,
		DelegatedTo: agentID,
		Description: content,
		Priority:    "normal",
		Context: map[string]any{
			"channel_id": msg.ChannelID,
			"user_id":    msg.UserID,
			"mode":       "architecture_map",
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := cc.taskStore.Create(tsk); err != nil {
		<-cc.summarySem
		log.Printf("[SUMMARY] create task failed: %v", err)
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: msg.ChannelID,
			Content:   "I couldn't create the codebase summary task.",
		})
		return
	}

	cc.bot.Send(&discordbot.OutboundMessage{
		ChannelID: msg.ChannelID,
		Content:   fmt.Sprintf("Started codebase summary task `%s`. I will scan the repo, write artifacts, and post the architecture summary when it finishes.", taskID),
	})
	cc.publishEvent("prizm.codebase_summary.started", map[string]any{
		"task_id":    taskID,
		"agent":      agentID,
		"channel_id": msg.ChannelID,
	})

	go cc.runCodebaseSummaryTask(taskID, msg.ChannelID, agentID, model, llm)
}

func (cc *conversationContext) runCodebaseSummaryTask(taskID, channelID, agentID, model string, llm provider.Provider) {
	defer func() { <-cc.summarySem }()

	if err := cc.taskStore.UpdateStatus(taskID, task.StatusInProgress, nil); err != nil {
		log.Printf("[SUMMARY] mark in_progress failed for %s: %v", taskID, err)
	}

	root, err := os.Getwd()
	if err != nil {
		root = "."
	}
	dataDir := ".prizm/data"
	if cc.cfg != nil && cc.cfg.Prizm.DataDir != "" {
		dataDir = cc.cfg.Prizm.DataDir
	}
	artifactDir := filepath.Join(dataDir, "codebase-summary", taskID)

	timeout := 20 * time.Minute
	if cc.cfg != nil && cc.cfg.Prizm.LLMTimeoutSeconds > 0 {
		timeout = time.Duration(cc.cfg.Prizm.LLMTimeoutSeconds) * time.Second
	}
	ctx, cancel := ctxcontext.WithTimeout(ctxcontext.Background(), timeout)
	defer cancel()

	result, runErr := codesummary.Run(ctx, codesummary.Config{
		Root:        root,
		ArtifactDir: artifactDir,
		TaskID:      taskID,
		AgentID:     agentID,
		Project:     "prizm",
		Model:       model,
		Provider:    llm,
		Timeout:     timeout,
	})
	if runErr != nil {
		log.Printf("[SUMMARY] task %s failed: %v", taskID, runErr)
		cc.taskStore.UpdateStatus(taskID, task.StatusFailed, map[string]any{
			"error": runErr.Error(),
		})
		cc.bot.Send(&discordbot.OutboundMessage{
			ChannelID: channelID,
			Content:   fmt.Sprintf("Codebase summary task `%s` failed: %s", taskID, runErr.Error()),
		})
		cc.publishEvent("prizm.codebase_summary.failed", map[string]any{
			"task_id": taskID,
			"error":   runErr.Error(),
		})
		return
	}

	resultMap := resultToMap(result)
	if err := cc.taskStore.UpdateStatus(taskID, task.StatusCompleted, resultMap); err != nil {
		log.Printf("[SUMMARY] mark completed failed for %s: %v", taskID, err)
	}

	cc.bot.Send(&discordbot.OutboundMessage{
		ChannelID: channelID,
		Content:   formatCodebaseSummaryReply(taskID, result),
	})
	cc.publishEvent("prizm.codebase_summary.completed", map[string]any{
		"task_id":       taskID,
		"report_path":   result.ReportPath,
		"evidence_path": result.EvidencePath,
		"files_scanned": result.FilesScanned,
		"packages_seen": result.PackagesSeen,
		"duration_ms":   result.DurationMS,
	})
}

func resultToMap(result codesummary.Result) map[string]any {
	data, _ := json.Marshal(result)
	var out map[string]any
	json.Unmarshal(data, &out)
	return out
}

func formatCodebaseSummaryReply(taskID string, result codesummary.Result) string {
	excerpt := strings.TrimSpace(result.Excerpt)
	if len(excerpt) > 1300 {
		excerpt = strings.TrimSpace(excerpt[:1300]) + "\n..."
	}
	return fmt.Sprintf("Codebase summary task `%s` completed.\n\n%s\n\nReport: `%s`\nEvidence: `%s`\nScanned: %d files, %d packages in %dms.",
		taskID,
		excerpt,
		result.ReportPath,
		result.EvidencePath,
		result.FilesScanned,
		result.PackagesSeen,
		result.DurationMS,
	)
}
