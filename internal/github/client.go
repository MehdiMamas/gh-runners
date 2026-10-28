// Package github is a small GitHub REST client covering the self-hosted runner
// endpoints gh-runners needs.
package github

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Client talks to the GitHub REST API with a single token.
type Client struct {
	Token   string
	BaseURL string // defaults to https://api.github.com
	HTTP    *http.Client

	mu    sync.Mutex
	etags map[string]cachedResp
}

type cachedResp struct {
	etag string
	body []byte
}

// APIError is a non-2xx response.
type APIError struct {
	Status  int
	Method  string
	Path    string
	Message string
	Scopes  string // X-OAuth-Scopes of the token, when GitHub sends it
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("GitHub API %s %s: %d %s", e.Method, e.Path, e.Status, e.Message)
	switch e.Status {
	case http.StatusUnauthorized:
		msg += " (token invalid or expired: run `gh auth login` or `gh-runners login`)"
	case http.StatusForbidden, http.StatusNotFound:
		if strings.Contains(e.Path, "/actions/runners") {
			msg += " (the token needs admin rights on this repo/org: classic token scope `repo`, plus `admin:org` for org runners; fine-grained token: Administration read/write)"
		}
	}
	return msg
}

// IsNotFound reports whether err is a 404 from the API.
func IsNotFound(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// APIBase is the REST API that New points at; tests use a fake server.
var APIBase = "https://api.github.com"

// New returns a client using the token found by ResolveToken.
func New(token string) *Client {
	return &Client{Token: token, BaseURL: APIBase, HTTP: &http.Client{Timeout: 60 * time.Second}}
}

// TokenSource names where a token came from, for `doctor`.
type TokenSource struct {
	Token  string
	Source string
}

// ResolveToken finds a token: GH_RUNNERS_TOKEN, GH_TOKEN, GITHUB_TOKEN, then
// the token saved by `gh-runners login` or the dashboard's sign-in
// (savedPath), then `gh auth token` unless GH_RUNNERS_NO_GH_CLI is set (tests
// set it so they never pick up the developer's real account).
func ResolveToken(savedPath string) (TokenSource, error) {
	for _, env := range []string{"GH_RUNNERS_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(env)); v != "" {
			return TokenSource{v, "$" + env}, nil
		}
	}
	if savedPath != "" {
		if b, err := os.ReadFile(savedPath); err == nil {
			if t := strings.TrimSpace(string(b)); t != "" {
				return TokenSource{t, savedPath}, nil
			}
		}
	}
	if gh, err := exec.LookPath("gh"); err == nil && os.Getenv("GH_RUNNERS_NO_GH_CLI") == "" {
		cmd := exec.Command(gh, "auth", "token", "--hostname", "github.com")
		hideWindow(cmd)
		out, err := cmd.Output()
		if err == nil {
			if t := strings.TrimSpace(string(out)); t != "" {
				return TokenSource{t, "gh auth token"}, nil
			}
		}
	}
	return TokenSource{}, errors.New("not signed in to GitHub: sign in from the dashboard (gh-runners ui), run `gh auth login`, or run `gh-runners login` with a personal access token")
}

func (c *Client) headers(req *http.Request) {
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "gh-runners")
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) (*http.Response, error) {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	u := path
	if !strings.HasPrefix(path, "http") {
		u = strings.TrimRight(c.BaseURL, "/") + path
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	c.headers(req)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// A GET remembers the ETag: an unchanged answer comes back as 304, which
	// GitHub does not count against the rate limit.
	cond := method == http.MethodGet && c.Token != ""
	var prev cachedResp
	var hasPrev bool
	if cond {
		c.mu.Lock()
		prev, hasPrev = c.etags[u]
		c.mu.Unlock()
		if hasPrev {
			req.Header.Set("If-None-Match", prev.etag)
		}
	}
	if err := limiter.before(); err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp, err
	}
	switch {
	case resp.StatusCode == http.StatusNotModified && hasPrev:
		limiter.after(resp, "")
		data = prev.body
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(data))
		}
		if rl := limiter.after(resp, e.Message); rl != nil {
			return resp, rl
		}
		return resp, &APIError{Status: resp.StatusCode, Method: method, Path: path, Message: e.Message, Scopes: resp.Header.Get("X-OAuth-Scopes")}
	default:
		limiter.after(resp, "")
		if et := resp.Header.Get("ETag"); cond && et != "" {
			c.mu.Lock()
			if c.etags == nil {
				c.etags = map[string]cachedResp{}
			}
			c.etags[u] = cachedResp{et, data}
			c.mu.Unlock()
		}
	}
	if out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, out); err != nil {
			return resp, fmt.Errorf("decode %s: %w", path, err)
		}
	}
	return resp, nil
}

// Scope is a repository ("owner/repo") or an organization ("org").
type Scope struct {
	Owner string
	Repo  string // empty for an organization
}

// ParseScope accepts owner/repo, an org name, or a github.com URL.
func ParseScope(s string, org bool) (Scope, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, ".git")
	for _, p := range []string{"https://github.com/", "http://github.com/", "git@github.com:", "ssh://git@github.com/", "github.com/"} {
		s = strings.TrimPrefix(s, p)
	}
	s = strings.Trim(s, "/")
	parts := strings.Split(s, "/")
	switch {
	case org && len(parts) == 1 && parts[0] != "":
		return Scope{Owner: parts[0]}, nil
	case !org && len(parts) == 2 && parts[0] != "" && parts[1] != "":
		return Scope{Owner: parts[0], Repo: parts[1]}, nil
	case org:
		return Scope{}, fmt.Errorf("%q is not an organization name", s)
	default:
		return Scope{}, fmt.Errorf("%q is not owner/repo (use --org for an organization)", s)
	}
}

func (s Scope) IsOrg() bool { return s.Repo == "" }

func (s Scope) String() string {
	if s.IsOrg() {
		return s.Owner
	}
	return s.Owner + "/" + s.Repo
}

// URL is what the runner's config script takes as --url.
func (s Scope) URL() string { return "https://github.com/" + s.String() }

func (s Scope) apiBase() string {
	if s.IsOrg() {
		return "/orgs/" + url.PathEscape(s.Owner) + "/actions/runners"
	}
	return "/repos/" + url.PathEscape(s.Owner) + "/" + url.PathEscape(s.Repo) + "/actions/runners"
}

type tokenResp struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// RegistrationToken returns a one-hour token for config.sh.
func (c *Client) RegistrationToken(ctx context.Context, s Scope) (string, error) {
	var t tokenResp
	_, err := c.do(ctx, http.MethodPost, s.apiBase()+"/registration-token", nil, &t)
	return t.Token, err
}

// RemoveToken returns a one-hour token for `config.sh remove`.
func (c *Client) RemoveToken(ctx context.Context, s Scope) (string, error) {
	var t tokenResp
	_, err := c.do(ctx, http.MethodPost, s.apiBase()+"/remove-token", nil, &t)
	return t.Token, err
}

// Label is a runner label.
type Label struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Runner is a registered self-hosted runner.
type Runner struct {
	ID     int64   `json:"id"`
	Name   string  `json:"name"`
	OS     string  `json:"os"`
	Status string  `json:"status"` // online | offline
	Busy   bool    `json:"busy"`
	Labels []Label `json:"labels"`
}

// ListRunners lists every runner registered to the scope.
func (c *Client) ListRunners(ctx context.Context, s Scope) ([]Runner, error) {
	var all []Runner
	for page := 1; ; page++ {
		var r struct {
			TotalCount int      `json:"total_count"`
			Runners    []Runner `json:"runners"`
		}
		if _, err := c.do(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=100&page=%d", s.apiBase(), page), nil, &r); err != nil {
			return nil, err
		}
		all = append(all, r.Runners...)
		if len(r.Runners) < 100 || len(all) >= r.TotalCount {
			return all, nil
		}
	}
}

// DeleteRunner removes a runner registration by id.
func (c *Client) DeleteRunner(ctx context.Context, s Scope, id int64) error {
	_, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", s.apiBase(), id), nil, nil)
	return err
}

// SetRunnerLabels replaces a runner's custom labels; GitHub keeps
// self-hosted, the OS and the architecture itself.
func (c *Client) SetRunnerLabels(ctx context.Context, s Scope, id int64, labels []string) ([]Label, error) {
	if labels == nil {
		labels = []string{}
	}
	var r struct {
		Labels []Label `json:"labels"`
	}
	_, err := c.do(ctx, http.MethodPut, fmt.Sprintf("%s/%d/labels", s.apiBase(), id), map[string]any{"labels": labels}, &r)
	return r.Labels, err
}

// WorkflowFile is one file of a repository's .github/workflows folder.
type WorkflowFile struct {
	Name    string
	Content string
}

// Workflows returns the YAML files in the repository's .github/workflows
// folder on the default branch; none when the folder does not exist.
func (c *Client) Workflows(ctx context.Context, owner, repo string) ([]WorkflowFile, error) {
	base := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/contents/.github/workflows"
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	if _, err := c.do(ctx, http.MethodGet, base, nil, &entries); err != nil {
		if IsNotFound(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []WorkflowFile
	for _, e := range entries {
		if e.Type != "file" || !(strings.HasSuffix(e.Name, ".yml") || strings.HasSuffix(e.Name, ".yaml")) {
			continue
		}
		var f struct {
			Content  string `json:"content"`
			Encoding string `json:"encoding"`
		}
		if _, err := c.do(ctx, http.MethodGet, base+"/"+url.PathEscape(e.Name), nil, &f); err != nil {
			return nil, err
		}
		text := f.Content
		if f.Encoding == "base64" {
			b, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(f.Content, "\n", ""))
			if err != nil {
				return nil, fmt.Errorf("%s: %w", e.Name, err)
			}
			text = string(b)
		}
		out = append(out, WorkflowFile{Name: e.Name, Content: text})
	}
	return out, nil
}

// Download is one runner package for an OS/arch.
type Download struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	DownloadURL  string `json:"download_url"`
	Filename     string `json:"filename"`
	SHA256       string `json:"sha256_checksum"`
}

// Downloads lists the runner packages GitHub offers for this scope.
func (c *Client) Downloads(ctx context.Context, s Scope) ([]Download, error) {
	var d []Download
	_, err := c.do(ctx, http.MethodGet, s.apiBase()+"/downloads", nil, &d)
	return d, err
}

// RunnerReleaseDownloads builds the download list from the latest
// actions/runner release. The per-scope downloads endpoint returns an empty
// list for repositories that never ran a workflow, so this is the fallback.
func (c *Client) RunnerReleaseDownloads(ctx context.Context) ([]Download, error) {
	var rel struct {
		Body   string `json:"body"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if _, err := c.do(ctx, http.MethodGet, "/repos/actions/runner/releases/latest", nil, &rel); err != nil {
		return nil, err
	}
	var out []Download
	for _, a := range rel.Assets {
		m := assetRe.FindStringSubmatch(a.Name)
		if m == nil {
			continue
		}
		d := Download{OS: m[1], Architecture: m[2], DownloadURL: a.URL, Filename: a.Name}
		d.SHA256 = releaseSHA(rel.Body, m[1]+"-"+m[2])
		out = append(out, d)
	}
	return out, nil
}

// releaseSHA finds a checksum in actions/runner release notes, which carry
// "<!-- BEGIN SHA win-x64 -->hash<!-- END SHA win-x64 -->".
func releaseSHA(body, platform string) string {
	begin := "<!-- BEGIN SHA " + platform + " -->"
	i := strings.Index(body, begin)
	if i < 0 {
		return ""
	}
	rest := body[i+len(begin):]
	if j := strings.Index(rest, "<!--"); j > 0 {
		return strings.TrimSpace(rest[:j])
	}
	return ""
}

var assetRe = regexp.MustCompile(`^actions-runner-(osx|linux|win)-(x64|arm64|arm)-\d+\.\d+\.\d+\.(?:tar\.gz|zip)$`)

// JITConfig is a just-in-time runner registration.
type JITConfig struct {
	Runner           Runner `json:"runner"`
	EncodedJITConfig string `json:"encoded_jit_config"`
}

// GenerateJITConfig registers a single-use runner and returns its encoded config.
func (c *Client) GenerateJITConfig(ctx context.Context, s Scope, name string, labels []string, groupID int64) (JITConfig, error) {
	if groupID == 0 {
		groupID = 1 // the Default group; repositories only have this one
	}
	var j JITConfig
	body := map[string]any{"name": name, "runner_group_id": groupID, "labels": labels, "work_folder": "_work"}
	_, err := c.do(ctx, http.MethodPost, s.apiBase()+"/generate-jitconfig", body, &j)
	return j, err
}

// Repo is the subset of repository fields gh-runners uses.
type Repo struct {
	FullName      string `json:"full_name"`
	Name          string `json:"name"`
	Description   string `json:"description"`
	HTMLURL       string `json:"html_url"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	Fork          bool   `json:"fork"`
	Disabled      bool   `json:"disabled"`
	PushedAt      string `json:"pushed_at"`
	DefaultBranch string `json:"default_branch"`
	Owner         struct {
		Login string `json:"login"`
		Type  string `json:"type"`
	} `json:"owner"`
	Permissions struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

// GetRepo fetches one repository.
func (c *Client) GetRepo(ctx context.Context, owner, repo string) (Repo, error) {
	var r Repo
	_, err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo), nil, &r)
	return r, err
}

// ListMyRepos lists repositories the token's user can administer
// (owned, collaborator with admin, and org repos).
func (c *Client) ListMyRepos(ctx context.Context) ([]Repo, error) {
	var all []Repo
	for page := 1; ; page++ {
		var r []Repo
		if _, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/user/repos?per_page=100&page=%d&sort=pushed&affiliation=owner,collaborator,organization_member", page), nil, &r); err != nil {
			return nil, err
		}
		all = append(all, r...)
		if len(r) < 100 {
			return all, nil
		}
	}
}

// User is the authenticated user.
type User struct {
	Login     string `json:"login"`
	Name      string `json:"name"`
	AvatarURL string `json:"avatar_url"`
}

// Me returns the authenticated user and the token's OAuth scopes (classic tokens only).
func (c *Client) Me(ctx context.Context) (User, string, error) {
	var u User
	resp, err := c.do(ctx, http.MethodGet, "/user", nil, &u)
	scopes := ""
	if resp != nil {
		scopes = resp.Header.Get("X-OAuth-Scopes")
	}
	return u, scopes, err
}

// WorkflowRun is a queued or running workflow run.
type WorkflowRun struct {
	ID     int64  `json:"id"`
	Status string `json:"status"`
}

// Runs lists workflow runs with a status (queued, in_progress).
func (c *Client) Runs(ctx context.Context, owner, repo, status string) ([]WorkflowRun, error) {
	var r struct {
		WorkflowRuns []WorkflowRun `json:"workflow_runs"`
	}
	_, err := c.do(ctx, http.MethodGet, "/repos/"+url.PathEscape(owner)+"/"+url.PathEscape(repo)+"/actions/runs?per_page=50&status="+url.QueryEscape(status), nil, &r)
	return r.WorkflowRuns, err
}

// Job is a workflow job.
type Job struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Labels []string `json:"labels"`
}

// RunJobs lists the jobs of a workflow run.
func (c *Client) RunJobs(ctx context.Context, owner, repo string, runID int64) ([]Job, error) {
	var r struct {
		Jobs []Job `json:"jobs"`
	}
	_, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/actions/runs/%d/jobs?filter=latest&per_page=100", url.PathEscape(owner), url.PathEscape(repo), runID), nil, &r)
	return r.Jobs, err
}

// NewIssue is a created issue.
type NewIssue struct {
	Number  int    `json:"number"`
	HTMLURL string `json:"html_url"`
}

// CreateIssue opens an issue as the token's user.
func (c *Client) CreateIssue(ctx context.Context, owner, repo, title, body string, labels []string) (NewIssue, error) {
	var out NewIssue
	_, err := c.do(ctx, http.MethodPost, "/repos/"+owner+"/"+repo+"/issues",
		map[string]any{"title": title, "body": body, "labels": labels}, &out)
	return out, err
}

// CreateRepo creates a private repository for the token's user, with a first
// commit so the contents API works on it at once.
func (c *Client) CreateRepo(ctx context.Context, name, description string) (Repo, error) {
	var r Repo
	_, err := c.do(ctx, http.MethodPost, "/user/repos",
		map[string]any{"name": name, "description": description, "private": true, "auto_init": true}, &r)
	return r, err
}

// Content is a file in a repository, or one entry of a folder listing
// (then Content is empty).
type Content struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	SHA     string `json:"sha"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

// Data decodes the file's content.
func (c Content) Data() ([]byte, error) {
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(c.Content, "\n", ""))
}

func contentsPath(owner, repo, path string) string {
	parts := strings.Split(path, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/contents/" + strings.Join(parts, "/")
}

// GetContents fetches one file.
func (c *Client) GetContents(ctx context.Context, owner, repo, path string) (Content, error) {
	var out Content
	_, err := c.do(ctx, http.MethodGet, contentsPath(owner, repo, path), nil, &out)
	return out, err
}

// ListContents lists a folder: names and blob SHAs, without the files.
func (c *Client) ListContents(ctx context.Context, owner, repo, path string) ([]Content, error) {
	var out []Content
	_, err := c.do(ctx, http.MethodGet, contentsPath(owner, repo, path), nil, &out)
	return out, err
}

// PutContents creates a file (sha empty) or replaces it (sha of the old
// blob) and returns the new blob's SHA.
func (c *Client) PutContents(ctx context.Context, owner, repo, path, message string, data []byte, sha string) (string, error) {
	body := map[string]any{"message": message, "content": base64.StdEncoding.EncodeToString(data)}
	if sha != "" {
		body["sha"] = sha
	}
	var out struct {
		Content Content `json:"content"`
	}
	_, err := c.do(ctx, http.MethodPut, contentsPath(owner, repo, path), body, &out)
	return out.Content.SHA, err
}

// DeleteContents deletes a file; sha is the blob to delete.
func (c *Client) DeleteContents(ctx context.Context, owner, repo, path, message, sha string) error {
	_, err := c.do(ctx, http.MethodDelete, contentsPath(owner, repo, path), map[string]any{"message": message, "sha": sha}, nil)
	return err
}
