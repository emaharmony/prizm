package freewill

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	_ "github.com/emaharmony/prizm/internal/sqlite"
)

const RelationshipHypothesisMaxBytes = 2000
const RelationshipAssessmentMaxBytes = 1000

// RelationshipAssessment is a tentative, source-bound local-fake inference.
// Opinion and affective appraisal are persisted as separate revisions.
type RelationshipAssessment struct {
	AssessmentID     string    `json:"assessment_id"`
	RelationshipID   string    `json:"relationship_id"`
	Kind             string    `json:"kind"`
	Status           string    `json:"status"`
	Hypothesis       string    `json:"hypothesis"`
	Uncertainty      string    `json:"uncertainty"`
	SourceGrantID    string    `json:"source_grant_id"`
	SourceReplyID    string    `json:"source_reply_id"`
	SourceDigest     string    `json:"source_digest"`
	Revision         int       `json:"revision"`
	PreviousRecordID string    `json:"previous_record_id,omitempty"`
	Provider         string    `json:"provider"`
	Model            string    `json:"model"`
	CreatedAt        time.Time `json:"created_at"`
}

// RelationshipAssessmentProvider has no tools or authority-bearing methods.
// Implementations receive a bounded, untrusted source reply as inert data.
type RelationshipAssessmentProvider interface {
	AssessRelationshipReply(context.Context, string) ([]RelationshipAssessmentOutput, error)
}

type RelationshipAssessmentOutput struct {
	Kind        string `json:"kind"`
	Hypothesis  string `json:"hypothesis"`
	Uncertainty string `json:"uncertainty"`
}

// RelationshipChallenge is participant-authored, unverified evidence attached
// to an exact opinion revision. Its body is deleted with the derived assessment.
type RelationshipChallenge struct {
	ChallengeID      string    `json:"challenge_id"`
	AssessmentID     string    `json:"assessment_id"`
	AuthorID         string    `json:"author_id"`
	ExpectedRevision int       `json:"expected_revision"`
	Rationale        string    `json:"rationale"`
	Evidence         string    `json:"evidence,omitempty"`
	Verified         bool      `json:"verified"`
	CreatedAt        time.Time `json:"created_at"`
}

var ErrRelationshipUnavailable = errors.New("freewill: relationship is unavailable")
var ErrRelationshipPurgePending = errors.New("freewill: relationship content is logically deleted; physical cleanup is pending")

// RelationshipObservation is an explicitly owner-authored hypothesis. The
// persisted body and provenance are deleted when either participant withdraws.
type RelationshipObservation struct {
	RelationshipID string
	AuthorID       string
	Hypothesis     string
	SourceRef      string
	CreatedAt      time.Time
	Label          string
}

// RelationshipConsentStore owns scoped mutual consent and relationship
// observation retention. Audits contain metadata only.
type RelationshipConsentStore struct {
	db    *sql.DB
	diary *DiaryConsentStore
}

// OpenRelationshipConsentStoreForDiary reuses the E36 consent database so
// source checks, relationship access, and E36 revocation share one transaction.
func OpenRelationshipConsentStoreForDiary(diary *DiaryConsentStore) (*RelationshipConsentStore, error) {
	if diary == nil || diary.db == nil {
		return nil, errors.New("freewill: diary consent database is unavailable")
	}
	if err := initRelationshipSchema(diary.db); err != nil {
		return nil, err
	}
	store := &RelationshipConsentStore{db: diary.db, diary: diary}
	if err := store.retryPendingPurges(context.Background()); err != nil {
		return nil, err
	}
	if err := store.ExpireUnavailableSources(context.Background(), time.Now().UTC()); err != nil {
		return nil, err
	}
	return store, nil
}

func initRelationshipSchema(db *sql.DB) error {
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS relationship_grants (
 relationship_id TEXT PRIMARY KEY, principal_a TEXT NOT NULL, principal_b TEXT NOT NULL,
 interaction_scope TEXT NOT NULL, created_at TEXT NOT NULL, consent_a INTEGER NOT NULL,
 consent_b INTEGER NOT NULL, withdrawn_at TEXT, withdrawal_actor TEXT, purge_pending INTEGER NOT NULL DEFAULT 0,
 source_grant_id TEXT NOT NULL DEFAULT '', source_reply_id TEXT NOT NULL DEFAULT '', source_digest TEXT NOT NULL DEFAULT '',
 CHECK(principal_a <> principal_b), CHECK(consent_a IN (0,1)), CHECK(consent_b IN (0,1))
);
CREATE TABLE IF NOT EXISTS relationship_observations (
 relationship_id TEXT PRIMARY KEY REFERENCES relationship_grants(relationship_id),
 author_id TEXT NOT NULL, hypothesis TEXT NOT NULL, source_ref TEXT NOT NULL,
 created_at TEXT NOT NULL, label TEXT NOT NULL CHECK(label='hypothesis')
);
CREATE TABLE IF NOT EXISTS relationship_assessment_runs (
 relationship_id TEXT PRIMARY KEY REFERENCES relationship_grants(relationship_id),
 status TEXT NOT NULL CHECK(status IN ('reserved','completed','source-invalidated')),
 reserved_at TEXT NOT NULL, completed_at TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS relationship_assessments (
 assessment_id TEXT PRIMARY KEY, relationship_id TEXT NOT NULL REFERENCES relationship_grants(relationship_id),
 kind TEXT NOT NULL CHECK(kind IN ('opinion','affective_appraisal')), status TEXT NOT NULL CHECK(status='tentative'),
 hypothesis TEXT NOT NULL, uncertainty TEXT NOT NULL, source_grant_id TEXT NOT NULL,
 source_reply_id TEXT NOT NULL, source_digest TEXT NOT NULL, revision INTEGER NOT NULL CHECK(revision > 0),
 previous_record_id TEXT NOT NULL DEFAULT '', provider TEXT NOT NULL, model TEXT NOT NULL, created_at TEXT NOT NULL,
 UNIQUE(relationship_id,kind,revision)
);
CREATE TABLE IF NOT EXISTS relationship_assessment_challenges (
 challenge_id TEXT PRIMARY KEY, relationship_id TEXT NOT NULL REFERENCES relationship_grants(relationship_id),
 assessment_id TEXT NOT NULL, author_id TEXT NOT NULL, expected_revision INTEGER NOT NULL,
 rationale TEXT NOT NULL, evidence TEXT NOT NULL DEFAULT '', verified INTEGER NOT NULL CHECK(verified=0), created_at TEXT NOT NULL,
 UNIQUE(relationship_id,assessment_id,expected_revision)
);
CREATE TABLE IF NOT EXISTS relationship_consent_audit (
 audit_id INTEGER PRIMARY KEY AUTOINCREMENT, relationship_id TEXT NOT NULL,
 principal_id TEXT NOT NULL, action TEXT NOT NULL, occurred_at TEXT NOT NULL
);`)
	return err
}

func (s *RelationshipConsentStore) Close() error {
	return nil
}

// AssessAfterMutualConsent reserves a durable one-shot call budget before
// invoking the provider. A crash after reservation deliberately does not retry.
// Outputs are accepted only as the two typed, bounded hypotheses below and are
// persisted only after revalidating current consent and the frozen E36 source.
func (s *RelationshipConsentStore) AssessAfterMutualConsent(ctx context.Context, id string, provider RelationshipAssessmentProvider, now time.Time) error {
	if s == nil || s.db == nil || s.diary == nil || provider == nil {
		return ErrRelationshipUnavailable
	}
	assessmentNow := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var a, b, scope, grantID, replyID, digest string
	var ca, cb int
	var withdrawn sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,interaction_scope,consent_a,consent_b,withdrawn_at,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &scope, &ca, &cb, &withdrawn, &grantID, &replyID, &digest)
	if err != nil || withdrawn.Valid {
		return ErrRelationshipUnavailable
	}
	if ca != 1 || cb != 1 {
		return tx.Commit()
	}
	parts := strings.Split(scope, "|")
	if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
		return ErrRelationshipUnavailable
	}
	reply, err := verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], assessmentNow)
	if errors.Is(err, ErrDiaryRelationshipSourceUnavailable) {
		if err = s.invalidateSourceTx(ctx, tx, id, assessmentNow); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if s.finishPurge(ctx, id) != nil {
			return ErrRelationshipPurgePending
		}
		return ErrRelationshipUnavailable
	}
	if err != nil || reply.EntrySHA256 != digest {
		return ErrRelationshipUnavailable
	}
	res, err := tx.ExecContext(ctx, `INSERT INTO relationship_assessment_runs(relationship_id,status,reserved_at) VALUES(?,'reserved',?) ON CONFLICT(relationship_id) DO NOTHING`, id, assessmentNow.Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return nil
	}
	if err = tx.Commit(); err != nil {
		return err
	}

	// The fake/local provider receives only this bounded field as inert data.
	body := reply.Text
	if len(body) > RelationshipHypothesisMaxBytes {
		body = body[:RelationshipHypothesisMaxBytes]
		for !utf8.ValidString(body) {
			body = body[:len(body)-1]
		}
	}
	outputs, err := provider.AssessRelationshipReply(ctx, body)
	if err != nil {
		return err
	}
	validated, err := validateRelationshipAssessmentOutputs(outputs)
	if err != nil {
		return err
	}
	persistNow := time.Now().UTC()

	tx, err = s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,interaction_scope,consent_a,consent_b,withdrawn_at,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &scope, &ca, &cb, &withdrawn, &grantID, &replyID, &digest)
	if err != nil || withdrawn.Valid || ca != 1 || cb != 1 {
		return ErrRelationshipUnavailable
	}
	parts = strings.Split(scope, "|")
	if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
		return ErrRelationshipUnavailable
	}
	reply, err = verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], persistNow)
	if errors.Is(err, ErrDiaryRelationshipSourceUnavailable) {
		if err = s.invalidateSourceTx(ctx, tx, id, persistNow); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if s.finishPurge(ctx, id) != nil {
			return ErrRelationshipPurgePending
		}
		return ErrRelationshipUnavailable
	}
	if err != nil || reply.EntrySHA256 != digest {
		return ErrRelationshipUnavailable
	}
	for _, out := range validated {
		var revision int
		var previous string
		_ = tx.QueryRowContext(ctx, `SELECT revision,assessment_id FROM relationship_assessments WHERE relationship_id=? AND kind=? ORDER BY revision DESC LIMIT 1`, id, out.Kind).Scan(&revision, &previous)
		revision++
		assessmentID, idErr := newRelationshipID()
		if idErr != nil {
			return idErr
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO relationship_assessments(assessment_id,relationship_id,kind,status,hypothesis,uncertainty,source_grant_id,source_reply_id,source_digest,revision,previous_record_id,provider,model,created_at) VALUES(?,?,?,'tentative',?,?,?,?,?,?,?,?,?,?)`, assessmentID, id, out.Kind, out.Hypothesis, out.Uncertainty, grantID, replyID, digest, revision, previous, "local-fake", "relationship-reply-assessor", persistNow.Format(time.RFC3339Nano)); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relationship_assessment_runs SET status='completed',completed_at=? WHERE relationship_id=? AND status='reserved'`, persistNow.Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return tx.Commit()
}

func validateRelationshipAssessmentOutputs(outputs []RelationshipAssessmentOutput) ([]RelationshipAssessmentOutput, error) {
	if len(outputs) != 2 {
		return nil, ErrRelationshipUnavailable
	}
	seen := map[string]bool{}
	for i := range outputs {
		out := &outputs[i]
		out.Hypothesis = strings.TrimSpace(out.Hypothesis)
		out.Uncertainty = strings.TrimSpace(out.Uncertainty)
		if (out.Kind != "opinion" && out.Kind != "affective_appraisal") || seen[out.Kind] || out.Hypothesis == "" || out.Uncertainty == "" || len(out.Hypothesis) > RelationshipAssessmentMaxBytes || len(out.Uncertainty) > 300 || !utf8.ValidString(out.Hypothesis) || !utf8.ValidString(out.Uncertainty) {
			return nil, ErrRelationshipUnavailable
		}
		seen[out.Kind] = true
	}
	if !seen["opinion"] || !seen["affective_appraisal"] {
		return nil, ErrRelationshipUnavailable
	}
	return outputs, nil
}

func (s *RelationshipConsentStore) ReadAssessments(ctx context.Context, actor, id string, now time.Time) ([]RelationshipAssessment, error) {
	if s == nil || s.db == nil || s.diary == nil {
		return nil, ErrRelationshipUnavailable
	}
	if err := s.retryPendingPurges(ctx); err != nil {
		return nil, ErrRelationshipPurgePending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var a, b, scope, grantID, replyID, digest string
	var ca, cb int
	var withdrawn sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,interaction_scope,consent_a,consent_b,withdrawn_at,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &scope, &ca, &cb, &withdrawn, &grantID, &replyID, &digest)
	if err != nil || withdrawn.Valid || ca != 1 || cb != 1 || (actor != a && actor != b) {
		return nil, ErrRelationshipUnavailable
	}
	parts := strings.Split(scope, "|")
	if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
		return nil, ErrRelationshipUnavailable
	}
	reply, err := verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], now)
	if errors.Is(err, ErrDiaryRelationshipSourceUnavailable) {
		if err = s.invalidateSourceTx(ctx, tx, id, now); err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		if s.finishPurge(ctx, id) != nil {
			return nil, ErrRelationshipPurgePending
		}
		return nil, ErrRelationshipUnavailable
	}
	if err != nil || reply.EntrySHA256 != digest {
		return nil, ErrRelationshipUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT assessment_id,relationship_id,kind,status,hypothesis,uncertainty,source_grant_id,source_reply_id,source_digest,revision,previous_record_id,provider,model,created_at FROM relationship_assessments WHERE relationship_id=? ORDER BY kind,revision`, id)
	if err != nil {
		return nil, err
	}
	var result []RelationshipAssessment
	for rows.Next() {
		var item RelationshipAssessment
		var created string
		if err = rows.Scan(&item.AssessmentID, &item.RelationshipID, &item.Kind, &item.Status, &item.Hypothesis, &item.Uncertainty, &item.SourceGrantID, &item.SourceReplyID, &item.SourceDigest, &item.Revision, &item.PreviousRecordID, &item.Provider, &item.Model, &created); err != nil {
			return nil, err
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, ErrRelationshipUnavailable
		}
		result = append(result, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err = rows.Close(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// ChallengeOpinion appends an attributed unverified challenge and a cautious
// host-authored opinion revision. It does not call a model or affect appraisal.
func (s *RelationshipConsentStore) ChallengeOpinion(ctx context.Context, actor, id, assessmentID, challengeID string, expectedRevision int, rationale, evidence string, now time.Time) (RelationshipAssessment, error) {
	var zero RelationshipAssessment
	rationale, evidence = strings.TrimSpace(rationale), strings.TrimSpace(evidence)
	if s == nil || s.db == nil || s.diary == nil || actor == "" || id == "" || assessmentID == "" || challengeID == "" || expectedRevision < 1 || rationale == "" || len(rationale) > 1000 || len(evidence) > 1000 || !utf8.ValidString(rationale) || !utf8.ValidString(evidence) {
		return zero, ErrRelationshipUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer tx.Rollback()
	var a, b, scope, grantID, replyID, digest string
	var ca, cb int
	var withdrawn sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,interaction_scope,consent_a,consent_b,withdrawn_at,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &scope, &ca, &cb, &withdrawn, &grantID, &replyID, &digest)
	if err != nil || withdrawn.Valid || ca != 1 || cb != 1 || (actor != a && actor != b) {
		return zero, ErrRelationshipUnavailable
	}
	parts := strings.Split(scope, "|")
	if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
		return zero, ErrRelationshipUnavailable
	}
	reply, err := verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], now.UTC())
	if errors.Is(err, ErrDiaryRelationshipSourceUnavailable) || (err == nil && reply.EntrySHA256 != digest) {
		if err = s.invalidateSourceTx(ctx, tx, id, now.UTC()); err != nil {
			return zero, err
		}
		if err = tx.Commit(); err != nil {
			return zero, err
		}
		if s.finishPurge(ctx, id) != nil {
			return zero, ErrRelationshipPurgePending
		}
		return zero, ErrRelationshipUnavailable
	}
	if err != nil {
		return zero, ErrRelationshipUnavailable
	}
	var kind, status, priorHyp, uncertainty, assessmentGrantID, assessmentReplyID, sourceDigest string
	var revision int
	err = tx.QueryRowContext(ctx, `SELECT kind,status,hypothesis,uncertainty,source_grant_id,source_reply_id,source_digest,revision FROM relationship_assessments WHERE assessment_id=? AND relationship_id=?`, assessmentID, id).Scan(&kind, &status, &priorHyp, &uncertainty, &assessmentGrantID, &assessmentReplyID, &sourceDigest, &revision)
	if err != nil || kind != "opinion" || status != "tentative" || revision != expectedRevision || assessmentGrantID != grantID || assessmentReplyID != replyID || sourceDigest != digest {
		return zero, ErrRelationshipUnavailable
	}
	var oldAssessmentID, oldAuthor, oldRationale, oldEvidence string
	var oldRevision int
	replayErr := tx.QueryRowContext(ctx, `SELECT assessment_id,author_id,expected_revision,rationale,evidence FROM relationship_assessment_challenges WHERE challenge_id=?`, challengeID).Scan(&oldAssessmentID, &oldAuthor, &oldRevision, &oldRationale, &oldEvidence)
	if replayErr == nil {
		if oldAssessmentID != assessmentID || oldAuthor != actor || oldRevision != expectedRevision || oldRationale != rationale || oldEvidence != evidence {
			return zero, ErrRelationshipUnavailable
		}
		var item RelationshipAssessment
		var createdText string
		err = tx.QueryRowContext(ctx, `SELECT assessment_id,relationship_id,kind,status,hypothesis,uncertainty,source_grant_id,source_reply_id,source_digest,revision,previous_record_id,provider,model,created_at FROM relationship_assessments WHERE relationship_id=? AND previous_record_id=? AND revision=? AND kind='opinion'`, id, assessmentID, expectedRevision+1).Scan(&item.AssessmentID, &item.RelationshipID, &item.Kind, &item.Status, &item.Hypothesis, &item.Uncertainty, &item.SourceGrantID, &item.SourceReplyID, &item.SourceDigest, &item.Revision, &item.PreviousRecordID, &item.Provider, &item.Model, &createdText)
		if err != nil {
			return zero, ErrRelationshipUnavailable
		}
		item.CreatedAt, err = time.Parse(time.RFC3339Nano, createdText)
		if err != nil {
			return zero, ErrRelationshipUnavailable
		}
		if err = tx.Commit(); err != nil {
			return zero, err
		}
		return item, nil
	}
	if !errors.Is(replayErr, sql.ErrNoRows) {
		return zero, ErrRelationshipUnavailable
	}
	created := now.UTC()
	createdText := created.Format(time.RFC3339Nano)
	if _, err = tx.ExecContext(ctx, `INSERT INTO relationship_assessment_challenges(challenge_id,relationship_id,assessment_id,author_id,expected_revision,rationale,evidence,verified,created_at) VALUES(?,?,?,?,?,?,?,0,?)`, challengeID, id, assessmentID, actor, expectedRevision, rationale, evidence, createdText); err != nil {
		return zero, ErrRelationshipUnavailable
	}
	newID, err := newRelationshipID()
	if err != nil {
		return zero, err
	}
	hypothesis := "Participant challenge recorded as unverified perspective: " + rationale + ". Original opinion remains tentative; this revision does not establish a corrected fact."
	if evidence != "" {
		hypothesis += " Cited evidence is unverified: " + evidence + "."
	}
	if len(hypothesis) > RelationshipAssessmentMaxBytes {
		return zero, ErrRelationshipUnavailable
	}
	uncertainty = "high: participant challenge is unverified; no semantic or factual validation performed"
	_, err = tx.ExecContext(ctx, `INSERT INTO relationship_assessments(assessment_id,relationship_id,kind,status,hypothesis,uncertainty,source_grant_id,source_reply_id,source_digest,revision,previous_record_id,provider,model,created_at) VALUES(?,?, 'opinion','tentative',?,?,?,?,?,?,?,'host-authored','participant-challenge',?)`, newID, id, hypothesis, uncertainty, grantID, replyID, digest, expectedRevision+1, assessmentID, createdText)
	if err != nil {
		return zero, err
	}
	if err = tx.Commit(); err != nil {
		return zero, err
	}
	return RelationshipAssessment{AssessmentID: newID, RelationshipID: id, Kind: "opinion", Status: "tentative", Hypothesis: hypothesis, Uncertainty: uncertainty, SourceGrantID: grantID, SourceReplyID: replyID, SourceDigest: digest, Revision: expectedRevision + 1, PreviousRecordID: assessmentID, Provider: "host-authored", Model: "participant-challenge", CreatedAt: created}, nil
}

func (s *RelationshipConsentStore) ReadChallenges(ctx context.Context, actor, id, assessmentID string, now time.Time) ([]RelationshipChallenge, error) {
	if s == nil || s.db == nil || s.diary == nil {
		return nil, ErrRelationshipUnavailable
	}
	if _, err := s.ReadAssessments(ctx, actor, id, now); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var a, b, scope, grantID, replyID, digest string
	var ca, cb int
	var withdrawn sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,interaction_scope,consent_a,consent_b,withdrawn_at,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &scope, &ca, &cb, &withdrawn, &grantID, &replyID, &digest); err != nil || withdrawn.Valid || ca != 1 || cb != 1 || (actor != a && actor != b) {
		return nil, ErrRelationshipUnavailable
	}
	parts := strings.Split(scope, "|")
	if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
		return nil, ErrRelationshipUnavailable
	}
	reply, sourceErr := verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], now.UTC())
	if errors.Is(sourceErr, ErrDiaryRelationshipSourceUnavailable) || (sourceErr == nil && reply.EntrySHA256 != digest) {
		if sourceErr = s.invalidateSourceTx(ctx, tx, id, now.UTC()); sourceErr != nil {
			return nil, sourceErr
		}
		if sourceErr = tx.Commit(); sourceErr != nil {
			return nil, sourceErr
		}
		if s.finishPurge(ctx, id) != nil {
			return nil, ErrRelationshipPurgePending
		}
		return nil, ErrRelationshipUnavailable
	}
	if sourceErr != nil {
		return nil, ErrRelationshipUnavailable
	}
	var exists int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM relationship_assessments WHERE relationship_id=? AND assessment_id=? AND kind='opinion'`, id, assessmentID).Scan(&exists); err != nil || exists == 0 {
		return nil, ErrRelationshipUnavailable
	}
	rows, err := tx.QueryContext(ctx, `SELECT challenge_id,assessment_id,author_id,expected_revision,rationale,evidence,verified,created_at FROM relationship_assessment_challenges WHERE relationship_id=? AND assessment_id=? ORDER BY created_at`, id, assessmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []RelationshipChallenge
	for rows.Next() {
		var c RelationshipChallenge
		var verified int
		var created string
		if err = rows.Scan(&c.ChallengeID, &c.AssessmentID, &c.AuthorID, &c.ExpectedRevision, &c.Rationale, &c.Evidence, &verified, &created); err != nil {
			return nil, err
		}
		c.Verified = verified == 1
		c.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

func (s *RelationshipConsentStore) ProposeFromReply(ctx context.Context, actor, other, grantID, replyID string, now time.Time) (string, error) {
	if s == nil || s.db == nil || s.diary == nil || actor == other || !validRelationshipPrincipal(actor) || !validRelationshipPrincipal(other) {
		return "", ErrRelationshipUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	reply, err := verifyActiveRelationshipSourceTx(ctx, tx, actor, grantID, replyID, other, now)
	if err != nil {
		return "", ErrRelationshipUnavailable
	}
	id, err := newRelationshipID()
	if err != nil {
		return "", err
	}
	a, b := actor, other
	if b < a {
		a, b = b, a
	}
	scope := strings.Join([]string{grantID, replyID, actor, other, reply.EntrySHA256}, "|")
	ca, cb := 0, 0
	if actor == a {
		ca = 1
	} else {
		cb = 1
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO relationship_grants(relationship_id,principal_a,principal_b,interaction_scope,created_at,consent_a,consent_b,source_grant_id,source_reply_id,source_digest) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, a, b, scope, now.UTC().Format(time.RFC3339Nano), ca, cb, grantID, replyID, reply.EntrySHA256)
	if err != nil {
		return "", err
	}
	if err = relationshipAudit(ctx, tx, id, actor, "consent.granted", now); err != nil {
		return "", err
	}
	if err = tx.Commit(); err != nil {
		return "", err
	}
	return id, nil
}

// Consent records the second principal's explicit, principal-bound opt-in.
func (s *RelationshipConsentStore) Consent(ctx context.Context, actor, id string, now time.Time) error {
	if s == nil || s.db == nil {
		return ErrRelationshipUnavailable
	}
	if err := s.retryPendingPurges(ctx); err != nil {
		return ErrRelationshipPurgePending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var a, b string
	var withdrawn sql.NullString
	var scope, grantID, replyID, digest string
	if s.diary != nil {
		err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,withdrawn_at,interaction_scope,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &withdrawn, &scope, &grantID, &replyID, &digest)
		if err != nil || withdrawn.Valid || (actor != a && actor != b) {
			return ErrRelationshipUnavailable
		}
		parts := strings.Split(scope, "|")
		if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
			return ErrRelationshipUnavailable
		}
		_, err = verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], now)
		if errors.Is(err, ErrDiaryRelationshipSourceUnavailable) {
			if err = s.invalidateSourceTx(ctx, tx, id, now); err != nil {
				return err
			}
			if err = tx.Commit(); err != nil {
				return err
			}
			if purgeErr := s.finishPurge(ctx, id); purgeErr != nil {
				return ErrRelationshipPurgePending
			}
			return ErrRelationshipUnavailable
		}
		if err != nil {
			return ErrRelationshipUnavailable
		}
	} else if err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,withdrawn_at FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &withdrawn); err != nil || withdrawn.Valid || (actor != a && actor != b) {
		return ErrRelationshipUnavailable
	}
	col := "consent_a"
	if actor == b {
		col = "consent_b"
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relationship_grants SET `+col+`=1 WHERE relationship_id=? AND withdrawn_at IS NULL`, id); err != nil {
		return err
	}
	if err = relationshipAudit(ctx, tx, id, actor, "consent.granted", now); err != nil {
		return err
	}
	return tx.Commit()
}

// ExpireUnavailableSources is a bounded startup sweep. It purges bodies whose
// source grant has expired/revoked since the last server run; ordinary
// reads/writes also validate and purge in their own transaction.
func (s *RelationshipConsentStore) ExpireUnavailableSources(ctx context.Context, now time.Time) error {
	if s == nil || s.db == nil || s.diary == nil {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT relationship_id FROM relationship_grants WHERE withdrawn_at IS NULL`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		tx, txErr := s.db.BeginTx(ctx, nil)
		if txErr != nil {
			return txErr
		}
		var scope, grantID, replyID, digest string
		txErr = tx.QueryRowContext(ctx, `SELECT interaction_scope,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=? AND withdrawn_at IS NULL`, id).Scan(&scope, &grantID, &replyID, &digest)
		if errors.Is(txErr, sql.ErrNoRows) {
			_ = tx.Rollback()
			continue
		}
		if txErr != nil {
			_ = tx.Rollback()
			return txErr
		}
		parts := strings.Split(scope, "|")
		if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
			_ = tx.Rollback()
			continue
		}
		_, txErr = verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], now)
		if errors.Is(txErr, ErrDiaryRelationshipSourceUnavailable) {
			if txErr = s.invalidateSourceTx(ctx, tx, id, now); txErr != nil {
				_ = tx.Rollback()
				return txErr
			}
			if txErr = tx.Commit(); txErr != nil {
				return txErr
			}
			if purgeErr := s.finishPurge(ctx, id); purgeErr != nil {
				return ErrRelationshipPurgePending
			}
			continue
		}
		if txErr != nil {
			_ = tx.Rollback()
			return txErr
		}
		if txErr = tx.Commit(); txErr != nil {
			return txErr
		}
	}
	return nil
}

func (s *RelationshipConsentStore) WriteHypothesisFromReply(ctx context.Context, actor, id, text, grantID, replyID string, now time.Time) error {
	text = strings.TrimSpace(text)
	if s == nil || s.db == nil || s.diary == nil || text == "" || len(text) > RelationshipHypothesisMaxBytes || !utf8.ValidString(text) {
		return ErrRelationshipUnavailable
	}
	if err := s.retryPendingPurges(ctx); err != nil {
		return ErrRelationshipPurgePending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var a, b, scope, sourceGrant, sourceReply, digest string
	var ca, cb int
	var withdrawn sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,interaction_scope,consent_a,consent_b,withdrawn_at,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &scope, &ca, &cb, &withdrawn, &sourceGrant, &sourceReply, &digest)
	parts := strings.Split(scope, "|")
	if err != nil || withdrawn.Valid || ca != 1 || cb != 1 || (actor != a && actor != b) || len(parts) != 5 || actor != parts[2] || grantID != sourceGrant || replyID != sourceReply {
		return ErrRelationshipUnavailable
	}
	reply, err := verifyActiveRelationshipSourceTx(ctx, tx, actor, sourceGrant, sourceReply, strings.Split(scope, "|")[3], now)
	if errors.Is(err, ErrDiaryRelationshipSourceUnavailable) {
		if err = s.invalidateSourceTx(ctx, tx, id, now); err != nil {
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		if purgeErr := s.finishPurge(ctx, id); purgeErr != nil {
			return ErrRelationshipPurgePending
		}
		return ErrRelationshipUnavailable
	}
	if err != nil {
		return ErrRelationshipUnavailable
	}
	if reply.EntrySHA256 != digest {
		return ErrRelationshipUnavailable
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO relationship_observations(relationship_id,author_id,hypothesis,source_ref,created_at,label) VALUES(?,?,?,?,?,'hypothesis')`, id, actor, text, scope, now.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	if err = relationshipAudit(ctx, tx, id, actor, "observation.created", now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *RelationshipConsentStore) ReadHypothesisFromReply(ctx context.Context, actor, id string, now time.Time) (RelationshipObservation, error) {
	if s == nil || s.db == nil || s.diary == nil {
		return RelationshipObservation{}, ErrRelationshipUnavailable
	}
	if err := s.retryPendingPurges(ctx); err != nil {
		return RelationshipObservation{}, ErrRelationshipPurgePending
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RelationshipObservation{}, err
	}
	defer tx.Rollback()
	var a, b, scope, grantID, replyID, digest string
	var ca, cb int
	var withdrawn sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,interaction_scope,consent_a,consent_b,withdrawn_at,source_grant_id,source_reply_id,source_digest FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &scope, &ca, &cb, &withdrawn, &grantID, &replyID, &digest)
	if err != nil || withdrawn.Valid || ca != 1 || cb != 1 || (actor != a && actor != b) {
		return RelationshipObservation{}, ErrRelationshipUnavailable
	}
	parts := strings.Split(scope, "|")
	if len(parts) != 5 || parts[0] != grantID || parts[1] != replyID || parts[4] != digest {
		return RelationshipObservation{}, ErrRelationshipUnavailable
	}
	reply, err := verifyActiveRelationshipSourceTx(ctx, tx, parts[2], grantID, replyID, parts[3], now)
	if errors.Is(err, ErrDiaryRelationshipSourceUnavailable) {
		if err = s.invalidateSourceTx(ctx, tx, id, now); err != nil {
			return RelationshipObservation{}, err
		}
		if err = tx.Commit(); err != nil {
			return RelationshipObservation{}, err
		}
		if purgeErr := s.finishPurge(ctx, id); purgeErr != nil {
			return RelationshipObservation{}, ErrRelationshipPurgePending
		}
		return RelationshipObservation{}, ErrRelationshipUnavailable
	}
	if err != nil || reply.EntrySHA256 != digest {
		return RelationshipObservation{}, ErrRelationshipUnavailable
	}
	var o RelationshipObservation
	var created string
	o.RelationshipID = id
	err = tx.QueryRowContext(ctx, `SELECT author_id,hypothesis,source_ref,created_at,label FROM relationship_observations WHERE relationship_id=?`, id).Scan(&o.AuthorID, &o.Hypothesis, &o.SourceRef, &created, &o.Label)
	if err != nil {
		return RelationshipObservation{}, ErrRelationshipUnavailable
	}
	o.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	if err != nil {
		return RelationshipObservation{}, ErrRelationshipUnavailable
	}
	if err = tx.Commit(); err != nil {
		return RelationshipObservation{}, err
	}
	return o, nil
}

func (s *RelationshipConsentStore) invalidateSourceTx(ctx context.Context, tx *sql.Tx, id string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM relationship_observations WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relationship_assessments WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM relationship_assessment_challenges WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE relationship_assessment_runs SET status='source-invalidated' WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE relationship_grants SET withdrawn_at=?,withdrawal_actor='system-source-invalidated',consent_a=0,consent_b=0,purge_pending=1 WHERE relationship_id=? AND withdrawn_at IS NULL`, now.UTC().Format(time.RFC3339Nano), id); err != nil {
		return err
	}
	return relationshipAudit(ctx, tx, id, "system", "source.invalidated", now)
}

// Withdraw is final for this relationship ID. It removes the observation row
// transactionally, then checkpoints the WAL. Backups/filesystem snapshots may
// still retain old pages and are outside this local-store deletion guarantee.
func (s *RelationshipConsentStore) Withdraw(ctx context.Context, actor, id string, now time.Time) error {
	if s == nil || s.db == nil {
		return ErrRelationshipUnavailable
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var a, b string
	var withdrawn sql.NullString
	var withdrawalActor sql.NullString
	if err = tx.QueryRowContext(ctx, `SELECT principal_a,principal_b,withdrawn_at,withdrawal_actor FROM relationship_grants WHERE relationship_id=?`, id).Scan(&a, &b, &withdrawn, &withdrawalActor); err != nil || (actor != a && actor != b) {
		return ErrRelationshipUnavailable
	}
	if withdrawn.Valid {
		if !withdrawalActor.Valid || withdrawalActor.String != actor {
			return ErrRelationshipUnavailable
		}
		_ = tx.Rollback()
		return s.finishPurge(ctx, id)
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM relationship_observations WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM relationship_assessments WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM relationship_assessment_challenges WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relationship_assessment_runs SET status='source-invalidated' WHERE relationship_id=?`, id); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE relationship_grants SET withdrawn_at=?,withdrawal_actor=?,consent_a=0,consent_b=0,purge_pending=1 WHERE relationship_id=?`, now.UTC().Format(time.RFC3339Nano), actor, id); err != nil {
		return err
	}
	if err = relationshipAudit(ctx, tx, id, actor, "consent.withdrawn", now); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return s.finishPurge(ctx, id)
}

// InvalidateSource purges derived text when its frozen reply grant is revoked
// or expires. Audit records only the relationship ID and system action.
func (s *RelationshipConsentStore) finishPurge(ctx context.Context, id string) error {
	var busy, frames, checkpointed int
	if err := s.db.QueryRowContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`).Scan(&busy, &frames, &checkpointed); err != nil {
		return err
	}
	if busy != 0 {
		return ErrRelationshipPurgePending
	}
	_, err := s.db.ExecContext(ctx, `UPDATE relationship_grants SET purge_pending=0 WHERE relationship_id=?`, id)
	return err
}

func (s *RelationshipConsentStore) retryPendingPurges(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `SELECT relationship_id FROM relationship_grants WHERE purge_pending=1`)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	for _, id := range ids {
		if err = s.finishPurge(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func relationshipAudit(ctx context.Context, tx *sql.Tx, id, principal, action string, now time.Time) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO relationship_consent_audit(relationship_id,principal_id,action,occurred_at) VALUES(?,?,?,?)`, id, principal, action, now.UTC().Format(time.RFC3339Nano))
	return err
}
func validRelationshipPrincipal(v string) bool {
	return strings.HasPrefix(v, "synthetic-") && len(v) <= 128 && v == strings.TrimSpace(v)
}
func newRelationshipID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "rel_" + hex.EncodeToString(b[:]), nil
}
