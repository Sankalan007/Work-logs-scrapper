package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"
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
	flagDuration := flag.String("duration", "1d", "Duration for work logs (1d, 3d, 7d, 1m, 3m, 12m or YYYY-MM-DD[:YYYY-MM-DD])")
	flagProjects := flag.String("projects", "", "Comma-separated list of specific project IDs or paths (restricts scraping)")
	flagEmail := flag.String("email", "", "Override email to filter commits (defaults to autodetected user email)")
	flagModel := flag.String("model", "gemini-2.5-flash", "Gemini model to use")
	flagSave := flag.Bool("save", false, "Save provided instance, token, and API key to ~/.gitlab-worklogs.json and exit")
	flagDryRun := flag.Bool("dry-run", false, "Dry run: print commits fetched from GitLab without calling Gemini")
	flagSheet := flag.String("sheet", "", "Google Sheets URL (or ID) to auto-fill, one row per date of the current month up to today")
	flagDateCol := flag.String("date-col", "Date", "Header name of the date column to read in the sheet")
	flagLogCol := flag.String("log-col", "", "Header name of the column to fill with generated work logs in the sheet")
	flagGoogleCreds := flag.String("google-creds", defaultGoogleCredsPath(), "Path to Google OAuth client-credentials JSON (Desktop app)")
	flagOverall := flag.Bool("overall", false, "Produce a single aggregate summary instead of a per-date breakdown")

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
		fmt.Println("\n  # Generate work log for last 1 day (default):")
		fmt.Println("  gitlab-worklogs")
		fmt.Println("\n  # Auto-fill a Google Sheet (one row per date this month, up to today):")
		fmt.Println("  gitlab-worklogs -sheet https://docs.google.com/spreadsheets/d/<ID>/edit -date-col Date -log-col \"Work Log\"")
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
	sheetMode := *flagSheet != ""
	var since, until time.Time
	if sheetMode {
		// Sheet mode ignores -duration: scan from the 1st of the current month through now.
		if *flagLogCol == "" {
			fmt.Fprintf(os.Stderr, "%sError: -log-col is required when using -sheet.%s\n", colorRed, colorReset)
			os.Exit(1)
		}
		now := time.Now()
		since = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		until = now
	} else {
		var derr error
		since, until, derr = ResolveDateRange(*flagDuration)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "%sError resolving duration: %v%s\n", colorRed, derr, colorReset)
			os.Exit(1)
		}
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

	// 8.5 Fetch merge-request activity (assignee / reviewer / comments) in the window.
	fmt.Printf("%s  Fetching merge request activity...%s", colorDim, colorReset)
	mrInvolvements, err := glClient.GetMRInvolvement(glUser.ID, glUser.Username, since, until)
	if err != nil {
		fmt.Printf(" %s[failed: %v]%s\n", colorYellow, err, colorReset)
		mrInvolvements = nil
	} else {
		fmt.Printf(" %sfound %d MR(s)%s\n", colorGreen, len(mrInvolvements), colorReset)
	}

	if len(gatheredCommits) == 0 && len(mrInvolvements) == 0 {
		fmt.Printf("\n%sNo commit or merge request activity found in the selected time range.%s\n", colorYellow, colorReset)
		return
	}

	// 8.6 Sheet mode: fill one row per date of the current month (up to today).
	if sheetMode {
		if geminiAPIKey == "" {
			fmt.Fprintf(os.Stderr, "\n%sError: Gemini API key is missing.%s\n", colorRed, colorReset)
			os.Exit(1)
		}
		runSheetFill(geminiAPIKey, modelName, glUser.Name, filterEmail, gatheredCommits, mrInvolvements,
			*flagGoogleCreds, *flagSheet, *flagDateCol, *flagLogCol)
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
		fmt.Printf("\n%s--- Dry Run: MR Activity ---%s\n", colorYellow, colorReset)
		for _, mr := range mrInvolvements {
			fmt.Printf("MR %d:%s | assignee=%v reviewer=%v comments=%d\n",
				mr.IID, mr.Title, mr.IsAssignee, mr.IsReviewer, len(mr.Comments))
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
	summary, err := GenerateWorkLogs(context.Background(), geminiAPIKey, modelName, glUser.Name, filterEmail,
		gatheredCommits, mrInvolvements, since, until, !*flagOverall)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sError generating work log: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	// Output final logs
	fmt.Printf("\n%s=== Synthesized Work Log ===%s\n\n", colorBold+colorGreen, colorReset)
	fmt.Println(summary)
	fmt.Println()
}

// runSheetFill buckets commits and MR activity by day and writes a per-day work log into
// the named sheet column for every row whose date falls in the current month, up to today.
func runSheetFill(apiKey, modelName, userName, filterEmail string, commits []GitLabCommit, mrs []MRInvolvement,
	credsPath, sheetLink, dateCol, logCol string) {

	// Bucket commits by their local committed date (YYYY-MM-DD).
	buckets := make(map[string][]GitLabCommit)
	for _, c := range commits {
		key := c.CommittedDate.Local().Format("2006-01-02")
		buckets[key] = append(buckets[key], c)
	}

	// Bucket MR involvement by day: comment-bearing MRs land on each comment's date;
	// assignee/reviewer-only MRs land on their last-updated date.
	mrBuckets := make(map[string][]MRInvolvement)
	for _, mr := range mrs {
		if len(mr.Comments) == 0 {
			key := mr.LastDate.Local().Format("2006-01-02")
			mrBuckets[key] = append(mrBuckets[key], mr)
			continue
		}
		perDay := make(map[string][]MRComment)
		for _, cm := range mr.Comments {
			key := cm.Date.Local().Format("2006-01-02")
			perDay[key] = append(perDay[key], cm)
		}
		for key, cms := range perDay {
			day := mr
			day.Comments = cms
			mrBuckets[key] = append(mrBuckets[key], day)
		}
	}

	now := time.Now()
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())

	ctx := context.Background()

	fmt.Printf("%s[Sheet] Authorizing with Google...%s\n", colorCyan, colorReset)
	sc, err := NewSheetClient(ctx, credsPath, sheetLink)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sFailed to open Google Sheet: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	fmt.Printf("%s[Sheet] Reading date column %q...%s\n", colorCyan, dateCol, colorReset)
	rows, logColIdx, err := sc.ReadDateColumn(dateCol, logCol)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%sFailed to read sheet: %v%s\n", colorRed, err, colorReset)
		os.Exit(1)
	}

	written, skipped := 0, 0
	for _, r := range rows {
		d := time.Date(r.Date.Year(), r.Date.Month(), r.Date.Day(), 0, 0, 0, 0, now.Location())

		// Only fill dates in the current month, up to and including today.
		if d.Before(monthStart) || d.After(today) {
			continue
		}

		dayKey := d.Format("2006-01-02")
		dayCommits := buckets[dayKey]
		dayMRs := mrBuckets[dayKey]
		if len(dayCommits) == 0 && len(dayMRs) == 0 {
			fmt.Printf("  %s%s: no activity, skipping%s\n", colorDim, dayKey, colorReset)
			skipped++
			continue
		}

		dayStart := d
		dayEnd := time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, now.Location())
		summary, err := GenerateWorkLogs(ctx, apiKey, modelName, userName, filterEmail,
			dayCommits, dayMRs, dayStart, dayEnd, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s%s: generation failed: %v%s\n", colorYellow, dayKey, err, colorReset)
			skipped++
			continue
		}

		if err := sc.WriteLog(r.RowNumber, logColIdx, summary); err != nil {
			fmt.Fprintf(os.Stderr, "  %s%s: write failed: %v%s\n", colorYellow, dayKey, err, colorReset)
			skipped++
			continue
		}
		fmt.Printf("  %s%s%s -> row %d (%d commit(s), %d MR(s))\n", colorGreen, dayKey, colorReset, r.RowNumber, len(dayCommits), len(dayMRs))
		written++
	}

	fmt.Printf("\n%sDone. Wrote %d row(s), skipped %d.%s\n", colorBold+colorGreen, written, skipped, colorReset)
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
