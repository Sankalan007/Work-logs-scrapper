# GitLab Work Logs Scraper CLI 🚀

A robust, premium Go-based CLI tool that scrapes your GitLab commit history and synthesizes it into a professional, high-quality **3-6 bullet point work log summary** using the official Google GenAI (Gemini) SDK.

No more manually digging through commits to write status updates, daily stand-up logs, or weekly performance reviews!

---

## Key Features ✨

- **Intelligent Duration Presets**: Quickly fetch history for the last `1d`, `3d`, `7d`, `1m`, `3m`, `12m`, or specify a custom date range (`YYYY-MM-DD:YYYY-MM-DD`).
- **Smart Noise Filtration**: Automatically filters out merge commits, branch creations, and chore/dependency commits to keep the AI input context clean.
- **Official Google GenAI SDK**: Built using the modern, unified `google.golang.org/genai` Go SDK, defaulting to the highly efficient `gemini-2.5-flash` model.
- **Flexible Credential Caching**: Save your credentials (instance URL, token, and API key) securely to a local JSON config file (`~/.gitlab-worklogs.json`) via a simple command-line flag.
- **Dry Run Mode**: Preview the list of fetched and filtered commits locally in your terminal *before* sending them to the Gemini API.
- **Premium CLI Output**: Features clean ANSI-colored status feedback and beautifully structured markdown logs ready to copy-paste.

---

## Prerequisites 🛠️

1. **Go Compiler**: Go 1.21 or higher installed on your system.
2. **GitLab Personal Access Token (PAT)**:
   - Create one at your GitLab instance under **Preferences > Personal access tokens**.
   - Required scope: **`read_api`** (or **`api`**).
3. **Google Gemini API Key**:
   - Obtain one for free or pay-as-you-go from Google AI Studio.

---

## Installation & Build 📦

Clone the repository and build the binary:

```bash
# Clone the repository
git clone https://github.com/Sankalan007/Work-logs-scrapper.git
cd "work logs scrapper"

# Build the executable
go build -o gitlab-worklogs
```

---

## One-Time Setup ⚙️

Save your configuration locally (stored under `~/.gitlab-worklogs.json`):

```bash
./gitlab-worklogs -save \
  -instance "https://gitlab.yourcompany.com" \
  -token "glpat-YOUR_GITLAB_TOKEN" \
  -api-key "AIzaSyYOUR_GEMINI_API_KEY"
```

Once saved, you can run the tool without passing these flags in subsequent executions.

---

## Usage Guide 📖

### 1. Basic Generation (Last 7 Days)
Run the tool directly to generate a summary of your commits for the default duration (7 days):
```bash
./gitlab-worklogs
```

### 2. Relative Durations
Supported presets: `1d`, `3d`, `7d`, `1m`, `3m`, `12m`:
```bash
./gitlab-worklogs -duration 3d
```

### 3. Custom Date Range
Specify exact start and end dates (`YYYY-MM-DD:YYYY-MM-DD` or just `YYYY-MM-DD` to start from that date to current time):
```bash
./gitlab-worklogs -duration 2026-06-01:2026-06-05
```

### 4. Restrict to Specific Projects
Filter your commit search to one or more specific GitLab projects by passing their IDs or path namespaces:
```bash
./gitlab-worklogs -projects "my-group/my-project,123456"
```

### 5. Dry-Run Verification
Verify what commits are collected without consuming Gemini API tokens:
```bash
./gitlab-worklogs -duration 3d -dry-run
```

---

## Command Flags Reference 🎛️

| Flag | Type | Description | Default |
| :--- | :--- | :--- | :--- |
| `-save` | `bool` | Save credentials to `~/.gitlab-worklogs.json` and exit | `false` |
| `-instance` | `string` | Target GitLab instance URL | `https://gitlab.com` |
| `-token` | `string` | GitLab Personal Access Token (PAT) | (Uses env `GITLAB_TOKEN` or Config) |
| `-api-key` | `string` | Google GenAI API Key | (Uses env `GEMINI_API_KEY` or Config) |
| `-duration` | `string` | Time duration range preset or exact bounds (`YYYY-MM-DD[:YYYY-MM-DD]`) | `7d` |
| `-projects` | `string` | Comma-separated list of GitLab project IDs or path namespaces to scan | (Scan all active) |
| `-email` | `string` | Override author email (defaults to your authenticated GitLab email) | (Auto-detected) |
| `-model` | `string` | Google GenAI model to invoke for synthesis | `gemini-2.5-flash` |
| `-dry-run` | `bool` | Print commits fetched locally from GitLab without invoking Gemini | `false` |

---

## Example Output 📝

```markdown
[1/4] Connecting to GitLab instance https://gitlab.com...
  Authenticated as: Sankalan Chanda (schanda)
  Filtering commits for author email: schanda@argusoft.com
  Time window: 2026-06-05 12:00 to 2026-06-12 12:00
[2/4] Resolving repositories...
  Scanning projects with recent activity since 2026-06-05...
  Found 3 projects to scan.
[3/4] Scraping commit messages...
  Scanning my-group/main-app... found 12 commit(s)
  Scanning my-group/utility-lib... no commits
  Processed 18 total raw commits, filtered down to 12 relevant developer commits.
[4/4] Summarizing work log using gemini-2.5-flash...

=== Synthesized Work Log ===

* Developed initial draft for call simulcast functionality.
* Implemented ICE restart and transport recreation flow to enhance call stability.
* Optimized feedback services, including performance improvements for concurrent Redis transactions.
* Refactored feedback Excel builder logic to execute server-side, reducing client-side overhead.
* Enhanced feedback status management with database-level transition rules and Kafka event payloads.
```

---

## License 📄

This project is licensed under the [MIT License](LICENSE).
