package skillcliapp

import (
	"fmt"
	"strings"
	"text/tabwriter"
)

// RenderInventoryPlain renders public inventory metadata.
func RenderInventoryPlain(in Inventory) string {
	var out strings.Builder
	w := tabwriter.NewWriter(&out, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tNAME\tOWNER\tENABLED\tPENDING\tREVISION")
	for _, entry := range in.Entries {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%t\t%t\t%s\n", entry.ID, entry.Name, entry.Ownership, entry.Enabled, entry.Pending, entry.Revision)
	}
	_ = w.Flush()
	return strings.TrimRight(out.String(), "\n")
}

// RenderEntryPlain renders one public skill entry.
func RenderEntryPlain(entry Entry) string {
	lines := []string{
		"ID: " + entry.ID,
		"Name: " + entry.Name,
		"Ownership: " + string(entry.Ownership),
		fmt.Sprintf("Enabled: %t", entry.Enabled),
		fmt.Sprintf("Pending: %t", entry.Pending),
	}
	if entry.Description != "" {
		lines = append(lines, "Description: "+entry.Description)
	}
	if entry.Revision != "" {
		lines = append(lines, "Revision: "+entry.Revision)
	}
	if entry.CurrentRevision != "" {
		lines = append(lines, "Current revision: "+entry.CurrentRevision)
	}
	return strings.Join(lines, "\n")
}

// RenderReviewPlain renders the bounded mutation summary before confirmation.
func RenderReviewPlain(review Review) string {
	lines := []string{"Operation: " + review.Operation}
	if review.Entry.ID != "" {
		lines = append(lines, "Skill: "+review.Entry.Name+" ("+review.Entry.ID+")")
	}
	if review.Entry.Revision != "" {
		lines = append(lines, "Revision: "+review.Entry.Revision)
	}
	if review.ResolvedCommit != "" {
		lines = append(lines, "Commit: "+review.ResolvedCommit)
	}
	if review.SkillPath != "" {
		lines = append(lines, "Skill path: "+review.SkillPath)
	}
	lines = append(lines, "Impact: "+review.Impact)
	return strings.Join(lines, "\n")
}

// RenderResultPlain renders durable acceptance and publication separately.
func RenderResultPlain(result Result) string {
	status := "no change"
	if result.Accepted && result.Published {
		status = "accepted and published"
	} else if result.Accepted && result.Pending {
		status = "accepted; publication pending (run balda skill recover)"
	}
	lines := []string{
		"Status: " + status,
		fmt.Sprintf("Generation: %d", result.Generation),
	}
	if result.RestartRequired {
		lines = append(lines, "Restart Balda to activate the updated catalog.")
	}
	if result.ResetRequired {
		lines = append(lines, "Existing sessions require /reset after restart.")
	}
	return strings.Join(lines, "\n")
}
