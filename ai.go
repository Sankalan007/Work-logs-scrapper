package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"google.golang.org/genai"
)

// GenerateWorkLogs takes a user's name, email, list of commits, and duration bounds to generate a dynamic-length work log.
func GenerateWorkLogs(ctx context.Context, apiKey, modelName, userName, userEmail string, commits []GitLabCommit, since, until time.Time) (string, error) {
	if len(commits) == 0 {
		return "No commits found in the specified duration.", nil
	}

	// Initialize the client
	var clientConfig *genai.ClientConfig
	if apiKey != "" {
		clientConfig = &genai.ClientConfig{
			APIKey: apiKey,
		}
	}
	client, err := genai.NewClient(ctx, clientConfig)
	if err != nil {
		return "", fmt.Errorf("failed to create Gemini client: %w", err)
	}

	if modelName == "" {
		modelName = "gemini-2.5-flash"
	}

	// Prepare commit history block
	var sb strings.Builder
	for _, c := range commits {
		msg := strings.TrimSpace(c.Message)
		// Truncate message if it's too long
		if len(msg) > 300 {
			msg = msg[:300] + "..."
		}
		sb.WriteString(fmt.Sprintf("- %s: %s\n", c.CommittedDate.Format("2006-01-02"), msg))
	}

	// Calculate target bullet points based on the duration (in days)
	days := int(until.Sub(since).Hours() / 24)
	if days < 1 {
		days = 1
	}

	var pointerRange string
	switch {
	case days <= 1:
		pointerRange = "1-3"
	case days <= 4:
		pointerRange = "2-5"
	case days <= 9:
		pointerRange = "3-6"
	case days <= 35:
		pointerRange = "4-8"
	case days <= 100:
		pointerRange = "5-10"
	default:
		pointerRange = "6-12"
	}

	prompt := fmt.Sprintf("Analyze the following GitLab commit messages for user %s (%s) and generate the work log:\n\n%s", userName, userEmail, sb.String())

	systemInstruction := fmt.Sprintf("You are an expert developer and project manager. Your job is to analyze a developer's raw commit messages and extract a clean, concise, %s pointer professional work log summarizing their achievements. Focus on technical and business value. Ignore minor/trivial commits like typo fixes, dependency updates, or automated tasks. Output ONLY markdown bullet points using '*' as the bullet symbol. Do NOT include any introductory or concluding text.", pointerRange)

	// Call the model
	resp, err := client.Models.GenerateContent(ctx, modelName, genai.Text(prompt), &genai.GenerateContentConfig{
		SystemInstruction: genai.NewContentFromText(systemInstruction, ""),
		Temperature:       genai.Ptr[float32](0.2), // Low temperature for factual summarization
	})
	if err != nil {
		return "", fmt.Errorf("Gemini API call failed: %w", err)
	}

	if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
		return "", fmt.Errorf("no response candidates returned by Gemini")
	}

	resultText := resp.Candidates[0].Content.Parts[0].Text
	return strings.TrimSpace(resultText), nil
}
