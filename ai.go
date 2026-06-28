package main

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"google.golang.org/genai"
)

// GenerateWorkLogs synthesizes a work log from commits and merge-request activity.
// When perDay is true the output is grouped under per-date headers; otherwise it is
// a single numbered list. MR engagement is phrased according to the rules baked into
// the system instruction (Tested / Reviewed / Re-tested / Tested and reviewed).
func GenerateWorkLogs(ctx context.Context, apiKey, modelName, userName, userEmail string,
	commits []GitLabCommit, mrs []MRInvolvement, since, until time.Time, perDay bool) (string, error) {

	if len(commits) == 0 && len(mrs) == 0 {
		return "No activity found in the specified duration.", nil
	}

	var clientConfig *genai.ClientConfig
	if apiKey != "" {
		clientConfig = &genai.ClientConfig{APIKey: apiKey}
	}
	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %w", err)
	}

	if modelName == "" {
		modelName = "gemini-2.5-flash"
	}

	prompt := buildActivityContext(userName, userEmail, commits, mrs)
	systemInstruction := buildSystemInstruction(perDay)

	resp, err := client.Models.GenerateContent(ctx, modelName, genai.Text(prompt), &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemInstruction, ""),
		Temperature:       genai.Ptr[float32](0.2),
	})
	if err != nil {
		return "", fmt.Errorf("Gemini API call failed: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response candidates returned by Gemini")
	}

	return strings.TrimSpace(resp.Candidates[0].Content.Parts[0].Text), nil
}

// buildActivityContext renders commits and MR engagement (with dates) into the prompt body.
func buildActivityContext(userName, userEmail string, commits []GitLabCommit, mrs []MRInvolvement) string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Developer: %s (%s)\n\n", userName, userEmail))

	sb.WriteString("COMMITS:\n")
	if len(commits) == 0 {
		sb.WriteString("(none)\n")
	} else {
		sorted := make([]GitLabCommit, len(commits))
		copy(sorted, commits)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].CommittedDate.Before(sorted[j].CommittedDate) })
		for _, c := range sorted {
			msg := strings.TrimSpace(c.Message)
			if len(msg) > 300 {
				msg = msg[:300] + "..."
			}
			msg = strings.ReplaceAll(msg, "\n", " ")
			sb.WriteString(fmt.Sprintf("[%s] %s\n", c.CommittedDate.Local().Format("2006-01-02"), msg))
		}
	}

	sb.WriteString("\nMERGE REQUEST ACTIVITY:\n")
	if len(mrs) == 0 {
		sb.WriteString("(none)\n")
	} else {
		sorted := make([]MRInvolvement, len(mrs))
		copy(sorted, mrs)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].LastDate.Before(sorted[j].LastDate) })
		for _, mr := range sorted {
			sb.WriteString(fmt.Sprintf("MR %d:%s (assignee=%v, formalReviewer=%v)\n",
				mr.IID, mr.Title, mr.IsAssignee, mr.IsReviewer))
			if len(mr.Comments) == 0 {
				sb.WriteString(fmt.Sprintf("  [%s] (no comment; assignee/reviewer only)\n",
					mr.LastDate.Local().Format("2006-01-02")))
				continue
			}
			for _, cm := range mr.Comments {
				body := strings.TrimSpace(cm.Body)
				if len(body) > 500 {
					body = body[:500] + "..."
				}
				body = strings.ReplaceAll(body, "\n", " ⏎ ")
				sb.WriteString(fmt.Sprintf("  [%s] my comment: %s\n", cm.Date.Local().Format("2006-01-02"), body))
			}
		}
	}

	return sb.String()
}

// buildSystemInstruction encodes the work-log rules, including MR phrasing and output format.
func buildSystemInstruction(perDay bool) string {
	var b strings.Builder
	b.WriteString("You are an expert developer and project manager. ")
	b.WriteString("Build a concise, professional work log from the developer's commits AND merge request (MR) activity. ")
	b.WriteString("Focus on technical and business value. Ignore trivial commits (typo fixes, dependency bumps, automated/merge commits).\n\n")

	b.WriteString("MERGE REQUEST RULES — for each MR the developer engaged with, emit exactly ONE line. ")
	b.WriteString("Decide the verb from these signals (a comment 'quotes code' if it contains a markdown code block, inline `code`, or a quoted diff line; ")
	b.WriteString("a comment 'lists verified scenarios' if it describes tested steps/scenarios with expected results or words like 'verified'/'tested'):\n")
	b.WriteString("- developer is assignee AND left at least one comment  -> \"Tested MR <IID>:<Title>\"\n")
	b.WriteString("- a comment quotes a piece of code, OR developer is the formal reviewer -> \"Reviewed MR <IID>:<Title>\"\n")
	b.WriteString("- a comment lists verified scenarios -> \"Re-tested MR <IID>:<Title>\"\n")
	b.WriteString("- if BOTH a testing signal (assignee+comment / verified scenarios) AND a reviewing signal (quoted code / formal reviewer) apply -> \"Tested and reviewed MR <IID>:<Title>\"\n")
	b.WriteString("Use the MR's exact <IID> and <Title> as given. Do not invent MRs.\n\n")

	b.WriteString("COMMIT RULES — these are the developer's own authored work. Summarize concrete achievements ")
	b.WriteString("as their own work-log items using active authoring verbs like \"Worked on\", \"Implemented\", \"Fixed\", \"Refactored\", \"Added\". ")
	b.WriteString("Never describe the developer's own commits/MRs as tested or reviewed.\n\n")

	b.WriteString("OUTPUT FORMAT — a NUMBERED markdown list, one achievement per line, like:\n")
	b.WriteString("1. Fixed and resolved maintenance page comments\n")
	b.WriteString("2. Decoupled audio and video streams for better debuggability\n")
	if perDay {
		b.WriteString("\nGROUP BY DATE: precede each day's list with a header line \"### YYYY-MM-DD\", then that day's numbered list (restart numbering at 1 each day). ")
		b.WriteString("Attribute each item to the date shown in brackets next to it. Order dates ascending.\n")
	}
	b.WriteString("\nOutput ONLY the list (and date headers if grouping). No introductory or concluding text.")
	return b.String()
}
