package main

import "strings"

// approval_buttons.go is the correctness-critical core of rich Discord approval
// cards: it builds the button set shown at a feedback gate and maps a clicked
// button's custom ID back to the exact feedback-response payload the existing
// command path (handleWorkflowFeedbackCommand) publishes. Keeping this layer pure
// means the action↔decision mapping is unit-tested; the discordgo
// component-send / InteractionCreate wiring is thin glue over it.

// feedbackButtonStyle mirrors Discord's button styles (1=primary, 3=success,
// 4=danger) without importing discordgo here, so this stays dependency-free.
type feedbackButtonStyle int

const (
	stylePrimary feedbackButtonStyle = 1
	styleSuccess feedbackButtonStyle = 3
	styleDanger  feedbackButtonStyle = 4
)

// feedbackButton is a single approval-card button.
type feedbackButton struct {
	Label    string
	Style    feedbackButtonStyle
	CustomID string
}

// customIDPrefix namespaces our component custom IDs so the interaction handler can
// cheaply ignore buttons that aren't ours.
const customIDPrefix = "prizmfb"
const fileApprovalPrefix = "prizmapprove"

// encodeFeedbackButtonID builds a custom ID of the form
// "prizmfb:<gate>:<action>:<runID>" where gate is pre|post.
func encodeFeedbackButtonID(gate, action, runID string) string {
	return strings.Join([]string{customIDPrefix, gate, action, runID}, ":")
}

// decodeFeedbackButtonID parses a custom ID back to (gate, action, runID). ok is
// false for any non-Prizm or malformed ID. runID may itself contain ':' (it does
// not today, but be safe) so we split into at most 4 parts.
func decodeFeedbackButtonID(customID string) (gate, action, runID string, ok bool) {
	parts := strings.SplitN(customID, ":", 4)
	if len(parts) != 4 || parts[0] != customIDPrefix {
		return "", "", "", false
	}
	if parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

// buildFeedbackButtons returns the buttons for a feedback gate. FEEDBACK_PRE
// (plan approval) offers approve / changes / reject; FEEDBACK_POST (review) offers
// approve / changes (a rejected review just loops back via changes). Returns nil
// for any other phase.
func buildFeedbackButtons(phase, runID string) []feedbackButton {
	gate := ""
	switch phase {
	case "FEEDBACK_PRE":
		gate = "pre"
	case "FEEDBACK_POST":
		gate = "post"
	default:
		return nil
	}
	btns := []feedbackButton{
		{Label: "Approve", Style: styleSuccess, CustomID: encodeFeedbackButtonID(gate, "approve", runID)},
		{Label: "Request changes", Style: stylePrimary, CustomID: encodeFeedbackButtonID(gate, "changes", runID)},
	}
	if gate == "pre" {
		btns = append(btns, feedbackButton{Label: "Reject", Style: styleDanger, CustomID: encodeFeedbackButtonID(gate, "reject", runID)})
	}
	return btns
}

// buttonActionToDecision maps a button action to the workflow decision string used
// in feedback-response payloads.
func buttonActionToDecision(action string) (string, bool) {
	switch action {
	case "approve":
		return "approved", true
	case "changes":
		return "changes_requested", true
	case "reject":
		return "rejected", true
	default:
		return "", false
	}
}

// feedbackButtonPayload turns a clicked button custom ID into the feedback-response
// payload to publish on prizm.workflow.feedback.response. It mirrors the shapes
// produced by handleWorkflowFeedbackCommand: a "review_response" for the post gate
// and a "feedback_response" for the pre gate. Returns ok=false for IDs that aren't
// ours, are malformed, target a non-gated-loop run, or carry an unknown action.
func feedbackButtonPayload(customID, reviewer string) (map[string]any, bool) {
	gate, action, runID, ok := decodeFeedbackButtonID(customID)
	if !ok {
		return nil, false
	}
	if !strings.HasPrefix(runID, "gl-") {
		return nil, false
	}
	decision, ok := buttonActionToDecision(action)
	if !ok {
		return nil, false
	}
	if reviewer == "" {
		reviewer = "discord"
	}
	respType := "feedback_response"
	if gate == "post" {
		respType = "review_response"
	}
	return map[string]any{
		"type":        respType,
		"decision":    decision,
		"workflow_id": runID,
		"reviewer":    reviewer,
		"notes":       "",
	}, true
}

// encodeFileApprovalButtonID builds a custom ID for file write approval buttons.
// Format: "prizmapprove:<approval_id>:<run_id>:<action>"
func encodeFileApprovalButtonID(approvalID, runID, action string) string {
	return strings.Join([]string{fileApprovalPrefix, approvalID, runID, action}, ":")
}

// decodeFileApprovalButtonID parses a file approval button custom ID.
// Returns approval_id, run_id, action, ok. ok is false for non-Prizm or malformed IDs.
func decodeFileApprovalButtonID(customID string) (approvalID, runID, action string, ok bool) {
	parts := strings.SplitN(customID, ":", 5)
	if len(parts) != 4 || parts[0] != fileApprovalPrefix {
		return "", "", "", false
	}
	if parts[1] == "" || parts[2] == "" || parts[3] == "" {
		return "", "", "", false
	}
	return parts[1], parts[2], parts[3], true
}

// buildFileApprovalButtons returns Approve and Deny buttons for a file write approval.
func buildFileApprovalButtons(approvalID, runID string) []feedbackButton {
	return []feedbackButton{
		{Label: "✅ Approve", Style: styleSuccess, CustomID: encodeFileApprovalButtonID(approvalID, runID, "approve")},
		{Label: "❌ Deny", Style: styleDanger, CustomID: encodeFileApprovalButtonID(approvalID, runID, "deny")},
	}
}
