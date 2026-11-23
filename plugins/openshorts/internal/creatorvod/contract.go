// Package creatorvod coordinates the isolated creator VOD workflow.
package creatorvod

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const Version = "clip-contract/v1"

const (
	maxCandidates = 100
	maxDelivered  = 10
	minClipScore  = 800
	minBatchScore = 850
)

var (
	ErrContract = errors.New("invalid creator VOD contract")
	ErrNotReady = errors.New("VOD readiness has not been established")
	identifier  = regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$")
)

// Ready is the input from the trusted readiness check. Stage 1 accepts
// synthetic observations through its test entry point.
type Ready struct {
	Version         string `json:"contract_version"`
	Profile         string `json:"profile_id"`
	SourceRef       string `json:"source_ref"`
	Name            string `json:"filename"`
	SourceHash      string `json:"source_hash"`
	ExecutionHash   string `json:"execution_hash"`
	Generation      string `json:"generation_id"`
	WorkflowVersion string `json:"workflow_version"`
	PolicyHash      string `json:"policy_hash"`
	Owner           string `json:"owner"`
	SizeBefore      int64  `json:"size_before"`
	SizeAfter       int64  `json:"size_after"`
	Stable          bool   `json:"stable"`
	ProbeOK         bool   `json:"probe_ok"`
	DurationMS      int64  `json:"duration_ms"`
}

func (r Ready) validate() error {
	if r.Version != Version || !validDigest(r.SourceHash) || !validDigest(r.PolicyHash) {
		return fmt.Errorf("%w: readiness version or digest", ErrContract)
	}
	for _, value := range []string{r.Profile, r.SourceRef, r.Generation, r.WorkflowVersion, r.Owner} {
		if !identifier.MatchString(value) {
			return fmt.Errorf("%w: invalid readiness identifier", ErrContract)
		}
	}
	name := strings.ToLower(r.Name)
	stem, finalized := strings.CutSuffix(name, ".ready.mp4")
	if !finalized || !identifier.MatchString(stem) || strings.HasSuffix(stem, "_done") ||
		!r.Stable || !r.ProbeOK || r.DurationMS <= 0 || r.SizeBefore <= 0 ||
		r.SizeBefore != r.SizeAfter || r.ExecutionHash != r.SourceHash {
		return ErrNotReady
	}
	return nil
}

func (r Ready) jobID() string {
	return "vod_" + digest([]byte(r.Profile+"\x00"+r.SourceHash+"\x00"+r.Generation))
}

type State string

const (
	Accepted          State = "accepted"
	Dispatching       State = "dispatching"
	Running           State = "running"
	Exporting         State = "exporting"
	Completed         State = "completed"
	NoQualifyingClips State = "no_qualifying_clips"
	NeedsReview       State = "needs_review"
	Failed            State = "failed"
	Cancelled         State = "cancelled"
)

const (
	submissionUnconfirmed = "SUBMISSION_UNCONFIRMED"
	qaUnverified          = "QA_UNVERIFIED"
	exportMismatch        = "EXPORT_VERIFICATION_FAILED"
	ownerFailed           = "OWNER_FAILED"
)

// Job is the current manifest projection. Each accepted revision gets its own
// immutable, hash-verified snapshot.
type Job struct {
	ID               string          `json:"job_id"`
	Revision         int64           `json:"revision"`
	Input            Ready           `json:"input"`
	State            State           `json:"state"`
	Reason           string          `json:"reason,omitempty"`
	CorrelationID    string          `json:"correlation_id"`
	LastEventID      string          `json:"last_event_id"`
	ExecutionKey     string          `json:"execution_key,omitempty"`
	DispatchRevision int64           `json:"dispatch_revision,omitempty"`
	CancelRequested  bool            `json:"cancel_requested,omitempty"`
	OwnerReceipt     *OwnerReceipt   `json:"owner_receipt,omitempty"`
	Selected         []Clip          `json:"selected,omitempty"`
	Exports          []ExportReceipt `json:"exports,omitempty"`
}

func (j Job) terminal() bool {
	switch j.State {
	case Completed, NoQualifyingClips, Failed, Cancelled:
		return true
	case NeedsReview:
		return j.Reason != submissionUnconfirmed
	default:
		return false
	}
}

type OwnerStatus string

const (
	OwnerRunning   OwnerStatus = "running"
	OwnerSucceeded OwnerStatus = "succeeded"
	OwnerFailed    OwnerStatus = "failed"
	OwnerCancelled OwnerStatus = "cancelled"
)

// OwnerReceipt is a full status snapshot. A newer sequence may supersede
// missed messages, but must echo the original dispatch revision and key.
type OwnerReceipt struct {
	Version          string      `json:"contract_version"`
	ExecutionKey     string      `json:"execution_key"`
	DispatchRevision int64       `json:"dispatch_revision"`
	RemoteID         string      `json:"remote_id"`
	Sequence         int64       `json:"sequence"`
	Status           OwnerStatus `json:"status"`
	AnalysisComplete bool        `json:"analysis_complete"`
	Clips            []Clip      `json:"clips"`
}

// Clip is a render receipt and independent QA verdict projected for release.
// Score is in hundredths so threshold comparisons are exact.
type Clip struct {
	ID              string          `json:"clip_id"`
	StartMS         int64           `json:"start_ms"`
	EndMS           int64           `json:"end_ms"`
	SourceHash      string          `json:"source_hash"`
	ArtifactRef     string          `json:"artifact_ref"`
	RenderHash      string          `json:"render_hash"`
	PolicyHash      string          `json:"policy_hash"`
	VerdictID       string          `json:"verdict_id"`
	QAHash          string          `json:"qa_hash"`
	ScoreHundredths *int            `json:"score_hundredths"`
	Gates           map[string]Gate `json:"gates"`
	QAEvidence      []string        `json:"qa_evidence_refs"`
}

type Gate string

const (
	Pass          Gate = "pass"
	Fail          Gate = "fail"
	Unknown       Gate = "unknown"
	NotApplicable Gate = "not_applicable"
)

// These gate names exercise release in the mock. Stage 2 must supply approved
// rubric and gate applicability from the pinned Pentakewl policy.
var mockCriticalGates = [...]string{
	"webcam", "gameplay", "audio", "boundaries", "caption_policy", "hook_policy",
}

func (r OwnerReceipt) validate(j Job) error {
	if r.Version != Version || r.ExecutionKey != j.ExecutionKey ||
		r.DispatchRevision != j.DispatchRevision || !identifier.MatchString(r.RemoteID) || r.Sequence < 1 {
		return fmt.Errorf("%w: invalid owner receipt identity", ErrContract)
	}
	switch r.Status {
	case OwnerRunning, OwnerFailed, OwnerCancelled:
		if len(r.Clips) != 0 || r.AnalysisComplete {
			return fmt.Errorf("%w: premature owner results", ErrContract)
		}
	case OwnerSucceeded:
		if len(r.Clips) > maxCandidates || (!r.AnalysisComplete && len(r.Clips) != 0) {
			return fmt.Errorf("%w: invalid analysis coverage or clip count", ErrContract)
		}
	default:
		return fmt.Errorf("%w: unrecognized owner status", ErrContract)
	}

	ids := make(map[string]bool, len(r.Clips))
	for _, clip := range r.Clips {
		if ids[clip.ID] {
			return fmt.Errorf("%w: duplicate clip identity", ErrContract)
		}
		ids[clip.ID] = true
		if err := clip.validate(j.Input); err != nil {
			return err
		}
	}
	return nil
}

func (c Clip) validate(input Ready) error {
	if !identifier.MatchString(c.ID) || !identifier.MatchString(c.ArtifactRef) ||
		!identifier.MatchString(c.VerdictID) || !validDigest(c.RenderHash) || !validDigest(c.QAHash) ||
		c.StartMS < 0 || c.EndMS <= c.StartMS || c.EndMS > input.DurationMS ||
		c.PolicyHash != input.PolicyHash || c.SourceHash != input.SourceHash {
		return fmt.Errorf("%w: invalid clip identity, time range or provenance", ErrContract)
	}
	if c.ScoreHundredths != nil && (*c.ScoreHundredths < 0 || *c.ScoreHundredths > 1000) {
		return fmt.Errorf("%w: score out of range", ErrContract)
	}
	if len(c.Gates) > 32 || len(c.QAEvidence) > 32 {
		return fmt.Errorf("%w: too much QA metadata", ErrContract)
	}
	for code, gate := range c.Gates {
		if !slices.Contains(mockCriticalGates[:], code) || (gate != Pass && gate != Fail && gate != Unknown && gate != NotApplicable) {
			return fmt.Errorf("%w: invalid gate result", ErrContract)
		}
	}
	for _, ref := range c.QAEvidence {
		if !identifier.MatchString(ref) {
			return fmt.Errorf("%w: invalid QA evidence reference", ErrContract)
		}
	}
	return nil
}

func (c Clip) qa() (eligible, uncertain bool) {
	if c.QAHash != c.RenderHash || c.ScoreHundredths == nil || len(c.QAEvidence) == 0 {
		return false, true
	}
	for _, code := range mockCriticalGates {
		switch c.Gates[code] {
		case Pass:
		case Fail:
			return false, false
		default:
			uncertain = true
		}
	}
	return !uncertain && *c.ScoreHundredths >= minClipScore, uncertain
}

func qualify(receipt OwnerReceipt) ([]Clip, bool) {
	var eligible []Clip
	unresolved := !receipt.AnalysisComplete
	for _, clip := range receipt.Clips {
		ok, unknown := clip.qa()
		unresolved = unresolved || unknown
		if ok {
			eligible = append(eligible, clip)
		}
	}
	sort.Slice(eligible, func(i, j int) bool {
		if *eligible[i].ScoreHundredths != *eligible[j].ScoreHundredths {
			return *eligible[i].ScoreHundredths > *eligible[j].ScoreHundredths
		}
		return eligible[i].ID < eligible[j].ID
	})

	var selected []Clip
	total := 0
	hashes := make(map[string]bool)
	windows := make(map[[2]int64]bool)
	for _, clip := range eligible {
		window := [2]int64{clip.StartMS, clip.EndMS}
		if hashes[clip.RenderHash] || windows[window] {
			continue
		}
		score := *clip.ScoreHundredths
		if len(selected) == maxDelivered || total+score < minBatchScore*(len(selected)+1) {
			break
		}
		selected = append(selected, clip)
		total += score
		hashes[clip.RenderHash] = true
		windows[window] = true
	}
	return selected, unresolved
}

type ExportReceipt struct {
	Version        string `json:"contract_version"`
	Key            string `json:"key"`
	ArtifactHash   string `json:"artifact_hash"`
	VerdictID      string `json:"verdict_id"`
	DestinationRef string `json:"destination_ref"`
}

func (r ExportReceipt) matches(key string, clip Clip) bool {
	return r.Version == Version && r.Key == key && r.ArtifactHash == clip.RenderHash &&
		r.VerdictID == clip.VerdictID && identifier.MatchString(r.DestinationRef)
}

func decodeStrict(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(dst); err != nil {
		return fmt.Errorf("%w: %v", ErrContract, err)
	}
	if err := d.Decode(new(json.RawMessage)); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON data", ErrContract)
	}
	return nil
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
