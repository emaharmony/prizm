package freewill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestProjectContextManifestIsVersionedContentFreeAndSourceBound(t *testing.T) {
	snapshot := NewProjectContextSnapshot("synthetic", "Choose a scoped review.", time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC))
	snapshot.Sources = append(snapshot.Sources, ProjectContextSource{ID: "project:readme", Path: "README.md", Content: "private raw project text",
		ObservedAt: snapshot.CapturedAt, Confidence: "direct_file_observation"})
	snapshot.Profile.Domain = ProjectContextFact{Status: ProjectContextFactInferred, Value: "synthetic domain",
		SourceIDs: []string{"project:readme"}}
	snapshot.Seal()

	manifest := snapshot.Manifest()
	if manifest.Version != ProjectContextManifestVersion {
		t.Fatalf("manifest version = %d", manifest.Version)
	}
	if manifest.Profile.Purpose.Status != ProjectContextFactUnknown || manifest.Profile.Purpose.Value != "" || strings.Contains(string(mustJSON(t, manifest)), "Choose a scoped review") {
		t.Fatalf("task objective was misclassified/persisted as observed project purpose: %#v", manifest.Profile.Purpose)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	encoded, err := json.Marshal(manifest)
	if err != nil || strings.Contains(string(encoded), "private raw project text") || strings.Contains(string(encoded), "Choose a scoped review") {
		t.Fatalf("manifest copied source content: %s err=%v", encoded, err)
	}
	if manifest.Profile.Domain.Status != ProjectContextFactInferred || manifest.Profile.History.Status != ProjectContextFactUnknown {
		t.Fatalf("observed/inferred/unknown distinction lost: %#v", manifest.Profile)
	}

	manifest.Profile.Domain.SourceIDs[0] = "unknown-source"
	if err := manifest.Validate(); err == nil {
		t.Fatal("manifest accepted a profile fact with an unknown source")
	}
}

func TestImportedProfileUsesPinnedV2ProvenanceAndKeepsConflictingClaims(t *testing.T) {
	url := "https://github.com/example/project"
	commit := strings.Repeat("a", 40)
	facts := ProjectContextProfile{Identity: ProjectContextFact{Status: ProjectContextFactObserved, Value: "synthetic", SourceIDs: []string{"github:profile"}}, Purpose: ProjectContextFact{Status: ProjectContextFactObserved, Value: "example purpose", SourceIDs: []string{"github:profile"}}, Domain: ProjectContextFact{Status: ProjectContextFactInferred, Value: "example domain", SourceIDs: []string{"github:profile"}}, Users: ProjectContextFact{Status: ProjectContextFactUnknown}, Architecture: ProjectContextFact{Status: ProjectContextFactUnknown}, Capabilities: ProjectContextFact{Status: ProjectContextFactUnknown}, Goals: ProjectContextFact{Status: ProjectContextFactUnknown}, Exclusions: ProjectContextFact{Status: ProjectContextFactUnknown}, History: ProjectContextFact{Status: ProjectContextFactUnknown}}
	facts.Claims = []ProjectContextClaim{{ID: "docs-stack", Status: ProjectContextFactObserved, Value: "documented framework claim", SourceIDs: []string{"github:profile"}}, {ID: "source-stack", Status: ProjectContextFactObserved, Value: "implemented framework claim", SourceIDs: []string{"github:profile"}}}
	facts.Conflicts = []ProjectContextConflict{{ID: "stack-disagreement", ClaimIDs: []string{"docs-stack", "source-stack"}}}
	body, _ := json.Marshal(struct {
		ProjectID     string                `json:"project_id"`
		RepositoryURL string                `json:"repository_url"`
		CommitSHA     string                `json:"commit_sha"`
		Facts         ProjectContextProfile `json:"facts"`
	}{"synthetic", url, commit, facts})
	d := sha256.Sum256(body)
	profile := ImportedProjectProfile{Version: 1, ProjectID: "synthetic", RepositoryURL: url, CommitSHA: commit, ProfileDigest: hex.EncodeToString(d[:]), Facts: facts}
	snapshot := NewProjectContextSnapshot("synthetic", "review", time.Now().UTC())
	snapshot, err := ApplyImportedProjectProfile(snapshot, profile, url, commit)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Seal()
	manifest := snapshot.Manifest()
	if manifest.Version != ProjectContextSourceManifestVersion {
		t.Fatalf("version=%d", manifest.Version)
	}
	if err := manifest.Validate(); err != nil {
		t.Fatal(err)
	}
	if manifest.Sources[len(manifest.Sources)-1].SourceURL != url || manifest.Sources[len(manifest.Sources)-1].CommitSHA != commit || manifest.Sources[len(manifest.Sources)-1].Confidence != "host_reviewed_uncertain_import" {
		t.Fatalf("provenance=%#v", manifest.Sources[len(manifest.Sources)-1])
	}
	profile.ProfileDigest = strings.Repeat("0", 64)
	if _, err := ApplyImportedProjectProfile(NewProjectContextSnapshot("synthetic", "", time.Now()), profile, url, commit); err == nil {
		t.Fatal("stale digest accepted")
	}
	profile.ProfileDigest = hex.EncodeToString(d[:])
	if _, err := ApplyImportedProjectProfile(NewProjectContextSnapshot("wrong", "", time.Now()), profile, url, commit); err == nil {
		t.Fatal("profile identity mismatch accepted")
	}
	if _, err := ApplyImportedProjectProfile(NewProjectContextSnapshot("synthetic", "", time.Now()), profile, "https://example.test/repo", commit); err == nil {
		t.Fatal("repository URL mismatch accepted")
	}
	if _, err := ApplyImportedProjectProfile(NewProjectContextSnapshot("synthetic", "", time.Now()), profile, url, strings.Repeat("b", 40)); err == nil {
		t.Fatal("pinned commit mismatch accepted")
	}
	manifest.Sources[len(manifest.Sources)-1].SourceType = "unrecognized"
	if err := manifest.Validate(); err == nil {
		t.Fatal("unknown v2 source type accepted")
	}
}

func TestProjectContextManifestRejectsNonPortableSourcePaths(t *testing.T) {
	snapshot := NewProjectContextSnapshot("synthetic", "objective", time.Now().UTC())
	snapshot.Sources = append(snapshot.Sources, ProjectContextSource{ID: "project:readme", Path: "README.md", Content: "safe content",
		ObservedAt: snapshot.CapturedAt, Confidence: "direct_file_observation"})
	snapshot.Seal()
	base := snapshot.Manifest()
	for _, invalid := range []string{".", "..", "../secret", "a/../secret", `..\secret`, `folder\..\secret`, `C:\project\README.md`, "C:/project/README.md", "//server/share", "a//b", "a/./b"} {
		t.Run(invalid, func(t *testing.T) {
			manifest := base.Clone()
			for index := range manifest.Sources {
				if manifest.Sources[index].ID == "project:readme" {
					manifest.Sources[index].Path = invalid
				}
			}
			if err := manifest.Validate(); err == nil {
				t.Fatalf("nonportable/traversal source path %q was accepted", invalid)
			}
		})
	}
}

func TestProjectContextManifestRejectsUnknownSchemaVersion(t *testing.T) {
	snapshot := NewProjectContextSnapshot("synthetic", "objective", time.Now().UTC())
	snapshot.Seal()
	manifest := snapshot.Manifest()
	manifest.Version++
	if err := manifest.Validate(); err == nil {
		t.Fatal("unknown manifest schema version was accepted")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
