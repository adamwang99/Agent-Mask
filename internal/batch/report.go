package batch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// writeReports writes report.json (for programs) and report.md (for people) in the output
// folder. Neither holds any hidden text: only file names, counts and kinds.
func writeReports(j *Job) error {
	if err := os.MkdirAll(j.Output, 0o755); err != nil {
		return err
	}
	b, _ := json.MarshalIndent(j, "", "  ")
	if err := os.WriteFile(filepath.Join(j.Output, "report.json"), b, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(j.Output, "report.md"), []byte(Markdown(j)), 0o644)
}

// Summary: one line.
func Summary(j *Job) string {
	c := j.Counts
	s := fmt.Sprintf("%d file(s): %d verified, %d to review, %d failed, %d skipped (unchanged)", c.Total, c.Verified, c.Review, c.Failed, c.Skipped)
	if j.Status == "canceled" {
		s += fmt.Sprintf("; canceled after %d", c.Done)
	}
	return s
}

// Markdown report.
func Markdown(j *Job) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Agent Mask report\n\n- Input: `%s`\n- Output: `%s`\n- Status: %s\n- %s\n", j.Input, j.Output, j.Status, Summary(j))
	if j.Started != nil && j.Finished != nil {
		fmt.Fprintf(&b, "- Time: %s\n", j.Finished.Sub(*j.Started).Round(1e9))
	}
	if j.Counts.Review > 0 {
		fmt.Fprintf(&b, "\n**%d file(s) in `%s/` still show something the check could read: look at them before sharing.**\n", j.Counts.Review, ReviewDir)
	}
	b.WriteString("\n| File | Result | Hidden | Kinds | Note |\n|---|---|---|---|---|\n")
	for _, f := range j.Files {
		rel, _ := filepath.Rel(j.Input, f.Input)
		note := f.Error
		if note == "" && len(f.Reasons) > 0 {
			note = strings.Join(f.Reasons, "; ")
		}
		var kinds []string
		for k, n := range f.Kinds {
			kinds = append(kinds, fmt.Sprintf("%s %d", k, n))
		}
		sort.Strings(kinds)
		fmt.Fprintf(&b, "| %s | %s | %d | %s | %s |\n", md(rel), f.Status, f.Hidden, strings.Join(kinds, ", "), md(note))
	}
	b.WriteString("\nAgent Mask reads text on screen (OCR). Faces, voices, and text too small or blurred to read are not hidden.\n")
	return b.String()
}

func md(s string) string { return strings.NewReplacer("|", "\\|", "\n", " ").Replace(s) }
