package freewill

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
	"time"
)

const ProjectContextManifestVersion uint16 = 1
const ProjectContextSourceManifestVersion uint16 = 2
const ProjectContextGitTrackedManifestVersion uint16 = 3

const (
	ProjectContextFactObserved                    = "observed"
	ProjectContextFactInferred                    = "inferred"
	ProjectContextFactUnknown                     = "unknown"
	ProjectContextConfidenceOwnerAuthorizedMemory = "owner_authorized_local_memory"
)

// IsOwnerAuthorizedMemorySource recognizes only the host-tagged, pathless
// memory source IDs used by the explicit local Freewill memory-context seam.
func IsOwnerAuthorizedMemorySource(source ProjectContextSource) bool {
	if source.Path != "" || source.Confidence != ProjectContextConfidenceOwnerAuthorizedMemory {
		return false
	}
	return strings.HasPrefix(source.ID, "memory:fact:") || strings.HasPrefix(source.ID, "memory:self-report:")
}

// ProjectContextSource is a bounded host-read file or host-authored fact.
// Content is untrusted project data and never grants capability or scope.
type ProjectContextSource struct {
	ID         string    `json:"id"`
	Path       string    `json:"path,omitempty"`
	Revision   string    `json:"revision"`
	Digest     string    `json:"digest"`
	ObservedAt time.Time `json:"observed_at"`
	Confidence string    `json:"confidence"`
	Content    string    `json:"content"`
	SourceType string    `json:"source_type,omitempty"`
	SourceURL  string    `json:"source_url,omitempty"`
	CommitSHA  string    `json:"commit_sha,omitempty"`
}

// ProjectContextSnapshot is an immutable, per-wake view of explicitly
// authorized project context. Unknown profile fields stay empty.
type ProjectContextSnapshot struct {
	ProjectID             string                 `json:"project_id"`
	Objective             string                 `json:"objective,omitempty"`
	Profile               ProjectContextProfile  `json:"profile"`
	RoadmapActions        []ProjectRoadmapAction `json:"roadmap_actions,omitempty"`
	CapturedAt            time.Time              `json:"captured_at"`
	Digest                string                 `json:"digest"`
	SelectorEgressAllowed bool                   `json:"selector_egress_allowed"`
	Sources               []ProjectContextSource `json:"sources"`
}

// ProjectRoadmapAction is a host-issued reference to exact, observed roadmap
// text. It bounds a proposed project task; it never grants execution authority.
type ProjectRoadmapAction struct {
	ID       string `json:"id"`
	Text     string `json:"text"`
	SourceID string `json:"source_id"`
}

// ProjectContextManifest is the versioned, content-free provenance projection
// stored with a Freewill run. File content remains ephemeral selector input.
type ProjectContextManifest struct {
	Version      uint16                      `json:"version"`
	ProjectID    string                      `json:"project_id"`
	CapturedAt   time.Time                   `json:"captured_at"`
	SnapshotHash string                      `json:"snapshot_hash"`
	Profile      ProjectContextProfile       `json:"profile"`
	Sources      []ProjectContextSourceEntry `json:"sources"`
}

type ProjectContextSourceEntry struct {
	ID         string    `json:"id"`
	Path       string    `json:"path,omitempty"`
	Revision   string    `json:"revision"`
	Digest     string    `json:"digest"`
	ObservedAt time.Time `json:"observed_at"`
	Confidence string    `json:"confidence"`
	SourceType string    `json:"source_type,omitempty"`
	SourceURL  string    `json:"source_url,omitempty"`
	CommitSHA  string    `json:"commit_sha,omitempty"`
}

// ProjectContextProfile separates host-known values from explicitly unknown
// dimensions. The selector may interpret sources but cannot author this record.
type ProjectContextProfile struct {
	Identity     ProjectContextFact       `json:"identity"`
	Purpose      ProjectContextFact       `json:"purpose"`
	Domain       ProjectContextFact       `json:"domain"`
	Users        ProjectContextFact       `json:"users"`
	Architecture ProjectContextFact       `json:"architecture"`
	Capabilities ProjectContextFact       `json:"capabilities"`
	Goals        ProjectContextFact       `json:"goals"`
	Exclusions   ProjectContextFact       `json:"exclusions"`
	History      ProjectContextFact       `json:"history"`
	Conflicts    []ProjectContextConflict `json:"conflicts,omitempty"`
	Claims       []ProjectContextClaim    `json:"claims,omitempty"`
}

type ProjectContextClaim struct {
	ID        string   `json:"id"`
	Status    string   `json:"status"`
	Value     string   `json:"value"`
	SourceIDs []string `json:"source_ids"`
}

// ProjectContextConflict retains incompatible claims as distinct evidence.
type ProjectContextConflict struct {
	ID       string   `json:"id"`
	ClaimIDs []string `json:"claim_ids"`
}

type ProjectContextFact struct {
	Status    string   `json:"status"`
	Value     string   `json:"value,omitempty"`
	SourceIDs []string `json:"source_ids,omitempty"`
}

func NewProjectContextSnapshot(projectID, objective string, capturedAt time.Time) ProjectContextSnapshot {
	if capturedAt.IsZero() {
		capturedAt = time.Now().UTC()
	}
	snapshot := ProjectContextSnapshot{ProjectID: projectID, Objective: strings.TrimSpace(objective), CapturedAt: capturedAt.UTC(), SelectorEgressAllowed: true,
		Profile: ProjectContextProfile{
			Identity:     ProjectContextFact{Status: ProjectContextFactObserved, Value: projectID, SourceIDs: []string{"host:project-binding"}},
			Purpose:      ProjectContextFact{Status: ProjectContextFactUnknown},
			Domain:       ProjectContextFact{Status: ProjectContextFactUnknown},
			Users:        ProjectContextFact{Status: ProjectContextFactUnknown},
			Architecture: ProjectContextFact{Status: ProjectContextFactUnknown},
			Capabilities: ProjectContextFact{Status: ProjectContextFactUnknown},
			Goals:        ProjectContextFact{Status: ProjectContextFactUnknown},
			Exclusions:   ProjectContextFact{Status: ProjectContextFactUnknown},
			History:      ProjectContextFact{Status: ProjectContextFactUnknown},
		}}
	snapshot.Sources = append(snapshot.Sources, ProjectContextSource{ID: "host:project-binding", ObservedAt: snapshot.CapturedAt,
		Confidence: "host_configuration", Content: projectID})
	if snapshot.Objective != "" {
		snapshot.Sources = append(snapshot.Sources, ProjectContextSource{ID: "host:objective", ObservedAt: snapshot.CapturedAt,
			Confidence: "host_configuration", Content: snapshot.Objective})
	}
	return snapshot
}

// Manifest projects a snapshot into bounded metadata without copying source
// text. Call after Seal so source revisions and the snapshot hash are fixed.
func (s ProjectContextSnapshot) Manifest() ProjectContextManifest {
	version := ProjectContextManifestVersion
	for _, source := range s.Sources {
		if source.SourceType == "git_tracked_file" {
			version = ProjectContextGitTrackedManifestVersion
			break
		}
		if source.SourceType != "" {
			version = ProjectContextSourceManifestVersion
		}
	}
	manifest := ProjectContextManifest{Version: version, ProjectID: s.ProjectID,
		CapturedAt: s.CapturedAt, SnapshotHash: s.Digest, Profile: cloneProjectContextProfile(s.Profile),
		Sources: make([]ProjectContextSourceEntry, 0, len(s.Sources))}
	for _, source := range s.Sources {
		sourceType := source.SourceType
		if version >= ProjectContextSourceManifestVersion && sourceType == "" {
			if source.Path == "" {
				sourceType = "host_fact"
			} else {
				sourceType = "local_file"
			}
		}
		manifest.Sources = append(manifest.Sources, ProjectContextSourceEntry{ID: source.ID, Path: source.Path,
			Revision: source.Revision, Digest: source.Digest, ObservedAt: source.ObservedAt, Confidence: source.Confidence,
			SourceType: sourceType, SourceURL: source.SourceURL, CommitSHA: source.CommitSHA})
	}
	return manifest
}

// Validate enforces stable manifest semantics and bounded, source-backed facts.
func (m ProjectContextManifest) Validate() error {
	if (m.Version != ProjectContextManifestVersion && m.Version != ProjectContextSourceManifestVersion && m.Version != ProjectContextGitTrackedManifestVersion) || strings.TrimSpace(m.ProjectID) == "" || len(m.ProjectID) > 128 ||
		m.CapturedAt.IsZero() || !validContextDigest(m.SnapshotHash) || len(m.Sources) == 0 || len(m.Sources) > 16 {
		return errors.New("freewill: project context manifest header is invalid")
	}
	sources := make(map[string]struct{}, len(m.Sources))
	for _, source := range m.Sources {
		if strings.TrimSpace(source.ID) == "" || len(source.ID) > 128 || len(source.Path) > 256 ||
			len(source.Revision) != 64 || !validContextDigest(source.Revision) || !validContextDigest(source.Digest) ||
			((m.Version != ProjectContextGitTrackedManifestVersion || source.SourceType != "git_tracked_file") && source.Digest != source.Revision) ||
			source.ObservedAt.IsZero() || len(source.Confidence) == 0 || len(source.Confidence) > 64 || !validProjectContextRelativePath(source.Path) {
			return errors.New("freewill: project context manifest source is invalid")
		}
		if m.Version == ProjectContextSourceManifestVersion || m.Version == ProjectContextGitTrackedManifestVersion {
			switch source.SourceType {
			case "github_repository_profile":
				if !validGitHubSourceURL(source.SourceURL) || !validCommitSHA(source.CommitSHA) || source.Digest != source.Revision {
					return errors.New("freewill: project context v2 source is invalid")
				}
			case "local_file", "host_fact":
				if source.SourceURL != "" || source.CommitSHA != "" {
					return errors.New("freewill: local v2 source has external provenance")
				}
			case "git_tracked_file":
				if m.Version != ProjectContextGitTrackedManifestVersion || source.SourceURL != "" || !validCommitSHA(source.CommitSHA) {
					return errors.New("freewill: git tracked source provenance is invalid")
				}
			default:
				return errors.New("freewill: project context v2 source type is invalid")
			}
		} else if source.SourceType != "" || source.SourceURL != "" || source.CommitSHA != "" {
			return errors.New("freewill: v1 project context cannot contain external provenance")
		}
		if _, exists := sources[source.ID]; exists {
			return errors.New("freewill: project context manifest source IDs are duplicated")
		}
		sources[source.ID] = struct{}{}
	}
	for name, fact := range projectContextProfileFacts(m.Profile) {
		if err := validateProjectContextFact(name, fact, sources); err != nil {
			return err
		}
	}
	claimIDs := map[string]struct{}{}
	for _, claim := range m.Profile.Claims {
		if claim.ID == "" || len(claim.ID) > 128 || (claim.Status != ProjectContextFactObserved && claim.Status != ProjectContextFactInferred && claim.Status != ProjectContextFactUnknown) || len(claim.Value) > 512 {
			return errors.New("freewill: project context claim is invalid")
		}
		if _, ok := claimIDs[claim.ID]; ok {
			return errors.New("freewill: duplicate project context claim")
		}
		claimIDs[claim.ID] = struct{}{}
		fact := ProjectContextFact{Status: claim.Status, Value: claim.Value, SourceIDs: claim.SourceIDs}
		if err := validateProjectContextFact("claim", fact, sources); err != nil {
			return err
		}
	}
	conflictIDs := map[string]struct{}{}
	for _, conflict := range m.Profile.Conflicts {
		if conflict.ID == "" || len(conflict.ID) > 128 || len(conflict.ClaimIDs) < 2 || len(conflict.ClaimIDs) > 16 {
			return errors.New("freewill: project context conflict is invalid")
		}
		if _, ok := conflictIDs[conflict.ID]; ok {
			return errors.New("freewill: duplicate project context conflict")
		}
		conflictIDs[conflict.ID] = struct{}{}
		for _, id := range conflict.ClaimIDs {
			if _, ok := claimIDs[id]; !ok {
				return errors.New("freewill: conflict cites unknown claim")
			}
		}
	}
	return nil
}

func validCommitSHA(s string) bool {
	if len(s) != 40 {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func validGitHubSourceURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host != "github.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.ContainsAny(u.Path, "\\\x00\r\n")
}

func validProjectContextRelativePath(value string) bool {
	if value == "" {
		return true
	}
	if strings.ContainsAny(value, "\\:\x00\r\n") || path.IsAbs(value) || path.Clean(value) != value {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if component == "" || component == "." || component == ".." {
			return false
		}
	}
	return true
}

func (m ProjectContextManifest) Clone() ProjectContextManifest {
	m.Sources = append([]ProjectContextSourceEntry(nil), m.Sources...)
	m.Profile = cloneProjectContextProfile(m.Profile)
	return m
}

func (s ProjectContextSnapshot) Clone() ProjectContextSnapshot {
	s.Sources = append([]ProjectContextSource(nil), s.Sources...)
	s.RoadmapActions = append([]ProjectRoadmapAction(nil), s.RoadmapActions...)
	s.Profile = cloneProjectContextProfile(s.Profile)
	return s
}

func validContextDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return false
		}
	}
	return true
}

func projectContextProfileFacts(profile ProjectContextProfile) map[string]ProjectContextFact {
	return map[string]ProjectContextFact{"identity": profile.Identity, "purpose": profile.Purpose, "domain": profile.Domain,
		"users": profile.Users, "architecture": profile.Architecture, "capabilities": profile.Capabilities,
		"goals": profile.Goals, "exclusions": profile.Exclusions, "history": profile.History}
}

func validateProjectContextFact(name string, fact ProjectContextFact, sources map[string]struct{}) error {
	switch fact.Status {
	case ProjectContextFactUnknown:
		if fact.Value != "" || len(fact.SourceIDs) != 0 {
			return fmt.Errorf("freewill: unknown project profile fact %q has a value", name)
		}
		return nil
	case ProjectContextFactObserved, ProjectContextFactInferred:
		if strings.TrimSpace(fact.Value) == "" || len(fact.Value) > 512 || len(fact.SourceIDs) == 0 || len(fact.SourceIDs) > 16 {
			return fmt.Errorf("freewill: project profile fact %q lacks bounded evidence", name)
		}
		seen := make(map[string]struct{}, len(fact.SourceIDs))
		for _, id := range fact.SourceIDs {
			if _, ok := sources[id]; !ok {
				return fmt.Errorf("freewill: project profile fact %q cites an unknown source", name)
			}
			if _, ok := seen[id]; ok {
				return fmt.Errorf("freewill: project profile fact %q duplicates a source", name)
			}
			seen[id] = struct{}{}
		}
		return nil
	default:
		return fmt.Errorf("freewill: project profile fact %q has an invalid status", name)
	}
}

func cloneProjectContextProfile(profile ProjectContextProfile) ProjectContextProfile {
	profile.Identity.SourceIDs = append([]string(nil), profile.Identity.SourceIDs...)
	profile.Purpose.SourceIDs = append([]string(nil), profile.Purpose.SourceIDs...)
	profile.Domain.SourceIDs = append([]string(nil), profile.Domain.SourceIDs...)
	profile.Users.SourceIDs = append([]string(nil), profile.Users.SourceIDs...)
	profile.Architecture.SourceIDs = append([]string(nil), profile.Architecture.SourceIDs...)
	profile.Capabilities.SourceIDs = append([]string(nil), profile.Capabilities.SourceIDs...)
	profile.Goals.SourceIDs = append([]string(nil), profile.Goals.SourceIDs...)
	profile.Exclusions.SourceIDs = append([]string(nil), profile.Exclusions.SourceIDs...)
	profile.History.SourceIDs = append([]string(nil), profile.History.SourceIDs...)
	profile.Conflicts = append([]ProjectContextConflict(nil), profile.Conflicts...)
	for i := range profile.Conflicts {
		profile.Conflicts[i].ClaimIDs = append([]string(nil), profile.Conflicts[i].ClaimIDs...)
	}
	profile.Claims = append([]ProjectContextClaim(nil), profile.Claims...)
	for i := range profile.Claims {
		profile.Claims[i].SourceIDs = append([]string(nil), profile.Claims[i].SourceIDs...)
	}
	return profile
}

// Seal assigns a deterministic digest to the snapshot content while retaining
// the capture timestamp as provenance. The digest excludes its own value.
func (s *ProjectContextSnapshot) Seal() {
	if s == nil {
		return
	}
	for i := range s.Sources {
		digest := sha256.Sum256([]byte(s.Sources[i].Content))
		s.Sources[i].Digest = hex.EncodeToString(digest[:])
		if s.Sources[i].SourceType != "git_tracked_file" || !validContextDigest(s.Sources[i].Revision) {
			s.Sources[i].Revision = s.Sources[i].Digest
		}
	}
	copy := *s
	copy.Digest = ""
	encoded, _ := json.Marshal(copy)
	digest := sha256.Sum256(encoded)
	s.Digest = hex.EncodeToString(digest[:])
}

// SourceIDs returns the exact host-issued identifiers accepted for candidate
// provenance validation.
func (s ProjectContextSnapshot) SourceIDs() map[string]struct{} {
	ids := make(map[string]struct{}, len(s.Sources))
	for _, source := range s.Sources {
		if strings.TrimSpace(source.ID) != "" {
			ids[source.ID] = struct{}{}
		}
	}
	return ids
}
