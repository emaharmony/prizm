package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ReflectionSink applies the existing memory gate and a deterministic project
// policy before storing a reflector lesson. Gate errors fail closed.
type ReflectionSink struct {
	Gate   *GateExtractor
	Store  MemoryStore
	Scoped *Facade
}

// StoreScopedReflectionMemory records a reflector lesson through the same
// scoped, idempotent lifecycle used by autonomous memory tools. The legacy
// method remains for callers that have not yet supplied task scope.
func (s *ReflectionSink) StoreScopedReflectionMemory(ctx context.Context, scope Scope, summary, content, category string, topics []string) error {
	if s == nil || s.Scoped == nil {
		return errors.New("scoped reflection memory sink is not configured")
	}
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(content) == "" {
		return errors.New("reflection memory summary and content are required")
	}
	if len(content) > 16*1024 {
		return errors.New("reflection memory content exceeds 16KiB")
	}
	switch category {
	case "decision", "feedback", "project", "reference":
	default:
		return fmt.Errorf("reflection memory category %q is not allowed", category)
	}
	if s.Gate != nil {
		gate, err := s.Gate.Gate(ctx, summary+"\n"+content)
		if err != nil {
			return fmt.Errorf("reflection memory gate: %w", err)
		}
		if gate == nil || !gate.ShouldRemember {
			return errors.New("reflection memory gate rejected candidate")
		}
	}
	_, _, err := s.Scoped.Capture(ctx, CaptureRequest{Scope: scope, Summary: summary, Content: content, Category: category, Topics: append([]string(nil), topics...), Source: "prizm:reflection"})
	return err
}

func (s *ReflectionSink) StoreReflectionMemory(ctx context.Context, project, summary, content, category string, topics []string) error {
	if s == nil || s.Gate == nil || s.Store == nil {
		return errors.New("reflection memory sink is not configured")
	}
	project = strings.TrimSpace(project)
	if project == "" {
		return errors.New("reflection memory project is required")
	}
	if strings.TrimSpace(summary) == "" || strings.TrimSpace(content) == "" {
		return errors.New("reflection memory summary and content are required")
	}
	if len(content) > 16*1024 {
		return errors.New("reflection memory content exceeds 16KiB")
	}
	switch category {
	case "decision", "feedback", "project", "reference":
	default:
		return fmt.Errorf("reflection memory category %q is not allowed", category)
	}
	gate, err := s.Gate.Gate(ctx, summary+"\n"+content)
	if err != nil {
		return fmt.Errorf("reflection memory gate: %w", err)
	}
	if gate == nil || !gate.ShouldRemember {
		return errors.New("reflection memory gate rejected candidate")
	}
	now := time.Now().UTC()
	_, err = s.Store.Store(ctx, Memory{Content: content, Category: category, Tier: "active", Summary: summary, KeyTopics: append([]string(nil), topics...), Source: "prizm:reflection", ProjectID: project, CreatedAt: now, AccessedAt: now})
	return err
}
