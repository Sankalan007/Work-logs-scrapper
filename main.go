package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
)

// ANSI colors for premium terminal UI
const (
	colorReset  = "\033[0m"
	colorBold   = "\033[1m"
	colorDim    = "\033[90m"
	colorRed    = "\033[31m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorBlue   = "\033[34m"
	colorCyan   = "\033[36m"
)

func main() {
	// 1. Load config from file first
	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError loading config file: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	// 2. Define CLI flags, defaulting to config values if present
	flagInstance := flag.String("instance", "", "GitLab instance URL (defaults to https://gitlab.com)")
	flagToken := flag.String("token", "", "GitLab Personal Access Token")
	flagAPIKey := flag.String("api-key", "", "Google GenAI API Key (or set GEMINI_API_KEY env var)")
	flagDuration := flag.String("duration", "7d", "Duration for work logs (1d, 3d, 7d, 1m, 3m, 12m or YYYY-MM-DD[:YYYY-MM-DD])")
	flagProjects := flag.String("projects", "", "Comma-separated list of specific project IDs or paths (restricts scraping)")
	flagEmail := flag.String("email", "", "Override email to filter commits (defaults to autodetected user email)")
	flagModel := flag.String("model", "gemini-2.5-flash", "Gemini model to use")
	flagSave := flag.Bool("save", false, "Save provided instance, token, and API key to ~/.gitlab-worklogs.json and exit")
	flagDryRun := flag.Bool("dry-run", false, "Dry run: print commits fetched from GitLab without calling Gemini")

	// Custom usage text
	flag.Usage = func() {
		fmt.Printf("%sGitLab Work Logs Scraper CLI%s\n", colorBold+colorCyan, colorReset)
		fmt.Println("Scrapes your GitLab commit activity and uses Google GenAI to synthesize it into a clean work log.")
		fmt.Println("\nUsage:")
		fmt.Println("  gitlab-worklogs [flags]")
		fmt.Println("\nFlags:")
		flag.PrintDefaults()
		fmt.Println("\nExamples:")
		fmt.Println("  # First-time setup:")
		fmt.Println("  gitlab-worklogs -save -instance https://gitlab.com -token glpat-XXX -api-key AIzaSyXXX")
		fmt.Println("\n  # Generate work log for last 7 days (default):")
		fmt.Println("  gitlab-worklogs")
		fmt.Println("\n  # Generate work log for a custom date range:")
		fmt.Println("  gitlab-worklogs -duration 2026-06-01:2026-06-05")
		fmt.Println("\n  # Dry run to see commits fetched:")
		fmt.Println("  gitlab-worklogs -duration 3d -dry-run")
	}

	flag.Parse()

	// 3. Resolve configuration precedence: CLI Flag > Environment Variable > Config File > Default
	instanceURL := getVal(*flagInstance, os.Getenv("GITLAB_INSTANCE_URL"), cfg.GitLabInstance, "https://gitlab.com")
	gitlabToken := getVal(*flagToken, os.Getenv("GITLAB_TOKEN"), cfg.GitLabToken, "")
	geminiAPIKey := getVal(*flagAPIKey, os.Getenv("GEMINI_API_KEY"), cfg.GeminiAPIKey, "")
	modelName := getVal(*flagModel, os.Getenv("GEMINI_MODEL"), cfg.DefaultModel, "gemini-2.5-flash")

	// 4. Handle Config Saving Mode
	if *flagSave {
		// Update configuration
		cfg.GitLabInstance = instanceURL
		cfg.GitLabToken = gitlabToken
		cfg.GeminiAPIKey = geminiAPIKey
		cfg.DefaultModel = modelName

		if err := SaveConfig(cfg); err != nil {
			fmt.Fprintf(os.Stderr, "%sFailed to save config: %v%s\n", colorRed, err, colorReset)
			os.Exit(1)
		}
		path, _ := GetConfigPath()
		fmt.Printf("%sSuccess! Configuration saved to %s%s\n", colorGreen, path, colorReset)
		fmt.Printf("Instance: %s\nToken: %s\nAPI Key: %s\nModel: %s\n",
			instanceURL, maskToken(gitlabToken), maskToken(geminiAPIKey), modelName)
		return
	}

	// 5. Validation Check
	if gitlabToken == "" {
		fmt.Fprintf(os.Stderr, "%sError: GitLab token is missing.%s\n", colorRed, colorReset)
		fmt.Fprintln(os.Stderr, "Provide it via -token, the GITLAB_TOKEN environment variable, or configure it with -save.")
		os.Exit(1)
	}

	// 6. Resolve Time Window
	since, until, err := ResolveDateRange(*flagDuration)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError resolving duration: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Printf("%s[1/4] Connecting to GitLab instance %s...%s\n", colorCyan, instanceURL, colorReset)
	glClient := NewGitLabClient(instanceURL, gitlabToken)

	// Fetch User Details to get Email and Name
	glUser, err := glClient.GetAuthenticatedUser()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sFailed to authenticate with GitLab: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}
	fmt.Printf("  Authenticated as: %s%s (%s)%s\n", colorBold, glUser.Name, glUser.Username, colorReset)

	// Resolve email filter
	filterEmail := *flagEmail
	if filterEmail == "" {
		filterEmail = glUser.Email
		if filterEmail == "" && glUser.PublicEmail != "" {
			filterEmail = glUser.PublicEmail
		}
	}
	fmt.Printf("  Filtering commits for author email: %s%s%s\n", colorBold, filterEmail, colorReset)

	// Determine start/end window for display
	fmt.Printf("  Time window: %s%s%s to %s%s%s\n",
		colorBold, since.Format("2006-01-02 15:04"), colorReset,
		colorBold, until.Format("2006-01-02 15:04"), colorReset)

	// 7. Resolve Projects to Scan
	fmt.Printf("%s[2/4] Resolving repositories...%s\n", colorCyan, colorReset)
	var projectsToScan []GitLabProject

	if *flagProjects != "" {
		parts := strings.Split(*flagProjects, ",")
		for _, part := range parts {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			fmt.Printf("  Fetching metadata for project: %s%s%s...\n", colorBold, part, colorReset)
			proj, err := glClient.GetProjectByIDOrPath(part)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %sWarning: failed to fetch project %q: %v%s\n", colorYellow, part, err, colorReset)
				continue
			}
			projectsToScan = append(projectsToScan, *proj)
		}
	} else {
		// Fetch active projects
		fmt.Printf("  Scanning projects with recent activity since %s...\n", since.Format("2006-01-02"))
		projectsToScan, err = glClient.GetActiveProjects(since)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%sFailed to fetch active projects: %v%s\n", colorRed, err, colorReset)
			os.Exit(1)
		}
	}

	fmt.Printf("  Found %s%d%s projects to scan.\n", colorBold, len(projectsToScan), colorReset)

	// 8. Fetch Commits from Projects
	fmt.Printf("%s[3/4] Scraping commit messages...%s\n", colorCyan, colorReset)
	var gatheredCommits []GitLabCommit
	processedCommitsCount := 0

	for _, p := range projectsToScan {
		fmt.Printf("  Scanning %s%s%s...", colorBold, p.PathWithNamespace, colorReset)
		// Fetch all commits in the window (pass filterEmail to filter at API layer for efficiency)
		commits, err := glClient.GetProjectCommits(p.ID, since, until, filterEmail)
		if err != nil {
			fmt.Printf(" %s[Failed to load: %v]%s\n", colorYellow, err, colorReset)
			continue
		}

		projectCommitsCount := 0
		for _, c := range commits {
			processedCommitsCount++

			// local validation check: ignore merge commits
			if len(c.ParentIDs) > 1 {
				continue
			}

			// filter out common noise messages
			msgLower := strings.ToLower(c.Message)
			if strings.HasPrefix(msgLower, "merge branch") ||
				strings.HasPrefix(msgLower, "merge pull request") ||
				strings.HasPrefix(msgLower, "merge tag") ||
				strings.HasPrefix(msgLower, "chore(deps):") ||
				strings.HasPrefix(msgLower, "bump version") {
				continue
			}

			// standard email verification
			if filterEmail != "" &&
				!strings.EqualFold(c.AuthorEmail, filterEmail) &&
				!strings.EqualFold(c.CommitterEmail, filterEmail) {
				continue
			}

			gatheredCommits = append(gatheredCommits, c)
			projectCommitsCount++
		}
		if projectCommitsCount > 0 {
			fmt.Printf(" %sfound %d commit(s)%s\n", colorGreen, projectCommitsCount, colorReset)
		} else {
			fmt.Printf(" %sno commits%s\n", colorDim, colorReset)
		}
	}

	fmt.Printf("  Processed %d total raw commits, filtered down to %s%d%s relevant developer commits.\n",
		processedCommitsCount, colorBold, len(gatheredCommits), colorReset)

	if len(gatheredCommits) == 0 {
		fmt.Printf("\n%sNo commit activities found in the selected time range.%s\n", colorYellow, colorReset)
		return
	}

	// 9. Handle dry run mode
	if *flagDryRun {
		fmt.Printf("\n%s--- Dry Run: Commit List ---%s\n", colorYellow, colorReset)
		for i, c := range gatheredCommits {
			firstLine := strings.Split(c.Message, "\n")[0]
			fmt.Printf("[%s] %s | %s | %s\n",
				c.CommittedDate.Format("2006-01-02"), c.ShortID, c.AuthorName, firstLine)
			if i >= 100 { // limit dry run display
				fmt.Printf("... and %d more commits\n", len(gatheredCommits)-100)
				break
			}
		}
		return
	}

	// 10. Generate Work Logs using Gemini
	if geminiAPIKey == "" {
		fmt.Fprintf(os.Stderr, "\n%sError: Gemini API key is missing.%s\n", colorRed, colorReset)
		fmt.Fprintln(os.Stderr, "Please provide it via the -api-key flag, GEMINI_API_KEY environment variable, or configure it with -save.")
		os.Exit(1)
	}

	fmt.Printf("%s[4/4] Summarizing work log using %s...%s\n", colorCyan, modelName, colorReset)
	summary, err := GenerateWorkLogs(context.Background(), geminiAPIKey, modelName, glUser.Name, filterEmail, gatheredCommits)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError generating work log: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	// Output final logs
	fmt.Printf("\n%s=== Synthesized Work Log ===%s\n\n", colorBold+colorGreen, colorReset)
	fmt.Println(summary)
	fmt.Println()
}

// Helpers
func getVal(cliFlag, envVal, cfgVal, defaultVal string) string {
	if cliFlag != "" {
		return cliFlag
	}
	if envVal != "" {
		return envVal
	}
	if cfgVal != "" {
		return cfgVal
	}
	return defaultVal
}

func maskToken(t string) string {
	if len(t) <= 8 {
		return "****"
	}
	return t[:4] + "...." + t[len(t)-4:]
}
