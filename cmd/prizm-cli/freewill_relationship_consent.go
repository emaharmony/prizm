package main

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"github.com/emaharmony/prizm/internal/api"
	"github.com/emaharmony/prizm/internal/freewill"
)

type freewillRelationshipConsentController struct {
	store    *freewill.RelationshipConsentStore
	assessor freewill.RelationshipAssessmentProvider
}

// localFakeRelationshipAssessor models a bounded typed provider call for the
// loopback synthetic-principal slice. It has no tools and never treats source
// text as instructions or repeats it in outputs.
type localFakeRelationshipAssessor struct{}

func (localFakeRelationshipAssessor) AssessRelationshipReply(ctx context.Context, source string) ([]freewill.RelationshipAssessmentOutput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(source) == "" || len(source) > freewill.RelationshipHypothesisMaxBytes {
		return nil, errors.New("freewill: bounded relationship source required")
	}
	words := strings.FieldsFunc(strings.ToLower(source), func(r rune) bool { return !unicode.IsLetter(r) })
	positive, negative := false, false
	for _, word := range words {
		switch word {
		case "thank", "thanks", "appreciate", "appreciated", "glad", "happy", "care":
			positive = true
		case "sorry", "upset", "angry", "hurt", "disappointed", "disagree":
			negative = true
		}
	}
	opinion, appraisal := "No supported relationship sentiment cue appears in this reply; broader opinion remains unknown.", "No clear affect cue appears in this reply; no participant's internal state is known."
	if positive && negative {
		opinion, appraisal = "The reply contains mixed sentiment cues; relationship opinion remains uncertain.", "Mixed wording supports no clear affective appraisal; internal state remains unknown."
	} else if positive {
		opinion, appraisal = "Appreciation wording may signal positive regard, but one reply cannot establish a stable opinion.", "Appreciation wording may indicate a positive tone; internal affect remains unknown."
	} else if negative {
		opinion, appraisal = "Distress or disagreement wording may signal a concern; broader opinion remains unknown.", "Distress wording may indicate a negative tone; internal affect remains unknown."
	}
	return []freewill.RelationshipAssessmentOutput{
		{Kind: "opinion", Hypothesis: opinion, Uncertainty: "high: deterministic surface-word cue only; one bounded reply is weak evidence"},
		{Kind: "affective_appraisal", Hypothesis: appraisal, Uncertainty: "high: simulated lexical appraisal, not an observed emotion or internal state"},
	}, nil
}

func newFreewillRelationshipConsentController(source *freewillDiaryConsentController) (*freewillRelationshipConsentController, error) {
	if source == nil || source.store == nil {
		return nil, errors.New("freewill: relationship consent requires the E36 diary source store")
	}
	store, err := freewill.OpenRelationshipConsentStoreForDiary(source.store)
	if err != nil {
		return nil, err
	}
	return &freewillRelationshipConsentController{store: store, assessor: localFakeRelationshipAssessor{}}, nil
}

func (c *freewillRelationshipConsentController) ProposeRelationship(ctx context.Context, actor, other, grantID, replyID string, now time.Time) (string, error) {
	if c == nil || c.store == nil || actor == other {
		return "", freewill.ErrRelationshipUnavailable
	}
	return c.store.ProposeFromReply(ctx, actor, other, grantID, replyID, now)
}

func (c *freewillRelationshipConsentController) ConsentRelationship(ctx context.Context, actor, id string, now time.Time) error {
	if err := c.store.Consent(ctx, actor, id, now); err != nil {
		return err
	}
	return c.store.AssessAfterMutualConsent(ctx, id, c.assessor, now)
}

func (c *freewillRelationshipConsentController) ReadRelationshipAssessments(ctx context.Context, actor, id string, now time.Time) ([]freewill.RelationshipAssessment, error) {
	return c.store.ReadAssessments(ctx, actor, id, now)
}

func (c *freewillRelationshipConsentController) ChallengeRelationshipOpinion(ctx context.Context, actor, id, assessmentID, challengeID string, expectedRevision int, rationale, evidence string, now time.Time) (freewill.RelationshipAssessment, error) {
	return c.store.ChallengeOpinion(ctx, actor, id, assessmentID, challengeID, expectedRevision, rationale, evidence, now)
}

func (c *freewillRelationshipConsentController) ReadRelationshipChallenges(ctx context.Context, actor, id, assessmentID string, now time.Time) ([]freewill.RelationshipChallenge, error) {
	return c.store.ReadChallenges(ctx, actor, id, assessmentID, now)
}

func (c *freewillRelationshipConsentController) WriteRelationshipHypothesis(ctx context.Context, actor, id, text, grantID, replyID string, now time.Time) error {
	return c.store.WriteHypothesisFromReply(ctx, actor, id, text, grantID, replyID, now)
}

func (c *freewillRelationshipConsentController) ReadRelationshipHypothesis(ctx context.Context, actor, id string, now time.Time) (api.FreewillRelationshipObservation, error) {
	observation, err := c.store.ReadHypothesisFromReply(ctx, actor, id, now)
	if err != nil {
		return api.FreewillRelationshipObservation{}, err
	}
	parts := strings.Split(observation.SourceRef, "|")
	return api.FreewillRelationshipObservation{RelationshipID: observation.RelationshipID, AuthorID: observation.AuthorID, Hypothesis: observation.Hypothesis,
		SourceRef: parts[0] + "/" + parts[1], CreatedAt: observation.CreatedAt, Label: observation.Label}, nil
}

func (c *freewillRelationshipConsentController) WithdrawRelationship(ctx context.Context, actor, id string, now time.Time) error {
	return c.store.Withdraw(ctx, actor, id, now)
}
