package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitLabUser represents GitLab user details.
type GitLabUser struct {
	ID          int    `json:"id"`
	Username    string `json:"username"`
	Name        string `json:"name"`
	Email       string `json:"email"`
	PublicEmail string `json:"public_email"`
}

// GitLabProject represents a simplified GitLab project structure.
type GitLabProject struct {
	ID             int       `json:"id"`
	Name           string    `json:"name"`
	PathWithNamespace string `json:"path_with_namespace"`
	LastActivityAt time.Time `json:"last_activity_at"`
}

// GitLabCommit represents a commit from the GitLab repository commits API.
type GitLabCommit struct {
	ID             string    `json:"id"`
	ShortID        string    `json:"short_id"`
	Title          string    `json:"title"`
	AuthorName     string    `json:"author_name"`
	AuthorEmail    string    `json:"author_email"`
	AuthoredDate   time.Time `json:"authored_date"`
	CommitterName  string    `json:"committer_name"`
	CommitterEmail string    `json:"committer_email"`
	CommittedDate  time.Time `json:"committed_date"`
	Message        string    `json:"message"`
	ParentIDs      []string  `json:"parent_ids"`
}

// GitLabClient handles API communications.
type GitLabClient struct {
	BaseURL string
	Token   string
	Client  *http.Client
}

// NewGitLabClient creates a new GitLab Client with normalized URL.
func NewGitLabClient(baseURL, token string) *GitLabClient {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = "https://gitlab.com"
	}
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "https://" + baseURL
	}
	baseURL = strings.TrimSuffix(baseURL, "/")

	return &GitLabClient{
		BaseURL: baseURL,
		Token:   token,
		Client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// executeRequest makes the HTTP request with the necessary auth header.
func (c *GitLabClient) executeRequest(method, path string, queryParams url.Values) ([]byte, http.Header, error) {
	fullURL := fmt.Sprintf("%s/api/v4/%s", c.BaseURL, strings.TrimPrefix(path, "/"))
	if len(queryParams) > 0 {
		fullURL = fmt.Sprintf("%s?%s", fullURL, queryParams.Encode())
	}

	req, err := http.NewRequest(method, fullURL, nil)
	if err != nil {
		return nil, nil, err
	}

	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, fmt.Errorf("GitLab API error: status=%d message=%s", resp.StatusCode, string(body))
	}

	return body, resp.Header, nil
}

// GetAuthenticatedUser retrieves the current user's profile.
func (c *GitLabClient) GetAuthenticatedUser() (*GitLabUser, error) {
	body, _, err := c.executeRequest("GET", "user", nil)
	if err != nil {
		return nil, err
	}

	var user GitLabUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, err
	}
	return &user, nil
}

// GetProjectByIDOrPath retrieves a single project by ID or URL-encoded path.
func (c *GitLabClient) GetProjectByIDOrPath(idOrPath string) (*GitLabProject, error) {
	escapedPath := url.PathEscape(idOrPath)
	// GitLab expects path with namespace to be URL-escaped e.g. "group/project" -> "group%2Fproject"
	// But it handles slashes if we double-escape or use URL-escape.
	// We'll replace "/" with "%2F" manually to be certain.
	escapedPath = strings.ReplaceAll(escapedPath, "/", "%2F")
	path := fmt.Sprintf("projects/%s", escapedPath)

	body, _, err := c.executeRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}

	var proj GitLabProject
	if err := json.Unmarshal(body, &proj); err != nil {
		return nil, err
	}
	return &proj, nil
}

// GetActiveProjects lists projects with membership, sorted by last activity.
// It stops paginating when last_activity_at is older than the `since` time.
func (c *GitLabClient) GetActiveProjects(since time.Time) ([]GitLabProject, error) {
	var activeProjects []GitLabProject
	page := 1
	perPage := 100

	for {
		params := url.Values{}
		params.Set("membership", "true")
		params.Set("simple", "true")
		params.Set("order_by", "last_activity_at")
		params.Set("sort", "desc")
		params.Set("per_page", strconv.Itoa(perPage))
		params.Set("page", strconv.Itoa(page))

		body, header, err := c.executeRequest("GET", "projects", params)
		if err != nil {
			return nil, err
		}

		var projects []GitLabProject
		if err := json.Unmarshal(body, &projects); err != nil {
			return nil, err
		}

		if len(projects) == 0 {
			break
		}

		reachedEndOfWindow := false
		for _, p := range projects {
			// If last activity is older than since, we don't need to inspect this project or subsequent ones
			if p.LastActivityAt.Before(since) {
				reachedEndOfWindow = true
				break
			}
			activeProjects = append(activeProjects, p)
		}

		if reachedEndOfWindow {
			break
		}

		nextPageStr := header.Get("X-Next-Page")
		if nextPageStr == "" {
			break
		}

		nextPage, err := strconv.Atoi(nextPageStr)
		if err != nil || nextPage == 0 {
			break
		}
		page = nextPage
	}

	return activeProjects, nil
}

// GetProjectCommits fetches commits for a specific project within the given time range.
func (c *GitLabClient) GetProjectCommits(projectID int, since, until time.Time, authorQuery string) ([]GitLabCommit, error) {
	var allCommits []GitLabCommit
	page := 1
	perPage := 100

	for {
		params := url.Values{}
		params.Set("since", since.Format(time.RFC3339))
		params.Set("until", until.Format(time.RFC3339))
		params.Set("all", "true") // Checks all branches
		params.Set("per_page", strconv.Itoa(perPage))
		params.Set("page", strconv.Itoa(page))

		if authorQuery != "" {
			params.Set("author", authorQuery)
		}

		path := fmt.Sprintf("projects/%d/repository/commits", projectID)
		body, header, err := c.executeRequest("GET", path, params)
		if err != nil {
			// It is common for some repositories to be empty or not initialized.
			// In that case, GitLab might return 404 or empty. We ignore it gracefully.
			if strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "empty") {
				return nil, nil
			}
			return nil, err
		}

		var commits []GitLabCommit
		if err := json.Unmarshal(body, &commits); err != nil {
			return nil, err
		}

		if len(commits) == 0 {
			break
		}

		allCommits = append(allCommits, commits...)

		nextPageStr := header.Get("X-Next-Page")
		if nextPageStr == "" {
			break
		}

		nextPage, err := strconv.Atoi(nextPageStr)
		if err != nil || nextPage == 0 {
			break
		}
		page = nextPage
	}

	return allCommits, nil
}

// ResolveDateRange converts a duration string into start and end time.Time.
func ResolveDateRange(durationStr string) (time.Time, time.Time, error) {
	now := time.Now()
	until := now
	var since time.Time

	durationStr = strings.TrimSpace(strings.ToLower(durationStr))

	switch durationStr {
	case "1d":
		since = now.AddDate(0, 0, -1)
	case "3d":
		since = now.AddDate(0, 0, -3)
	case "7d":
		since = now.AddDate(0, 0, -7)
	case "1m":
		since = now.AddDate(0, -1, 0)
	case "3m":
		since = now.AddDate(0, -3, 0)
	case "12m":
		since = now.AddDate(0, -12, 0)
	default:
		if strings.Contains(durationStr, ":") {
			parts := strings.Split(durationStr, ":")
			if len(parts) != 2 {
				return since, until, fmt.Errorf("invalid custom date range format, expected YYYY-MM-DD:YYYY-MM-DD")
			}
			s, err := time.Parse("2006-01-02", strings.TrimSpace(parts[0]))
			if err != nil {
				return since, until, fmt.Errorf("invalid start date format (use YYYY-MM-DD): %w", err)
			}
			e, err := time.Parse("2006-01-02", strings.TrimSpace(parts[1]))
			if err != nil {
				return since, until, fmt.Errorf("invalid end date format (use YYYY-MM-DD): %w", err)
			}
			// End date should cover the entire day
			e = time.Date(e.Year(), e.Month(), e.Day(), 23, 59, 59, 999999999, e.Location())
			return s, e, nil
		} else {
			// Check if it's a single date (YYYY-MM-DD)
			s, err := time.Parse("2006-01-02", durationStr)
			if err != nil {
				return since, until, fmt.Errorf("invalid duration/date format (expected preset like 1d, 3d, 7d, 1m, 3m, 12m or YYYY-MM-DD[:YYYY-MM-DD])")
			}
			return s, until, nil
		}
	}

	return since, until, nil
}
