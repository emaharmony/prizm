package freewill

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/emaharmony/prizm/internal/sqlite"
)

const DiaryShareMaxLifetime = 24 * time.Hour

var ErrDiaryConsentAuditUnavailable = errors.New("freewill: diary consent audit is unavailable")
var ErrDiaryRelationshipSourceUnavailable = errors.New("freewill: relationship source reply is unavailable")

const DiaryShareReplyMaxBytes = 2000

// DiaryShare is one consented, immutable view of an exact diary entry revision.
// FrozenView contains diary text and is never copied into the audit table.
type DiaryShare struct {
	GrantID                    string
	OwnerPrincipalID           string
	RecipientPrincipalID       string
	EntryID                    string
	Revision                   int
	ProjectID                  string
	WorkspaceID                string
	AgentID                    string
	IntentionID                string
	ParentRunID                string
	EntrySHA256                string
	ExpiresAt                  time.Time
	GrantedAt                  time.Time
	RevokedAt                  *time.Time
	FrozenView                 []byte
	RelationshipCleanupPending bool
}

// DiaryConsentAudit is metadata only. It intentionally has no text or credential field.
type DiaryConsentAudit struct {
	OccurredAt  time.Time
	PrincipalID string
	Action      string
	Decision    string
	GrantID     string
	EntryID     string
	Revision    int
}

// DiaryShareReply is untrusted, attributed recipient commentary tied to the
// exact frozen share. It does not assert that the reply is true or actionable.
type DiaryShareReply struct {
	ReplyID              string
	RequestID            string
	GrantID              string
	RecipientPrincipalID string
	EntryID              string
	Revision             int
	ProjectID            string
	WorkspaceID          string
	AgentID              string
	IntentionID          string
	ParentRunID          string
	EntrySHA256          string
	Text                 string
	CreatedAt            time.Time
}

// DiaryConsentStore persists exact-entry grants and metadata-only decisions.
type DiaryConsentStore struct {
	db *sql.DB
}

func OpenDiaryConsentStore(directory string) (*DiaryConsentStore, error) {
	if strings.TrimSpace(directory) == "" {
		return nil, errors.New("freewill: diary consent directory is required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("freewill: create diary consent directory: %w", err)
	}
	if err := restrictDiaryConsentPermissions(directory, true); err != nil {
		return nil, errors.New("freewill: diary consent directory permissions could not be restricted")
	}
	path := filepath.Join(directory, "consent.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("freewill: create diary consent database: %w", err)
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("freewill: close diary consent database bootstrap: %w", err)
	}
	if err := restrictDiaryConsentPermissions(path, false); err != nil {
		return nil, errors.New("freewill: diary consent database permissions could not be restricted")
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		if _, err := os.Stat(sidecar); err == nil {
			if err := restrictDiaryConsentPermissions(sidecar, false); err != nil {
				return nil, errors.New("freewill: diary consent sidecar permissions could not be restricted")
			}
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("freewill: open diary consent database: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA busy_timeout = 5000; PRAGMA journal_mode = WAL; PRAGMA foreign_keys = ON; PRAGMA secure_delete = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("freewill: configure diary consent database: %w", err)
	}
	const schema = `
CREATE TABLE IF NOT EXISTS freewill_diary_shares (
	grant_id TEXT PRIMARY KEY,
	owner_principal_id TEXT NOT NULL,
	recipient_principal_id TEXT NOT NULL,
	entry_id TEXT NOT NULL,
	entry_revision INTEGER NOT NULL,
	project_id TEXT NOT NULL,
	workspace_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	intention_id TEXT NOT NULL,
	parent_run_id TEXT NOT NULL,
	entry_sha256 TEXT NOT NULL,
	frozen_view BLOB NOT NULL,
	granted_at TEXT NOT NULL,
	expires_at TEXT NOT NULL,
	revoked_at TEXT,
	CHECK (owner_principal_id <> recipient_principal_id),
	CHECK (entry_revision > 0)
);
CREATE INDEX IF NOT EXISTS idx_freewill_diary_shares_recipient ON freewill_diary_shares(recipient_principal_id, expires_at);
CREATE TABLE IF NOT EXISTS freewill_diary_consent_audit (
	audit_id INTEGER PRIMARY KEY AUTOINCREMENT,
	occurred_at TEXT NOT NULL,
	principal_id TEXT NOT NULL,
	action TEXT NOT NULL,
	decision TEXT NOT NULL,
	grant_id TEXT NOT NULL DEFAULT '',
	entry_id TEXT NOT NULL DEFAULT '',
	entry_revision INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS freewill_diary_share_replies (
	reply_id TEXT PRIMARY KEY,
	request_id TEXT NOT NULL,
	grant_id TEXT NOT NULL,
	recipient_principal_id TEXT NOT NULL,
	entry_id TEXT NOT NULL,
	entry_revision INTEGER NOT NULL,
	project_id TEXT NOT NULL,
	workspace_id TEXT NOT NULL,
	agent_id TEXT NOT NULL,
	intention_id TEXT NOT NULL,
	parent_run_id TEXT NOT NULL,
	entry_sha256 TEXT NOT NULL,
	reply_text TEXT NOT NULL,
	created_at TEXT NOT NULL,
	UNIQUE(grant_id, request_id),
	FOREIGN KEY(grant_id) REFERENCES freewill_diary_shares(grant_id),
	CHECK(entry_revision > 0)
);
CREATE INDEX IF NOT EXISTS idx_freewill_diary_share_replies_grant ON freewill_diary_share_replies(grant_id, created_at, reply_id);
CREATE TRIGGER IF NOT EXISTS freewill_diary_share_replies_no_update BEFORE UPDATE ON freewill_diary_share_replies BEGIN SELECT RAISE(ABORT, 'diary share replies are append-only'); END;
CREATE TRIGGER IF NOT EXISTS freewill_diary_share_replies_no_delete BEFORE DELETE ON freewill_diary_share_replies BEGIN SELECT RAISE(ABORT, 'diary share replies are append-only'); END;`
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("freewill: initialize diary consent schema: %w", err)
	}
	if err := initRelationshipSchema(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("freewill: initialize relationship consent schema: %w", err)
	}
	if err := restrictDiaryConsentPermissions(path, false); err != nil {
		_ = db.Close()
		return nil, errors.New("freewill: diary consent database permissions could not be restricted")
	}
	return &DiaryConsentStore{db: db}, nil
}

func restrictDiaryConsentPermissions(path string, directory bool) error {
	if runtime.GOOS == "windows" {
		current, err := user.Current()
		if err != nil || !strings.HasPrefix(current.Uid, "S-") {
			return errors.New("current Windows user SID is unavailable")
		}
		before, err := windowsDiaryConsentACEs(path)
		if err != nil {
			return err
		}
		for _, ace := range before {
			if !ace.inherited && (!diaryConsentACEIsCurrentUser(ace.principal, current) || ace.deny) {
				return errors.New("preexisting explicit Windows ACL is not limited to the current user")
			}
		}
		rights := "*" + current.Uid + ":F"
		if directory {
			rights = "*" + current.Uid + ":(OI)(CI)F"
		}
		cmd := exec.Command("icacls", path, "/inheritance:r", "/grant:r", rights)
		if err := cmd.Run(); err != nil {
			return errors.New("windows ACL restriction failed")
		}
		after, err := windowsDiaryConsentACEs(path)
		if err != nil {
			return err
		}
		foundCurrent := false
		for _, ace := range after {
			if ace.inherited || ace.deny || !diaryConsentACEIsCurrentUser(ace.principal, current) {
				return errors.New("resulting Windows ACL is broader than the current user")
			}
			foundCurrent = true
		}
		if !foundCurrent {
			return errors.New("resulting Windows ACL has no current-user access")
		}
		return nil
	}
	permissions := os.FileMode(0600)
	if directory {
		permissions = 0700
	}
	if err := os.Chmod(path, permissions); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Mode().Perm() != permissions {
		return errors.New("file permissions remain broader than requested")
	}
	return nil
}

type diaryConsentWindowsACE struct {
	principal string
	inherited bool
	deny      bool
}

func windowsDiaryConsentACEs(path string) ([]diaryConsentWindowsACE, error) {
	output, err := exec.Command("icacls", path).Output()
	if err != nil {
		return nil, errors.New("could not inspect Windows diary consent ACL")
	}
	var aces []diaryConsentWindowsACE
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		pathPrefix := strings.Trim(path, "\"")
		if len(line) > len(pathPrefix) && strings.EqualFold(line[:len(pathPrefix)], pathPrefix) {
			line = strings.TrimSpace(line[len(pathPrefix):])
		}
		marker := strings.Index(line, ":(")
		if marker < 1 {
			continue
		}
		principal := strings.TrimSpace(line[:marker])
		rights := line[marker+1:]
		aces = append(aces, diaryConsentWindowsACE{principal: principal, inherited: strings.Contains(rights, "(I)"), deny: strings.Contains(strings.ToUpper(rights), "(DENY)")})
	}
	if len(aces) == 0 {
		return nil, errors.New("windows diary consent ACL inspection returned no ACEs")
	}
	return aces, nil
}

func diaryConsentACEIsCurrentUser(principal string, current *user.User) bool {
	principal = strings.TrimSpace(principal)
	return strings.EqualFold(principal, current.Username) || strings.EqualFold(principal, "*"+current.Uid) || strings.EqualFold(principal, current.Uid)
}

func (s *DiaryConsentStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *DiaryConsentStore) Create(ctx context.Context, actor string, share DiaryShare, now time.Time) (DiaryShare, error) {
	if s == nil || s.db == nil {
		return DiaryShare{}, errors.New("freewill: diary consent store is unavailable")
	}
	share.GrantedAt = now.UTC()
	share.ExpiresAt = share.ExpiresAt.UTC()
	if !validSyntheticPrincipal(actor) || actor != share.OwnerPrincipalID || !validSyntheticPrincipal(share.RecipientPrincipalID) || actor == share.RecipientPrincipalID ||
		share.EntryID == "" || share.EntryID != strings.TrimSpace(share.EntryID) || share.Revision < 1 ||
		share.ProjectID == "" || share.WorkspaceID == "" || share.AgentID == "" || share.IntentionID == "" || share.ParentRunID == "" ||
		len(share.FrozenView) == 0 || !json.Valid(share.FrozenView) || !diaryShareSnapshotMatches(share) || !share.ExpiresAt.After(now) || share.ExpiresAt.After(now.Add(DiaryShareMaxLifetime)) {
		if err := s.RecordDecision(ctx, actor, "share.grant", "", share.EntryID, share.Revision, "denied", now); err != nil {
			return DiaryShare{}, ErrDiaryConsentAuditUnavailable
		}
		return DiaryShare{}, errors.New("freewill: diary share request was rejected")
	}
	digest := sha256.Sum256(share.FrozenView)
	share.EntrySHA256 = hex.EncodeToString(digest[:])
	grantID, err := newDiaryShareID()
	if err != nil {
		if auditErr := s.RecordDecision(ctx, actor, "share.grant", "", share.EntryID, share.Revision, "denied", now); auditErr != nil {
			return DiaryShare{}, ErrDiaryConsentAuditUnavailable
		}
		return DiaryShare{}, errors.New("freewill: diary share request was rejected")
	}
	share.GrantID = grantID
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO freewill_diary_shares
		(grant_id, owner_principal_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, frozen_view, granted_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, share.GrantID, share.OwnerPrincipalID, share.RecipientPrincipalID, share.EntryID,
		share.Revision, share.ProjectID, share.WorkspaceID, share.AgentID, share.IntentionID, share.ParentRunID, share.EntrySHA256, share.FrozenView,
		share.GrantedAt.Format(time.RFC3339Nano), share.ExpiresAt.Format(time.RFC3339Nano))
	if err != nil {
		_ = tx.Rollback()
		if auditErr := s.RecordDecision(ctx, actor, "share.grant", share.GrantID, share.EntryID, share.Revision, "denied", now); auditErr != nil {
			return DiaryShare{}, ErrDiaryConsentAuditUnavailable
		}
		return DiaryShare{}, errors.New("freewill: diary share request was rejected")
	}
	if err := insertDiaryConsentAudit(ctx, tx, DiaryConsentAudit{OccurredAt: now.UTC(), PrincipalID: actor, Action: "share.grant", Decision: "allowed", GrantID: share.GrantID, EntryID: share.EntryID, Revision: share.Revision}); err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	if err := tx.Commit(); err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	return share, nil
}

func (s *DiaryConsentStore) Read(ctx context.Context, actor, grantID string, now time.Time) (DiaryShare, error) {
	if s == nil || s.db == nil {
		return DiaryShare{}, errors.New("freewill: diary consent store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	defer tx.Rollback()
	share, readErr := scanDiaryShare(tx.QueryRowContext(ctx, `SELECT grant_id, owner_principal_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, frozen_view, granted_at, expires_at, revoked_at
		FROM freewill_diary_shares WHERE grant_id = ?`, grantID))
	decision := "denied"
	if readErr == nil && validSyntheticPrincipal(actor) && actor == share.RecipientPrincipalID && share.RevokedAt == nil && now.Before(share.ExpiresAt) {
		digest := sha256.Sum256(share.FrozenView)
		if hex.EncodeToString(digest[:]) == share.EntrySHA256 && diaryShareSnapshotMatches(share) {
			decision = "allowed"
		} else {
			readErr = errors.New("freewill: diary share snapshot is invalid")
		}
	} else if readErr == nil {
		readErr = errors.New("freewill: diary share is unavailable")
	}
	auditEntry, auditRevision := "", 0
	if readErr == nil || share.GrantID != "" {
		auditEntry, auditRevision = share.EntryID, share.Revision
	}
	if err := insertDiaryConsentAudit(ctx, tx, DiaryConsentAudit{OccurredAt: now.UTC(), PrincipalID: safeDiaryPrincipal(actor), Action: "share.read", Decision: decision, GrantID: grantID, EntryID: auditEntry, Revision: auditRevision}); err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	if err := tx.Commit(); err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	if readErr != nil {
		return DiaryShare{}, readErr
	}
	return share, nil
}

func (s *DiaryConsentStore) Revoke(ctx context.Context, actor, grantID string, now time.Time) (DiaryShare, error) {
	if s == nil || s.db == nil {
		return DiaryShare{}, errors.New("freewill: diary consent store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	defer tx.Rollback()
	share, readErr := scanDiaryShare(tx.QueryRowContext(ctx, `SELECT grant_id, owner_principal_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, frozen_view, granted_at, expires_at, revoked_at
		FROM freewill_diary_shares WHERE grant_id = ?`, grantID))
	decision := "denied"
	if readErr == nil && validSyntheticPrincipal(actor) && actor == share.OwnerPrincipalID && share.RevokedAt == nil {
		if _, err := tx.ExecContext(ctx, `UPDATE freewill_diary_shares SET revoked_at = ? WHERE grant_id = ? AND revoked_at IS NULL`, now.UTC().Format(time.RFC3339Nano), grantID); err != nil {
			readErr = err
		} else {
			decision = "allowed"
			at := now.UTC()
			share.RevokedAt = &at
			rows, relErr := tx.QueryContext(ctx, `SELECT relationship_id FROM relationship_grants WHERE source_grant_id=? AND withdrawn_at IS NULL`, grantID)
			if relErr != nil {
				return DiaryShare{}, relErr
			} else {
				var relationshipIDs []string
				for rows.Next() {
					var relationshipID string
					if relErr = rows.Scan(&relationshipID); relErr != nil {
						break
					}
					relationshipIDs = append(relationshipIDs, relationshipID)
				}
				if relErr == nil {
					relErr = rows.Err()
				}
				rows.Close()
				for _, relationshipID := range relationshipIDs {
					if relErr = (&RelationshipConsentStore{db: s.db}).invalidateSourceTx(ctx, tx, relationshipID, now); relErr != nil {
						break
					}
				}
				if relErr != nil {
					return DiaryShare{}, relErr
				}
			}
		}
	} else if readErr == nil {
		readErr = errors.New("freewill: diary share cannot be revoked by this principal")
	}
	if err := insertDiaryConsentAudit(ctx, tx, DiaryConsentAudit{OccurredAt: now.UTC(), PrincipalID: safeDiaryPrincipal(actor), Action: "share.revoke", Decision: decision, GrantID: grantID, EntryID: share.EntryID, Revision: share.Revision}); err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	if err := tx.Commit(); err != nil {
		return DiaryShare{}, ErrDiaryConsentAuditUnavailable
	}
	if decision == "allowed" {
		if purgeErr := (&RelationshipConsentStore{db: s.db}).retryPendingPurges(ctx); purgeErr != nil {
			share.RelationshipCleanupPending = true
		}
	}
	if readErr != nil {
		return DiaryShare{}, readErr
	}
	return share, nil
}

// CreateReply appends recipient text against one exact active frozen grant.
// The request ID is a one-shot idempotency key: replay is rejected, never
// converted into a second write or an implicit successful retry.
func (s *DiaryConsentStore) CreateReply(ctx context.Context, actor string, expected DiaryShare, requestID, text string, now time.Time) (DiaryShareReply, error) {
	if s == nil || s.db == nil {
		return DiaryShareReply{}, errors.New("freewill: diary consent store is unavailable")
	}
	denied := func(tx *sql.Tx, share DiaryShare) (DiaryShareReply, error) {
		audit := DiaryConsentAudit{OccurredAt: now.UTC(), PrincipalID: actor, Action: "share.reply", Decision: "denied", GrantID: expected.GrantID, EntryID: share.EntryID, Revision: share.Revision}
		var err error
		if tx == nil {
			err = s.RecordDecision(ctx, actor, audit.Action, audit.GrantID, audit.EntryID, audit.Revision, audit.Decision, now)
		} else {
			err = insertDiaryConsentAudit(ctx, tx, audit)
			if err == nil {
				err = tx.Commit()
			}
		}
		if err != nil {
			return DiaryShareReply{}, ErrDiaryConsentAuditUnavailable
		}
		return DiaryShareReply{}, errors.New("freewill: diary share reply was rejected")
	}
	if !validSyntheticPrincipal(actor) || strings.TrimSpace(requestID) == "" || requestID != strings.TrimSpace(requestID) || len(requestID) > 128 ||
		strings.TrimSpace(text) == "" || len([]byte(text)) > DiaryShareReplyMaxBytes || !utf8.ValidString(text) {
		return denied(nil, DiaryShare{})
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DiaryShareReply{}, ErrDiaryConsentAuditUnavailable
	}
	defer tx.Rollback()
	share, err := scanDiaryShare(tx.QueryRowContext(ctx, `SELECT grant_id, owner_principal_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, frozen_view, granted_at, expires_at, revoked_at FROM freewill_diary_shares WHERE grant_id = ?`, expected.GrantID))
	if err != nil {
		return denied(tx, DiaryShare{})
	}
	match := actor == share.RecipientPrincipalID && share.RevokedAt == nil && now.Before(share.ExpiresAt) &&
		expected.GrantID == share.GrantID && expected.EntryID == share.EntryID && expected.Revision == share.Revision &&
		expected.EntrySHA256 == share.EntrySHA256 && expected.ProjectID == share.ProjectID && expected.WorkspaceID == share.WorkspaceID &&
		expected.AgentID == share.AgentID && expected.IntentionID == share.IntentionID && expected.ParentRunID == share.ParentRunID
	digest := sha256.Sum256(share.FrozenView)
	match = match && hex.EncodeToString(digest[:]) == share.EntrySHA256 && diaryShareSnapshotMatches(share)
	if !match {
		return denied(tx, share)
	}
	replyID, err := newDiaryShareID()
	if err != nil {
		return denied(tx, share)
	}
	createdAt := now.UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO freewill_diary_share_replies
		(reply_id, request_id, grant_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, reply_text, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, replyID, requestID, share.GrantID, actor, share.EntryID, share.Revision,
		share.ProjectID, share.WorkspaceID, share.AgentID, share.IntentionID, share.ParentRunID, share.EntrySHA256, text, createdAt.Format(time.RFC3339Nano))
	if err != nil {
		return denied(tx, share)
	}
	if err := insertDiaryConsentAudit(ctx, tx, DiaryConsentAudit{OccurredAt: createdAt, PrincipalID: actor, Action: "share.reply", Decision: "allowed", GrantID: share.GrantID, EntryID: share.EntryID, Revision: share.Revision}); err != nil {
		return DiaryShareReply{}, ErrDiaryConsentAuditUnavailable
	}
	if err := tx.Commit(); err != nil {
		return DiaryShareReply{}, ErrDiaryConsentAuditUnavailable
	}
	return DiaryShareReply{ReplyID: replyID, RequestID: requestID, GrantID: share.GrantID, RecipientPrincipalID: actor, EntryID: share.EntryID,
		Revision: share.Revision, ProjectID: share.ProjectID, WorkspaceID: share.WorkspaceID, AgentID: share.AgentID, IntentionID: share.IntentionID,
		ParentRunID: share.ParentRunID, EntrySHA256: share.EntrySHA256, Text: text, CreatedAt: createdAt}, nil
}

// ReadReplies returns durable history only to the grant owner. Expiry and
// revocation stop recipient access but do not silently erase retained replies.
// VerifyActiveRelationshipSource requires an active, unexpired frozen share and
// an immutable reply whose recipient and full stored scope match that grant.
func (s *DiaryConsentStore) VerifyActiveRelationshipSource(ctx context.Context, actor, grantID, replyID, recipient string, now time.Time) (DiaryShareReply, error) {
	if s == nil || s.db == nil {
		return DiaryShareReply{}, ErrDiaryRelationshipSourceUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return DiaryShareReply{}, err
	}
	defer tx.Rollback()
	reply, err := verifyActiveRelationshipSourceTx(ctx, tx, actor, grantID, replyID, recipient, now)
	if err != nil {
		return DiaryShareReply{}, err
	}
	if err = tx.Commit(); err != nil {
		return DiaryShareReply{}, err
	}
	return reply, nil
}

func verifyActiveRelationshipSourceTx(ctx context.Context, tx *sql.Tx, actor, grantID, replyID, recipient string, now time.Time) (DiaryShareReply, error) {
	share, err := scanDiaryShare(tx.QueryRowContext(ctx, `SELECT grant_id, owner_principal_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, frozen_view, granted_at, expires_at, revoked_at FROM freewill_diary_shares WHERE grant_id = ?`, grantID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DiaryShareReply{}, ErrDiaryRelationshipSourceUnavailable
		}
		return DiaryShareReply{}, err
	}
	if !validSyntheticPrincipal(actor) || actor != share.OwnerPrincipalID || recipient != share.RecipientPrincipalID || share.RevokedAt != nil || !now.Before(share.ExpiresAt) || !diaryShareSnapshotMatches(share) {
		return DiaryShareReply{}, ErrDiaryRelationshipSourceUnavailable
	}
	var reply DiaryShareReply
	var created string
	err = tx.QueryRowContext(ctx, `SELECT reply_id, request_id, grant_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, reply_text, created_at FROM freewill_diary_share_replies WHERE grant_id = ? AND reply_id = ?`, grantID, replyID).Scan(
		&reply.ReplyID, &reply.RequestID, &reply.GrantID, &reply.RecipientPrincipalID, &reply.EntryID, &reply.Revision, &reply.ProjectID, &reply.WorkspaceID, &reply.AgentID, &reply.IntentionID, &reply.ParentRunID, &reply.EntrySHA256, &reply.Text, &created)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return DiaryShareReply{}, ErrDiaryRelationshipSourceUnavailable
		}
		return DiaryShareReply{}, err
	}
	reply.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return DiaryShareReply{}, err
	}
	if reply.GrantID != share.GrantID || reply.RecipientPrincipalID != share.RecipientPrincipalID || reply.EntryID != share.EntryID || reply.Revision != share.Revision || reply.EntrySHA256 != share.EntrySHA256 || reply.ProjectID != share.ProjectID || reply.WorkspaceID != share.WorkspaceID || reply.AgentID != share.AgentID || reply.IntentionID != share.IntentionID || reply.ParentRunID != share.ParentRunID {
		return DiaryShareReply{}, ErrDiaryRelationshipSourceUnavailable
	}
	return reply, nil
}

func (s *DiaryConsentStore) ReadReplies(ctx context.Context, actor, grantID string, now time.Time) ([]DiaryShareReply, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("freewill: diary consent store is unavailable")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrDiaryConsentAuditUnavailable
	}
	defer tx.Rollback()
	share, readErr := scanDiaryShare(tx.QueryRowContext(ctx, `SELECT grant_id, owner_principal_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, frozen_view, granted_at, expires_at, revoked_at FROM freewill_diary_shares WHERE grant_id = ?`, grantID))
	allowed := readErr == nil && validSyntheticPrincipal(actor) && actor == share.OwnerPrincipalID
	decision := "denied"
	var replies []DiaryShareReply
	if allowed {
		rows, err := tx.QueryContext(ctx, `SELECT reply_id, request_id, grant_id, recipient_principal_id, entry_id, entry_revision, project_id, workspace_id, agent_id, intention_id, parent_run_id, entry_sha256, reply_text, created_at FROM freewill_diary_share_replies WHERE grant_id = ? ORDER BY created_at, reply_id`, grantID)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var item DiaryShareReply
			var created string
			if err := rows.Scan(&item.ReplyID, &item.RequestID, &item.GrantID, &item.RecipientPrincipalID, &item.EntryID, &item.Revision,
				&item.ProjectID, &item.WorkspaceID, &item.AgentID, &item.IntentionID, &item.ParentRunID, &item.EntrySHA256, &item.Text, &created); err != nil {
				rows.Close()
				return nil, err
			}
			item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
			if err != nil {
				rows.Close()
				return nil, err
			}
			if item.GrantID != share.GrantID || item.RecipientPrincipalID != share.RecipientPrincipalID || item.EntryID != share.EntryID || item.Revision != share.Revision ||
				item.EntrySHA256 != share.EntrySHA256 || item.ProjectID != share.ProjectID || item.WorkspaceID != share.WorkspaceID || item.AgentID != share.AgentID ||
				item.IntentionID != share.IntentionID || item.ParentRunID != share.ParentRunID {
				rows.Close()
				if err := insertDiaryConsentAudit(ctx, tx, DiaryConsentAudit{OccurredAt: now.UTC(), PrincipalID: actor, Action: "share.replies.read", Decision: "denied", GrantID: grantID, EntryID: share.EntryID, Revision: share.Revision}); err != nil {
					return nil, ErrDiaryConsentAuditUnavailable
				}
				if err := tx.Commit(); err != nil {
					return nil, ErrDiaryConsentAuditUnavailable
				}
				return nil, errors.New("freewill: diary share reply binding is invalid")
			}
			replies = append(replies, item)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
		decision = "allowed"
	} else if readErr == nil {
		readErr = errors.New("freewill: diary share replies are unavailable")
	}
	if err := insertDiaryConsentAudit(ctx, tx, DiaryConsentAudit{OccurredAt: now.UTC(), PrincipalID: safeDiaryPrincipal(actor), Action: "share.replies.read", Decision: decision,
		GrantID: grantID, EntryID: share.EntryID, Revision: share.Revision}); err != nil {
		return nil, ErrDiaryConsentAuditUnavailable
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrDiaryConsentAuditUnavailable
	}
	if readErr != nil {
		return nil, readErr
	}
	return replies, nil
}

// RecordDecision stores a metadata-only direct diary-access decision.
func (s *DiaryConsentStore) RecordDecision(ctx context.Context, actor, action, grantID, entryID string, revision int, decision string, now time.Time) error {
	if s == nil || s.db == nil {
		return errors.New("freewill: diary consent store is unavailable")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO freewill_diary_consent_audit (occurred_at, principal_id, action, decision, grant_id, entry_id, entry_revision) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		now.UTC().Format(time.RFC3339Nano), safeDiaryPrincipal(actor), action, decision, grantID, entryID, revision)
	return err
}

func insertDiaryConsentAudit(ctx context.Context, tx *sql.Tx, record DiaryConsentAudit) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO freewill_diary_consent_audit (occurred_at, principal_id, action, decision, grant_id, entry_id, entry_revision) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		record.OccurredAt.UTC().Format(time.RFC3339Nano), safeDiaryPrincipal(record.PrincipalID), record.Action, record.Decision, record.GrantID, record.EntryID, record.Revision)
	return err
}

func scanDiaryShare(row *sql.Row) (DiaryShare, error) {
	var share DiaryShare
	var grantedAt, expiresAt string
	var revokedAt sql.NullString
	if err := row.Scan(&share.GrantID, &share.OwnerPrincipalID, &share.RecipientPrincipalID, &share.EntryID, &share.Revision,
		&share.ProjectID, &share.WorkspaceID, &share.AgentID, &share.IntentionID, &share.ParentRunID, &share.EntrySHA256, &share.FrozenView,
		&grantedAt, &expiresAt, &revokedAt); err != nil {
		return DiaryShare{}, err
	}
	var err error
	if share.GrantedAt, err = time.Parse(time.RFC3339Nano, grantedAt); err != nil {
		return DiaryShare{}, err
	}
	if share.ExpiresAt, err = time.Parse(time.RFC3339Nano, expiresAt); err != nil {
		return DiaryShare{}, err
	}
	if revokedAt.Valid {
		at, err := time.Parse(time.RFC3339Nano, revokedAt.String)
		if err != nil {
			return DiaryShare{}, err
		}
		share.RevokedAt = &at
	}
	return share, nil
}

func newDiaryShareID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func validSyntheticPrincipal(value string) bool {
	return strings.HasPrefix(value, "synthetic-") && len(value) <= 128 && value == strings.TrimSpace(value)
}

func safeDiaryPrincipal(value string) string {
	if !validSyntheticPrincipal(value) {
		return "unknown"
	}
	return value
}

func diaryShareSnapshotMatches(share DiaryShare) bool {
	var snapshot struct {
		EntryID     string `json:"entry_id"`
		Revision    int    `json:"revision"`
		ProjectID   string `json:"project_id"`
		WorkspaceID string `json:"workspace_id"`
		AgentID     string `json:"agent_id"`
		IntentionID string `json:"intention_id"`
		ParentRunID string `json:"parent_run_id"`
	}
	if json.Unmarshal(share.FrozenView, &snapshot) != nil {
		return false
	}
	return snapshot.EntryID == share.EntryID && snapshot.Revision == share.Revision && snapshot.ProjectID == share.ProjectID &&
		snapshot.WorkspaceID == share.WorkspaceID && snapshot.AgentID == share.AgentID && snapshot.IntentionID == share.IntentionID && snapshot.ParentRunID == share.ParentRunID
}
