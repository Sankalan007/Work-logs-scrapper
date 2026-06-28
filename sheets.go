package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
	"google.golang.org/api/sheets/v4"
)

// sheetRow associates a 1-based spreadsheet row number with its parsed date.
type sheetRow struct {
	RowNumber int
	Date      time.Time
}

// SheetClient wraps the Google Sheets API service and the target spreadsheet.
type SheetClient struct {
	svc           *sheets.Service
	spreadsheetID string
	sheetTitle    string
}

var sheetIDRe = regexp.MustCompile(`/spreadsheets/d/([a-zA-Z0-9-_]+)`)

// ParseSheetID extracts the spreadsheet ID from a full Google Sheets URL or a bare ID.
func ParseSheetID(link string) (string, error) {
	link = strings.TrimSpace(link)
	if m := sheetIDRe.FindStringSubmatch(link); m != nil {
		return m[1], nil
	}
	// Assume the caller passed a bare ID (no slashes).
	if link != "" && !strings.Contains(link, "/") {
		return link, nil
	}
	return "", fmt.Errorf("could not extract spreadsheet ID from %q", link)
}

// googleTokenPath returns the cache path for the OAuth token.
func googleTokenPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".gitlab-worklogs-google-token.json"), nil
}

// defaultGoogleCredsPath returns the default OAuth client-credentials path.
func defaultGoogleCredsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".gitlab-worklogs-google.json"
	}
	return filepath.Join(home, ".gitlab-worklogs-google.json")
}

// NewSheetClient performs OAuth (cached after first run) and returns a ready Sheets client.
func NewSheetClient(ctx context.Context, credsPath, sheetLink string) (*SheetClient, error) {
	id, err := ParseSheetID(sheetLink)
	if err != nil {
		return nil, err
	}

	credBytes, err := os.ReadFile(credsPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read Google OAuth credentials at %s: %w\n"+
			"Create an OAuth client (type 'Desktop app') in Google Cloud Console, enable the Google Sheets API, "+
			"download the JSON, and place it there (or pass -google-creds).", credsPath, err)
	}

	config, err := google.ConfigFromJSON(credBytes, sheets.SpreadsheetsScope)
	if err != nil {
		return nil, fmt.Errorf("invalid Google OAuth credentials JSON: %w", err)
	}

	client, err := oauthClient(ctx, config)
	if err != nil {
		return nil, err
	}

	svc, err := sheets.NewService(ctx, option.WithHTTPClient(client))
	if err != nil {
		return nil, fmt.Errorf("failed to create Sheets service: %w", err)
	}

	title, err := firstSheetTitle(svc, id)
	if err != nil {
		return nil, err
	}

	return &SheetClient{svc: svc, spreadsheetID: id, sheetTitle: title}, nil
}

// oauthClient returns an HTTP client authorized with a cached or freshly-fetched token.
func oauthClient(ctx context.Context, config *oauth2.Config) (*http.Client, error) {
	tokPath, err := googleTokenPath()
	if err != nil {
		return nil, err
	}

	tok, err := tokenFromFile(tokPath)
	if err != nil {
		tok, err = tokenFromWeb(ctx, config)
		if err != nil {
			return nil, err
		}
		if err := saveToken(tokPath, tok); err != nil {
			fmt.Fprintf(os.Stderr, "%sWarning: failed to cache Google token: %v%s\n", colorYellow, err, colorReset)
		}
	}
	return config.Client(ctx, tok), nil
}

func tokenFromFile(path string) (*oauth2.Token, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	if err := json.NewDecoder(f).Decode(tok); err != nil {
		return nil, err
	}
	return tok, nil
}

func saveToken(path string, tok *oauth2.Token) error {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(tok)
}

// tokenFromWeb runs a loopback OAuth flow: opens the browser, waits for the callback.
func tokenFromWeb(ctx context.Context, config *oauth2.Config) (*oauth2.Token, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("failed to start local OAuth callback server: %w", err)
	}
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	config.RedirectURL = fmt.Sprintf("http://127.0.0.1:%d/", port)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if e := r.URL.Query().Get("error"); e != "" {
				errCh <- fmt.Errorf("authorization denied: %s", e)
				fmt.Fprintln(w, "Authorization failed. You may close this tab.")
				return
			}
			code := r.URL.Query().Get("code")
			if code == "" {
				return
			}
			fmt.Fprintln(w, "Authorization complete. You may close this tab and return to the terminal.")
			codeCh <- code
		}),
	}
	go srv.Serve(listener)
	defer srv.Shutdown(context.Background())

	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline, oauth2.ApprovalForce)
	fmt.Printf("%sOpening browser for Google authorization...%s\n", colorCyan, colorReset)
	fmt.Printf("  If it does not open, visit:\n  %s\n", authURL)
	openBrowser(authURL)

	select {
	case code := <-codeCh:
		tok, err := config.Exchange(ctx, code)
		if err != nil {
			return nil, fmt.Errorf("failed to exchange auth code for token: %w", err)
		}
		return tok, nil
	case err := <-errCh:
		return nil, err
	case <-time.After(3 * time.Minute):
		return nil, fmt.Errorf("timed out waiting for Google authorization")
	}
}

func openBrowser(url string) {
	var cmd string
	var args []string
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd, args = "rundll32", []string{"url.dll,FileProtocolHandler"}
	default:
		cmd = "xdg-open"
	}
	args = append(args, url)
	_ = exec.Command(cmd, args...).Start()
}

// firstSheetTitle returns the title of the first sheet/tab in the spreadsheet.
func firstSheetTitle(svc *sheets.Service, spreadsheetID string) (string, error) {
	ss, err := svc.Spreadsheets.Get(spreadsheetID).Fields("sheets.properties.title").Do()
	if err != nil {
		return "", fmt.Errorf("failed to open spreadsheet (is it shared with your Google account?): %w", err)
	}
	if len(ss.Sheets) == 0 {
		return "", fmt.Errorf("spreadsheet has no sheets")
	}
	return ss.Sheets[0].Properties.Title, nil
}

// ReadDateColumn reads the header row, locates the date and log columns by name,
// and returns the parsed dates per row plus the column index to write logs into.
func (c *SheetClient) ReadDateColumn(dateCol, logCol string) (rows []sheetRow, logColIdx int, err error) {
	readRange := fmt.Sprintf("'%s'!A1:ZZ", c.sheetTitle)
	resp, err := c.svc.Spreadsheets.Values.Get(c.spreadsheetID, readRange).
		ValueRenderOption("FORMATTED_VALUE").
		DateTimeRenderOption("FORMATTED_STRING").
		Do()
	if err != nil {
		return nil, -1, fmt.Errorf("failed to read sheet values: %w", err)
	}
	if len(resp.Values) == 0 {
		return nil, -1, fmt.Errorf("sheet appears empty (no header row found)")
	}

	header := resp.Values[0]
	dateIdx, logIdx := -1, -1
	for i, cell := range header {
		name := strings.TrimSpace(fmt.Sprintf("%v", cell))
		if strings.EqualFold(name, dateCol) {
			dateIdx = i
		}
		if strings.EqualFold(name, logCol) {
			logIdx = i
		}
	}
	if dateIdx == -1 {
		return nil, -1, fmt.Errorf("date column %q not found in header row", dateCol)
	}
	if logIdx == -1 {
		return nil, -1, fmt.Errorf("log column %q not found in header row", logCol)
	}

	for r := 1; r < len(resp.Values); r++ {
		row := resp.Values[r]
		if dateIdx >= len(row) {
			continue
		}
		raw := strings.TrimSpace(fmt.Sprintf("%v", row[dateIdx]))
		if raw == "" {
			continue
		}
		d, ok := parseSheetDate(raw)
		if !ok {
			continue
		}
		rows = append(rows, sheetRow{RowNumber: r + 1, Date: d})
	}
	return rows, logIdx, nil
}

// parseSheetDate tries a range of common spreadsheet date layouts.
func parseSheetDate(s string) (time.Time, bool) {
	layouts := []string{
		"2006-01-02", "2006/01/02",
		"01/02/2006", "02/01/2006",
		"1/2/2006", "2/1/2006",
		"Jan 2, 2006", "2 Jan 2006", "January 2, 2006",
		"02-01-2006", "01-02-2006",
		"2006-01-02 15:04:05",
	}
	for _, l := range layouts {
		if t, err := time.Parse(l, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// WriteLog writes a single value into the log column at the given row number.
func (c *SheetClient) WriteLog(rowNumber, logColIdx int, value string) error {
	cell := fmt.Sprintf("'%s'!%s%d", c.sheetTitle, colLetter(logColIdx), rowNumber)
	_, err := c.svc.Spreadsheets.Values.Update(c.spreadsheetID, cell, &sheets.ValueRange{
		Values: [][]interface{}{{value}},
	}).ValueInputOption("RAW").Do()
	return err
}

// colLetter converts a 0-based column index into a spreadsheet column letter (A, B, ... AA).
func colLetter(idx int) string {
	letters := ""
	for idx >= 0 {
		letters = string(rune('A'+idx%26)) + letters
		idx = idx/26 - 1
	}
	return letters
}
