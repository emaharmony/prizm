package freewill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ImportedProjectProfile is a content-free, host-reviewed local export. It does
// not assert that GitHub was freshly queried or that the claims are verified.
type ImportedProjectProfile struct {
	Version       uint16                `json:"version"`
	ProjectID     string                `json:"project_id"`
	RepositoryURL string                `json:"repository_url"`
	CommitSHA     string                `json:"commit_sha"`
	ProfileDigest string                `json:"profile_digest"`
	Facts         ProjectContextProfile `json:"facts"`
}

// ApplyImportedProjectProfile binds an explicitly loaded host-reviewed profile
// to its configured project/repository and the exact content digest.
func ApplyImportedProjectProfile(snapshot ProjectContextSnapshot, imported ImportedProjectProfile, expectedRepositoryURL, expectedCommitSHA string) (ProjectContextSnapshot, error) {
	if imported.Version != 1 || imported.ProjectID != snapshot.ProjectID || !validGitHubSourceURL(imported.RepositoryURL) || imported.RepositoryURL != expectedRepositoryURL || !validCommitSHA(imported.CommitSHA) || imported.CommitSHA != expectedCommitSHA {
		return ProjectContextSnapshot{}, errors.New("freewill: imported project profile identity or source binding mismatch")
	}
	content := struct {
		ProjectID     string                `json:"project_id"`
		RepositoryURL string                `json:"repository_url"`
		CommitSHA     string                `json:"commit_sha"`
		Facts         ProjectContextProfile `json:"facts"`
	}{imported.ProjectID, imported.RepositoryURL, imported.CommitSHA, imported.Facts}
	encoded, err := json.Marshal(content)
	if err != nil {
		return ProjectContextSnapshot{}, err
	}
	digest := sha256.Sum256(encoded)
	if imported.ProfileDigest != hex.EncodeToString(digest[:]) {
		return ProjectContextSnapshot{}, errors.New("freewill: imported project profile digest is stale")
	}
	if strings.TrimSpace(imported.Facts.Identity.Value) != imported.ProjectID || imported.Facts.Identity.Status != ProjectContextFactObserved {
		return ProjectContextSnapshot{}, errors.New("freewill: imported project profile identity fact is invalid")
	}
	for name, fact := range projectContextProfileFacts(imported.Facts) {
		if err := validateImportedFact(name, fact); err != nil {
			return ProjectContextSnapshot{}, err
		}
	}
	claimIDs := map[string]struct{}{}
	for _, claim := range imported.Facts.Claims {
		if _, ok := claimIDs[claim.ID]; ok {
			return ProjectContextSnapshot{}, errors.New("freewill: duplicate imported claim")
		}
		claimIDs[claim.ID] = struct{}{}
		if err := validateImportedFact(claim.ID, ProjectContextFact{Status: claim.Status, Value: claim.Value, SourceIDs: claim.SourceIDs}); err != nil {
			return ProjectContextSnapshot{}, err
		}
	}
	for _, conflict := range imported.Facts.Conflicts {
		for _, id := range conflict.ClaimIDs {
			if _, ok := claimIDs[id]; !ok {
				return ProjectContextSnapshot{}, errors.New("freewill: imported conflict cites unknown claim")
			}
		}
	}
	id := "github:profile"
	snapshot.Sources = append(snapshot.Sources, ProjectContextSource{ID: id, Revision: digestString(encoded), Digest: digestString(encoded), ObservedAt: time.Now().UTC(), Confidence: "host_reviewed_uncertain_import", Content: string(encoded), SourceType: "github_repository_profile", SourceURL: imported.RepositoryURL, CommitSHA: imported.CommitSHA})
	profile := cloneProjectContextProfile(imported.Facts)
	for _, fact := range projectContextProfileFacts(profile) {
		for _, sourceID := range fact.SourceIDs {
			if sourceID != id {
				return ProjectContextSnapshot{}, errors.New("freewill: imported profile fact references unrecognized evidence")
			}
		}
	}
	snapshot.Profile = profile
	return snapshot, nil
}

func digestString(b []byte) string { d := sha256.Sum256(b); return hex.EncodeToString(d[:]) }
func validateImportedFact(name string, fact ProjectContextFact) error {
	if fact.Status == ProjectContextFactUnknown {
		if fact.Value != "" || len(fact.SourceIDs) != 0 {
			return errors.New("freewill: imported unknown fact carries a claim")
		}
		return nil
	}
	if (fact.Status != ProjectContextFactObserved && fact.Status != ProjectContextFactInferred) || strings.TrimSpace(fact.Value) == "" || len(fact.Value) > 512 || len(fact.SourceIDs) != 1 || fact.SourceIDs[0] != "github:profile" {
		return errors.New("freewill: imported fact is invalid")
	}
	_ = name
	return nil
}
