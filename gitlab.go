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
	ID                int       `json:"id"`
	Name              string    `json:"name"`
	PathWithNamespace string    `json:"path_with_namespace"`
	LastActivityAt    time.Time `json:"last_activity_at"`
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

// GitLabMR represents a merge request from the GitLab API.
type GitLabMR struct {
	IID        int          `json:"iid"`
	ProjectID  int          `json:"project_id"`
	Title      string       `json:"title"`
	WebURL     string       `json:"web_url"`
	State      string       `json:"state"`
	UpdatedAt  time.Time    `json:"updated_at"`
	Author     GitLabUser   `json:"author"`
	Assignees  []GitLabUser `json:"assignees"`
	Reviewers  []GitLabUser `json:"reviewers"`
	References struct {
		Full string `json:"full"`
	} `json:"references"`
}

// MRComment is a single comment the user left on a merge request.
type MRComment struct {
	Date time.Time
	Body string
}

// MRInvolvement captures how the user engaged with a single merge request.
type MRInvolvement struct {
	IID        int
	Title      string
	Ref        string // e.g. "group/project!123" (falls back to web URL)
	IsAssignee bool
	IsReviewer bool
	Comments   []MRComment
	LastDate   time.Time // attribution date when there are no comments
}

// containsUserID reports whether the given user ID is present in the list.
func containsUserID(users []GitLabUser, id int) bool {
	for _, u := range users {
		if u.ID == id {
			return true
		}
	}
	return false
}

// ListMergeRequests returns merge requests filtered by a username field
// (filterKey is "assignee_username" or "reviewer_username") updated within the window.
func (c *GitLabClient) ListMergeRequests(filterKey, username string, since, until time.Time) ([]GitLabMR, error) {
	var all []GitLabMR
	page := 1
	perPage := 100

	for {
		params := url.Values{}
		params.Set("scope", "all")
		params.Set(filterKey, username)
		params.Set("updated_after", since.Format(time.RFC3339))
		params.Set("updated_before", until.Format(time.RFC3339))
		params.Set("order_by", "updated_at")
		params.Set("sort", "desc")
		params.Set("per_page", strconv.Itoa(perPage))
		params.Set("page", strconv.Itoa(page))

		body, header, err := c.executeRequest("GET", "merge_requests", params)
		if err != nil {
			return nil, err
		}

		var mrs []GitLabMR
		if err := json.Unmarshal(body, &mrs); err != nil {
			return nil, err
		}
		if len(mrs) == 0 {
			break
		}
		all = append(all, mrs...)

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
	return all, nil
}

// GetMergeRequest fetches a single merge request's detail.
func (c *GitLabClient) GetMergeRequest(projectID, iid int) (*GitLabMR, error) {
	path := fmt.Sprintf("projects/%d/merge_requests/%d", projectID, iid)
	body, _, err := c.executeRequest("GET", path, nil)
	if err != nil {
		return nil, err
	}
	var mr GitLabMR
	if err := json.Unmarshal(body, &mr); err != nil {
		return nil, err
	}
	return &mr, nil
}

// mrCommentEvent is a comment the user made on a merge request, from the events API.
type mrCommentEvent struct {
	ProjectID int
	IID       int
	Date      time.Time
	Body      string
}

// gitlabEvent mirrors the relevant fields of a GitLab event object.
type gitlabEvent struct {
	ActionName string    `json:"action_name"`
	CreatedAt  time.Time `json:"created_at"`
	ProjectID  int       `json:"project_id"`
	Note       *struct {
		Body         string `json:"body"`
		NoteableType string `json:"noteable_type"`
		NoteableIID  int    `json:"noteable_iid"`
	} `json:"note"`
}

// GetMyMRCommentEvents returns the authenticated user's MR comments within the window.
func (c *GitLabClient) GetMyMRCommentEvents(since, until time.Time) ([]mrCommentEvent, error) {
	var out []mrCommentEvent
	page := 1
	perPage := 100

	for {
		params := url.Values{}
		params.Set("action", "commented")
		// `after`/`before` are date-only and exclusive; widen by a day and filter precisely below.
		params.Set("after", since.AddDate(0, 0, -1).Format("2006-01-02"))
		params.Set("before", until.AddDate(0, 0, 1).Format("2006-01-02"))
		params.Set("per_page", strconv.Itoa(perPage))
		params.Set("page", strconv.Itoa(page))

		body, header, err := c.executeRequest("GET", "events", params)
		if err != nil {
			return nil, err
		}

		var events []gitlabEvent
		if err := json.Unmarshal(body, &events); err != nil {
			return nil, err
		}
		if len(events) == 0 {
			break
		}

		for _, ev := range events {
			if ev.Note == nil || ev.Note.NoteableType != "MergeRequest" {
				continue
			}
			if ev.CreatedAt.Before(since) || ev.CreatedAt.After(until) {
				continue
			}
			out = append(out, mrCommentEvent{
				ProjectID: ev.ProjectID,
				IID:       ev.Note.NoteableIID,
				Date:      ev.CreatedAt,
				Body:      ev.Note.Body,
			})
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
	return out, nil
}

// GetMRInvolvement assembles all merge requests the user engaged with in the window:
// MRs they are assignee/reviewer of (updated in window) plus MRs they commented on.
func (c *GitLabClient) GetMRInvolvement(userID int, username string, since, until time.Time) ([]MRInvolvement, error) {
	records := make(map[string]*MRInvolvement)
	key := func(projectID, iid int) string { return fmt.Sprintf("%d/%d", projectID, iid) }

	apply := func(mr GitLabMR) *MRInvolvement {
		// Authoring an MR is "my own work" (captured via commits); never treat the
		// author as a tester/reviewer of their own MR, even if they commented.
		if mr.Author.ID == userID {
			return nil
		}
		k := key(mr.ProjectID, mr.IID)
		rec := records[k]
		if rec == nil {
			ref := mr.References.Full
			if ref == "" {
				ref = mr.WebURL
			}
			rec = &MRInvolvement{IID: mr.IID, Title: mr.Title, Ref: ref, LastDate: mr.UpdatedAt}
			records[k] = rec
		}
		rec.IsAssignee = rec.IsAssignee || containsUserID(mr.Assignees, userID)
		rec.IsReviewer = rec.IsReviewer || containsUserID(mr.Reviewers, userID)
		return rec
	}

	assigned, err := c.ListMergeRequests("assignee_username", username, since, until)
	if err != nil {
		return nil, fmt.Errorf("failed to list assigned MRs: %w", err)
	}
	for _, mr := range assigned {
		apply(mr)
	}

	reviewing, err := c.ListMergeRequests("reviewer_username", username, since, until)
	if err != nil {
		return nil, fmt.Errorf("failed to list reviewer MRs: %w", err)
	}
	for _, mr := range reviewing {
		apply(mr)
	}

	comments, err := c.GetMyMRCommentEvents(since, until)
	if err != nil {
		return nil, fmt.Errorf("failed to list MR comment events: %w", err)
	}
	for _, cm := range comments {
		k := key(cm.ProjectID, cm.IID)
		rec := records[k]
		if rec == nil {
			// MR not seen via assignee/reviewer lists: fetch detail to resolve title and roles.
			detail, derr := c.GetMergeRequest(cm.ProjectID, cm.IID)
			if derr != nil {
				continue // skip MRs we cannot resolve
			}
			rec = apply(*detail)
			if rec == nil {
				continue // I authored this MR; my work is captured via commits
			}
		}
		rec.Comments = append(rec.Comments, MRComment{Date: cm.Date, Body: cm.Body})
	}

	out := make([]MRInvolvement, 0, len(records))
	for _, rec := range records {
		out = append(out, *rec)
	}
	return out, nil
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
