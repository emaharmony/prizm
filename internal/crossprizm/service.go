package crossprizm

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/nats-io/nats.go"
)

// Handler processes a verified inbound cross-Prizm message.
type Handler func(ctx context.Context, subject string, msg Message) (*Message, error)

// Service subscribes to signed protocol subjects on the active NATS bus.
type Service struct {
	nc      *nats.Conn
	cfg     ServiceConfig
	handler Handler
	subs    []*nats.Subscription
}

// ServiceConfig configures a cross-Prizm listener.
type ServiceConfig struct {
	InstanceID      string
	Secret          string
	AllowedSubjects []string
	MaxAge          time.Duration
}

// NewService creates a cross-Prizm protocol service.
func NewService(nc *nats.Conn, cfg ServiceConfig, handler Handler) (*Service, error) {
	if nc == nil {
		return nil, fmt.Errorf("crossprizm: NATS connection is required")
	}
	if cfg.InstanceID == "" {
		return nil, fmt.Errorf("crossprizm: instance ID is required")
	}
	if cfg.Secret == "" {
		return nil, fmt.Errorf("crossprizm: shared secret is required")
	}
	if len(cfg.AllowedSubjects) == 0 {
		cfg.AllowedSubjects = DefaultSubjects()
	}
	if cfg.MaxAge == 0 {
		cfg.MaxAge = 5 * time.Minute
	}
	if handler == nil {
		handler = func(context.Context, string, Message) (*Message, error) { return nil, nil }
	}
	return &Service{nc: nc, cfg: cfg, handler: handler}, nil
}

// InstanceID returns the instance ID of this cross-Prizm service.
func (s *Service) InstanceID() string {
	return s.cfg.InstanceID
}

// Start subscribes to allowed protocol subjects.
func (s *Service) Start(ctx context.Context) error {
	for _, subject := range s.cfg.AllowedSubjects {
		subject := subject
		sub, err := s.nc.Subscribe(subject, func(natsMsg *nats.Msg) {
			s.handle(ctx, subject, natsMsg)
		})
		if err != nil {
			s.Close()
			return fmt.Errorf("crossprizm: subscribe %q: %w", subject, err)
		}
		s.subs = append(s.subs, sub)
	}
	if err := s.nc.Flush(); err != nil {
		s.Close()
		return fmt.Errorf("crossprizm: flush subscriptions: %w", err)
	}
	return nil
}

// Close unsubscribes all protocol subscriptions.
func (s *Service) Close() {
	for _, sub := range s.subs {
		_ = sub.Unsubscribe()
	}
	s.subs = nil
}

// Publish signs and sends a cross-Prizm message on the given subject.
func (s *Service) Publish(subject string, msg Message) error {
	if msg.From == "" {
		msg.From = s.cfg.InstanceID
	}
	if msg.Timestamp == "" {
		msg.Timestamp = time.Now().UTC().Format(time.RFC3339Nano)
	}
	if msg.Nonce == "" {
		msg.Nonce = newNonce()
	}
	if err := msg.Sign(s.cfg.Secret); err != nil {
		return fmt.Errorf("crossprizm: publish sign: %w", err)
	}
	payload, err := msg.Payload()
	if err != nil {
		return fmt.Errorf("crossprizm: publish payload: %w", err)
	}
	if err := s.nc.Publish(subject, payload); err != nil {
		return fmt.Errorf("crossprizm: publish: %w", err)
	}
	return s.nc.Flush()
}

func (s *Service) handle(ctx context.Context, subject string, natsMsg *nats.Msg) {
	msg, err := Decode(natsMsg.Data)
	if err != nil {
		log.Printf("[CROSS-PRIZM] rejecting malformed message on %s: %v", subject, err)
		return
	}
	if msg.To != "" && msg.To != s.cfg.InstanceID {
		return
	}
	if err := msg.Verify(s.cfg.Secret, s.cfg.MaxAge); err != nil {
		log.Printf("[CROSS-PRIZM] rejecting unsigned/invalid message from %q on %s: %v", msg.From, subject, err)
		return
	}

	resp, err := s.handler(ctx, subject, msg)
	if err != nil {
		log.Printf("[CROSS-PRIZM] handler failed for %s from %q: %v", subject, msg.From, err)
		resp = &Message{
			From:          s.cfg.InstanceID,
			To:            msg.From,
			MessageType:   TypeTaskResponse,
			CorrelationID: msg.CorrelationID,
			Response: map[string]any{
				"status": "failed",
				"error":  err.Error(),
			},
			Timestamp: time.Now().UTC().Format(time.RFC3339Nano),
			Nonce:     newNonce(),
		}
	}
	if resp == nil {
		return
	}
	if resp.From == "" {
		resp.From = s.cfg.InstanceID
	}
	if resp.To == "" {
		resp.To = msg.From
	}
	if resp.CorrelationID == "" {
		resp.CorrelationID = msg.CorrelationID
	}
	if resp.MessageType == "" {
		resp.MessageType = TypeTaskResponse
	}
	if err := resp.Sign(s.cfg.Secret); err != nil {
		log.Printf("[CROSS-PRIZM] failed to sign response: %v", err)
		return
	}
	payload, err := resp.Payload()
	if err != nil {
		log.Printf("[CROSS-PRIZM] failed to encode response: %v", err)
		return
	}
	subjectOut := SubjectForType(resp.MessageType)
	if err := s.nc.Publish(subjectOut, payload); err != nil {
		log.Printf("[CROSS-PRIZM] failed to publish response: %v", err)
	}
}
