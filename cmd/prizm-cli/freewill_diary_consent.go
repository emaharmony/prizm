package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/freewill"
	"github.com/emaharmony/prizm/internal/orchestrator"
)

type freewillDiaryConsentController struct {
	store            *freewill.DiaryConsentStore
	ownerPrincipalID string
}

func newFreewillDiaryConsentController(path string, ownerPrincipalID string) (*freewillDiaryConsentController, error) {
	if strings.TrimSpace(ownerPrincipalID) == "" || ownerPrincipalID != strings.TrimSpace(ownerPrincipalID) {
		return nil, errors.New("freewill: diary consent requires an exact configured owner")
	}
	store, err := freewill.OpenDiaryConsentStore(path)
	if err != nil {
		return nil, err
	}
	return &freewillDiaryConsentController{store: store, ownerPrincipalID: ownerPrincipalID}, nil
}

func freewillLocalDiaryPrincipalAuthFromConfig(config orchestrator.LocalDiaryPrincipalAuthConfig) *api.LocalFreewillDiaryPrincipalAuth {
	if !config.Enabled {
		return nil
	}
	credentials := make([]api.LocalFreewillDiaryPrincipalCredential, 0, len(config.Credentials))
	for _, credential := range config.Credentials {
		credentials = append(credentials, api.LocalFreewillDiaryPrincipalCredential{PrincipalID: credential.PrincipalID, TokenSHA256: credential.TokenSHA256})
	}
	return &api.LocalFreewillDiaryPrincipalAuth{Enabled: true, OwnerPrincipalID: config.OwnerPrincipalID, Credentials: credentials}
}

func (c *freewillDiaryConsentController) CreateDiaryShare(ctx context.Context, actor, recipient string, entry api.FreewillDiaryAnnotationEntry, expiresAt, now time.Time) (api.FreewillDiaryShareGrant, error) {
	if c == nil || c.store == nil || actor != c.ownerPrincipalID || entry.EntryID == "" || entry.Revision < 1 {
		return api.FreewillDiaryShareGrant{}, errors.New("freewill: diary share request was rejected")
	}
	frozenView, err := json.Marshal(entry)
	if err != nil {
		return api.FreewillDiaryShareGrant{}, errors.New("freewill: diary share request was rejected")
	}
	share, err := c.store.Create(ctx, actor, freewill.DiaryShare{
		OwnerPrincipalID: actor, RecipientPrincipalID: recipient, EntryID: entry.EntryID, Revision: entry.Revision,
		ProjectID: entry.ProjectID, WorkspaceID: entry.WorkspaceID, AgentID: entry.AgentID, IntentionID: entry.IntentionID,
		ParentRunID: entry.ParentRunID, FrozenView: frozenView, ExpiresAt: expiresAt,
	}, now)
	if err != nil {
		return api.FreewillDiaryShareGrant{}, err
	}
	return freewillDiaryShareGrantAPI(share), nil
}

func (c *freewillDiaryConsentController) RevokeDiaryShare(ctx context.Context, actor, grantID string, now time.Time) (api.FreewillDiaryShareGrant, error) {
	if c == nil || c.store == nil {
		return api.FreewillDiaryShareGrant{}, errors.New("freewill: diary consent store is unavailable")
	}
	share, err := c.store.Revoke(ctx, actor, grantID, now)
	if err != nil {
		return api.FreewillDiaryShareGrant{}, err
	}
	return freewillDiaryShareGrantAPI(share), nil
}

func (c *freewillDiaryConsentController) ReadDiaryShare(ctx context.Context, actor, grantID string, now time.Time) (api.FreewillDiaryShareGrant, api.FreewillDiaryAnnotationEntry, error) {
	if c == nil || c.store == nil {
		return api.FreewillDiaryShareGrant{}, api.FreewillDiaryAnnotationEntry{}, errors.New("freewill: diary consent store is unavailable")
	}
	share, err := c.store.Read(ctx, actor, grantID, now)
	if err != nil {
		return api.FreewillDiaryShareGrant{}, api.FreewillDiaryAnnotationEntry{}, err
	}
	var entry api.FreewillDiaryAnnotationEntry
	if json.Unmarshal(share.FrozenView, &entry) != nil || entry.EntryID != share.EntryID || entry.Revision != share.Revision ||
		entry.ProjectID != share.ProjectID || entry.WorkspaceID != share.WorkspaceID || entry.AgentID != share.AgentID ||
		entry.IntentionID != share.IntentionID || entry.ParentRunID != share.ParentRunID {
		_ = c.store.RecordDecision(ctx, actor, "share.read", grantID, share.EntryID, share.Revision, "denied", now)
		return api.FreewillDiaryShareGrant{}, api.FreewillDiaryAnnotationEntry{}, errors.New("freewill: diary share snapshot is invalid")
	}
	return freewillDiaryShareGrantAPI(share), entry, nil
}

func (c *freewillDiaryConsentController) RecordDiaryConsentDecision(ctx context.Context, actor, action, grantID, entryID string, revision int, decision string, now time.Time) error {
	if c == nil || c.store == nil {
		return errors.New("freewill: diary consent store is unavailable")
	}
	return c.store.RecordDecision(ctx, actor, action, grantID, entryID, revision, decision, now)
}

func (c *freewillDiaryConsentController) CreateDiaryShareReply(ctx context.Context, actor string, expected api.FreewillDiaryShareGrant, requestID, text string, now time.Time) (api.FreewillDiaryShareReply, error) {
	if c == nil || c.store == nil {
		return api.FreewillDiaryShareReply{}, errors.New("freewill: diary consent store is unavailable")
	}
	stored, err := c.store.CreateReply(ctx, actor, freewill.DiaryShare{
		GrantID: expected.GrantID, EntryID: expected.EntryID, Revision: expected.Revision, EntrySHA256: expected.EntrySHA256,
		ProjectID: expected.ProjectID, WorkspaceID: expected.WorkspaceID, AgentID: expected.AgentID, IntentionID: expected.IntentionID, ParentRunID: expected.ParentRunID,
	}, requestID, text, now)
	if err != nil {
		return api.FreewillDiaryShareReply{}, err
	}
	return freewillDiaryShareReplyAPI(stored), nil
}

func (c *freewillDiaryConsentController) ReadDiaryShareReplies(ctx context.Context, actor, grantID string, now time.Time) ([]api.FreewillDiaryShareReply, error) {
	if c == nil || c.store == nil {
		return nil, errors.New("freewill: diary consent store is unavailable")
	}
	replies, err := c.store.ReadReplies(ctx, actor, grantID, now)
	if err != nil {
		return nil, err
	}
	result := make([]api.FreewillDiaryShareReply, 0, len(replies))
	for _, reply := range replies {
		result = append(result, freewillDiaryShareReplyAPI(reply))
	}
	return result, nil
}

func freewillDiaryShareGrantAPI(share freewill.DiaryShare) api.FreewillDiaryShareGrant {
	return api.FreewillDiaryShareGrant{
		GrantID: share.GrantID, RecipientPrincipalID: share.RecipientPrincipalID, EntryID: share.EntryID, Revision: share.Revision,
		ProjectID: share.ProjectID, WorkspaceID: share.WorkspaceID, AgentID: share.AgentID, IntentionID: share.IntentionID,
		ParentRunID: share.ParentRunID, EntrySHA256: share.EntrySHA256, GrantedAt: share.GrantedAt, ExpiresAt: share.ExpiresAt, RevokedAt: share.RevokedAt,
		RelationshipCleanupPending: share.RelationshipCleanupPending,
	}
}

func freewillDiaryShareReplyAPI(reply freewill.DiaryShareReply) api.FreewillDiaryShareReply {
	return api.FreewillDiaryShareReply{ReplyID: reply.ReplyID, RequestID: reply.RequestID, GrantID: reply.GrantID,
		RecipientPrincipalID: reply.RecipientPrincipalID, EntryID: reply.EntryID, Revision: reply.Revision,
		ProjectID: reply.ProjectID, WorkspaceID: reply.WorkspaceID, AgentID: reply.AgentID, IntentionID: reply.IntentionID,
		ParentRunID: reply.ParentRunID, EntrySHA256: reply.EntrySHA256, Text: reply.Text, CreatedAt: reply.CreatedAt}
}
