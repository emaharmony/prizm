package stage

import (
	"context"
	"fmt"
	"testing"

	"github.com/emaharmony/prizm/internal/event"
	"github.com/emaharmony/prizm/internal/session"
)

// mockSessionManager implements SessionManager for testing.
type mockSessionManager struct {
	session   *session.Session
	message   *session.Message
	createErr error
	findErr   error
	addMsgErr error
	created   bool
}

func (m *mockSessionManager) FindActive(channelType, channelID, userID string) (*session.Session, error) {
	if m.findErr != nil {
		return nil, m.findErr
	}
	if m.session != nil {
		return m.session, nil
	}
	return nil, nil
}

func (m *mockSessionManager) Create(agentID, channelType, channelID, userID string) (*session.Session, error) {
	if m.createErr != nil {
		return nil, m.createErr
	}
	m.created = true
	if m.session == nil {
		m.session = &session.Session{ID: "sess-test-123"}
	}
	return m.session, nil
}

func (m *mockSessionManager) AddMessage(sessionID, role, content, agentID string) (*session.Message, error) {
	if m.addMsgErr != nil {
		return nil, m.addMsgErr
	}
	m.message = &session.Message{ID: "msg-test-123"}
	return m.message, nil
}

// mockNatsPublisher implements NatsPublisher for testing.
type mockNatsPublisher struct {
	published []string
	data      [][]byte
	err       error
}

func (m *mockNatsPublisher) Publish(subject string, data []byte) error {
	if m.err != nil {
		return m.err
	}
	m.published = append(m.published, subject)
	m.data = append(m.data, data)
	return nil
}

// --- RoutingStage Tests ---

func TestRoutingStage_Name(t *testing.T) {
	s := &RoutingStage{}
	if s.Name() != "routing" {
		t.Errorf("expected name 'routing', got %q", s.Name())
	}
}

func TestRoutingStage_NoRouter(t *testing.T) {
	s := &RoutingStage{}
	rc := &RunContext{RunID: "test-run", Task: "hello"}
	err := s.Validate(rc)
	if err == nil {
		t.Error("expected validation error for nil router")
	}
}

func TestRoutingStage_NoProviderForModel(t *testing.T) {
	s := &RoutingStage{
		Router:    nil, // Will fail Validate first
		Providers: nil,
	}
	rc := &RunContext{RunID: "test-run", Task: "hello"}
	err := s.Validate(rc)
	if err == nil {
		t.Error("expected validation error for nil providers")
	}
}

// --- SessionStage Tests ---

func TestSessionStage_Name(t *testing.T) {
	s := &SessionStage{}
	if s.Name() != "session" {
		t.Errorf("expected name 'session', got %q", s.Name())
	}
}

func TestSessionStage_FindExistingSession(t *testing.T) {
	existingSess := &session.Session{ID: "sess-existing"}
	sessMgr := &mockSessionManager{session: existingSess}

	s := &SessionStage{
		SessionMgr:  sessMgr,
		ChannelType: "discord",
		ChannelID:   "channel-123",
		UserID:      "user-456",
		RawContent:  "hello",
	}

	rc := &RunContext{RunID: "test-run", Task: "hello", Agent: "lumi"}
	newRC, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if newRC.SessionID != "sess-existing" {
		t.Errorf("expected session 'sess-existing', got %q", newRC.SessionID)
	}
	if sessMgr.created {
		t.Error("should not create a new session when one exists")
	}
}

func TestSessionStage_CreateNewSession(t *testing.T) {
	sessMgr := &mockSessionManager{}

	s := &SessionStage{
		SessionMgr:  sessMgr,
		ChannelType: "discord",
		ChannelID:   "channel-123",
		UserID:      "user-456",
		RawContent:  "hello",
	}

	rc := &RunContext{RunID: "test-run", Task: "hello", Agent: "lumi"}
	newRC, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got error: %s", result.Error)
	}
	if newRC.SessionID == "" {
		t.Error("expected session ID to be set")
	}
	if !sessMgr.created {
		t.Error("expected new session to be created")
	}
}

func TestSessionStage_NoSessionManager(t *testing.T) {
	s := &SessionStage{}
	rc := &RunContext{RunID: "test-run", Task: "hello"}
	err := s.Validate(rc)
	if err == nil {
		t.Error("expected validation error for nil session manager")
	}
}

func TestSessionStage_AddMessageFailure_NonFatal(t *testing.T) {
	sessMgr := &mockSessionManager{
		session:   &session.Session{ID: "sess-123"},
		addMsgErr: fmt.Errorf("db error"),
	}

	s := &SessionStage{
		SessionMgr:  sessMgr,
		ChannelType: "discord",
		ChannelID:   "channel-123",
		UserID:      "user-456",
		RawContent:  "hello",
	}

	rc := &RunContext{RunID: "test-run", Task: "hello", Agent: "lumi"}
	newRC, result, _ := s.Execute(context.Background(), rc)

	// Session stage should still succeed (add message failure is non-fatal)
	if !result.Success {
		t.Errorf("expected success despite add message failure, got: %s", result.Error)
	}
	if !newRC.SessionSaveFailed {
		t.Error("expected SessionSaveFailed flag to be set")
	}
}

func TestSessionStage_FindActiveError(t *testing.T) {
	sessMgr := &mockSessionManager{
		findErr: fmt.Errorf("connection error"),
	}

	s := &SessionStage{
		SessionMgr:  sessMgr,
		ChannelType: "discord",
		ChannelID:   "channel-123",
		UserID:      "user-456",
		RawContent:  "hello",
	}

	rc := &RunContext{RunID: "test-run", Task: "hello", Agent: "lumi"}
	_, result, _ := s.Execute(context.Background(), rc)

	if result.Success {
		t.Error("expected failure when FindActive errors")
	}
}

func TestSessionStage_CreateError(t *testing.T) {
	sessMgr := &mockSessionManager{
		createErr: fmt.Errorf("db connection failed"),
	}

	s := &SessionStage{
		SessionMgr:  sessMgr,
		ChannelType: "discord",
		ChannelID:   "channel-123",
		UserID:      "user-456",
		RawContent:  "hello",
	}

	rc := &RunContext{RunID: "test-run", Task: "hello", Agent: "lumi"}
	_, result, _ := s.Execute(context.Background(), rc)

	if result.Success {
		t.Error("expected failure when Create errors")
	}
}

// --- EventPublishStage Tests ---

func TestEventPublishStage_Name(t *testing.T) {
	s := &EventPublishStage{}
	if s.Name() != "event_publish" {
		t.Errorf("expected name 'event_publish', got %q", s.Name())
	}
}

func TestEventPublishStage_NilPublisher(t *testing.T) {
	s := &EventPublishStage{Publisher: nil, BusURL: ""}
	rc := &RunContext{
		RunID:  "test-run",
		Events: []event.Event{{ID: "evt-1", Type: "test.event"}},
	}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success when NATS not configured, got: %s", result.Error)
	}
	if result.Data["published"] != false {
		t.Error("expected published=false when NATS not configured")
	}
}

func TestEventPublishStage_PublishEvents(t *testing.T) {
	pub := &mockNatsPublisher{}
	s := &EventPublishStage{Publisher: pub, BusURL: "nats://localhost:4222"}

	rc := &RunContext{
		RunID: "test-run",
		Events: []event.Event{
			{ID: "evt-1", Type: "prizm.run.started", Source: "prizm"},
			{ID: "evt-2", Type: "lumi.llm.completed", Source: "lumi"},
		},
	}

	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	if result.Data["published"] != 2 {
		t.Errorf("expected 2 published events, got %v", result.Data["published"])
	}
	if len(pub.published) != 2 {
		t.Errorf("expected 2 NATS subjects, got %d", len(pub.published))
	}
}

func TestEventPublishStage_PartialFailure(t *testing.T) {
	pub := &mockNatsPublisher{
		err: fmt.Errorf("connection lost"), // All publishes fail
	}
	s := &EventPublishStage{Publisher: pub, BusURL: "nats://localhost:4222"}

	rc := &RunContext{
		RunID: "test-run",
		Events: []event.Event{
			{ID: "evt-1", Type: "prizm.run.started", Source: "prizm"},
			{ID: "evt-2", Type: "lumi.llm.completed", Source: "lumi"},
		},
	}

	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected infra error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success even with publish failures, got: %s", result.Error)
	}
	// All events should fail
	if result.Data["published"] != 0 {
		t.Errorf("expected 0 published, got %v", result.Data["published"])
	}
	if result.Data["failed"] != 2 {
		t.Errorf("expected 2 failed, got %v", result.Data["failed"])
	}
}

func TestEventPublishStage_EmptyEvents(t *testing.T) {
	pub := &mockNatsPublisher{}
	s := &EventPublishStage{Publisher: pub, BusURL: "nats://localhost:4222"}

	rc := &RunContext{RunID: "test-run", Events: []event.Event{}}
	_, result, err := s.Execute(context.Background(), rc)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Success {
		t.Errorf("expected success, got: %s", result.Error)
	}
	if result.Data["published"] != 0 {
		t.Errorf("expected 0 published for empty events, got %v", result.Data["published"])
	}
}

// --- RunContext With* Methods Tests ---

func TestRunContext_WithAgent(t *testing.T) {
	rc := &RunContext{RunID: "test", Agent: "old"}
	newRC := rc.WithAgent("lumi")
	if newRC.Agent != "lumi" {
		t.Errorf("expected agent 'lumi', got %q", newRC.Agent)
	}
	if rc.Agent != "old" {
		t.Error("original should not be mutated")
	}
}

func TestRunContext_WithSessionID(t *testing.T) {
	rc := &RunContext{RunID: "test"}
	newRC := rc.WithSessionID("sess-123")
	if newRC.SessionID != "sess-123" {
		t.Errorf("expected session 'sess-123', got %q", newRC.SessionID)
	}
	if rc.SessionID != "" {
		t.Error("original should not be mutated")
	}
}

func TestRunContext_WithModel(t *testing.T) {
	rc := &RunContext{RunID: "test", Model: "old-model"}
	newRC := rc.WithModel("glm-5.1:cloud")
	if newRC.Model != "glm-5.1:cloud" {
		t.Errorf("expected model 'glm-5.1:cloud', got %q", newRC.Model)
	}
	if rc.Model != "old-model" {
		t.Error("original should not be mutated")
	}
}

func TestRunContext_CopyOnWrite(t *testing.T) {
	rc := &RunContext{
		RunID:  "test",
		Agent:  "lumi",
		Model:  "glm-5.1:cloud",
		Events: []event.Event{{ID: "evt-1"}},
	}
	newRC := rc.WithAgent("mango")
	if newRC.Agent != "mango" {
		t.Errorf("expected agent 'mango', got %q", newRC.Agent)
	}
	if rc.Agent != "lumi" {
		t.Error("original should not be mutated")
	}
	// Verify other fields preserved
	if newRC.Model != "glm-5.1:cloud" {
		t.Errorf("model should be preserved, got %q", newRC.Model)
	}
	if len(newRC.Events) != 1 {
		t.Errorf("events should be preserved, got %d", len(newRC.Events))
	}
}
