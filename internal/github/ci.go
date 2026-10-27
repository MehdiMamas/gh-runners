package github

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
)

// Do sends one API request and decodes the JSON answer into out (may be nil).
// It is the escape hatch for endpoints without a typed method, such as the
// pull request and commit status calls the local CI uses.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	_, err := c.do(ctx, method, path, body, out)
	return err
}

// PullRequest is the part of a pull request the local CI looks at.
type PullRequest struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	User   struct {
		Login string `json:"login"`
	} `json:"user"`
	Head PRRef `json:"head"`
	Base PRRef `json:"base"`
}

// PRRef is one side of a pull request. Repo is nil when a fork was deleted.
type PRRef struct {
	SHA  string `json:"sha"`
	Ref  string `json:"ref"`
	Repo *struct {
		FullName string `json:"full_name"`
	} `json:"repo"`
}

// RepoName is "owner/repo" of the ref, empty when the repo is gone.
func (r PRRef) RepoName() string {
	if r.Repo == nil {
		return ""
	}
	return r.Repo.FullName
}

// PullRequest fetches one pull request.
func (c *Client) PullRequest(ctx context.Context, owner, repo string, n int) (PullRequest, error) {
	var p PullRequest
	err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, n), nil, &p)
	return p, err
}

// OpenPullRequests lists open pull requests, newest first. The client's ETag
// cache makes an unchanged list a free 304.
func (c *Client) OpenPullRequests(ctx context.Context, owner, repo string) ([]PullRequest, error) {
	var all []PullRequest
	for page := 1; ; page++ {
		var r []PullRequest
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls?state=open&per_page=100&page=%d", owner, repo, page), nil, &r); err != nil {
			return nil, err
		}
		all = append(all, r...)
		if len(r) < 100 {
			return all, nil
		}
	}
}

// PullRequestFiles lists the paths a pull request changes (paginated).
func (c *Client) PullRequestFiles(ctx context.Context, owner, repo string, n int) ([]string, error) {
	var all []string
	for page := 1; ; page++ {
		var r []struct {
			Filename         string `json:"filename"`
			PreviousFilename string `json:"previous_filename"`
		}
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d/files?per_page=100&page=%d", owner, repo, n, page), nil, &r); err != nil {
			return nil, err
		}
		for _, f := range r {
			all = append(all, f.Filename)
			if f.PreviousFilename != "" {
				all = append(all, f.PreviousFilename)
			}
		}
		if len(r) < 100 {
			return all, nil
		}
	}
}

// FileAt returns a repository file's content at a ref.
func (c *Client) FileAt(ctx context.Context, owner, repo, path, ref string) ([]byte, error) {
	var r struct {
		Content  string `json:"content"`
		Encoding string `json:"encoding"`
	}
	p := fmt.Sprintf("/repos/%s/%s/contents/%s?ref=%s", owner, repo, path, url.QueryEscape(ref))
	if err := c.Do(ctx, http.MethodGet, p, nil, &r); err != nil {
		return nil, err
	}
	if r.Encoding != "base64" {
		return nil, fmt.Errorf("unexpected encoding %q for %s", r.Encoding, path)
	}
	return base64.StdEncoding.DecodeString(strings.ReplaceAll(r.Content, "\n", ""))
}

// DownloadTarball saves the repository source at sha to dest. GitHub answers
// with a redirect to another host; Go drops the Authorization header there,
// so the token never leaves api.github.com.
func (c *Client) DownloadTarball(ctx context.Context, owner, repo, sha, dest string) error {
	u := strings.TrimRight(c.BaseURL, "/") + fmt.Sprintf("/repos/%s/%s/tarball/%s", owner, repo, sha)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	c.headers(req)
	hc := *c.HTTP
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return &APIError{Status: resp.StatusCode, Method: http.MethodGet, Path: u, Message: resp.Status}
	}
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// SetStatus sets a commit status (state: pending, success, failure, error).
func (c *Client) SetStatus(ctx context.Context, owner, repo, sha, state, name, desc string) error {
	if len(desc) > 140 {
		desc = desc[:140]
	}
	return c.Do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/statuses/%s", owner, repo, sha),
		map[string]string{"state": state, "context": name, "description": desc}, nil)
}

// UpsertComment edits the pull request comment containing marker, or adds
// one. It returns the comment's id.
func (c *Client) UpsertComment(ctx context.Context, owner, repo string, n int, marker, body string) error {
	for page := 1; ; page++ {
		var cs []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", owner, repo, n, page), nil, &cs); err != nil {
			return err
		}
		for _, cm := range cs {
			if strings.Contains(cm.Body, marker) {
				return c.Do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/%s/issues/comments/%d", owner, repo, cm.ID), map[string]string{"body": body}, nil)
			}
		}
		if len(cs) < 100 {
			break
		}
	}
	return c.Do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, n), map[string]string{"body": body}, nil)
}

// Comment adds a plain pull request comment.
func (c *Client) Comment(ctx context.Context, owner, repo string, n int, body string) error {
	return c.Do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repo, n), map[string]string{"body": body}, nil)
}

// Approved reports whether the pull request has an approving review and no
// open change request, counting each reviewer's latest verdict.
func (c *Client) Approved(ctx context.Context, owner, repo string, n int) (bool, error) {
	latest := map[string]string{}
	for page := 1; ; page++ {
		var rs []struct {
			State string `json:"state"`
			User  struct {
				Login string `json:"login"`
			} `json:"user"`
		}
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews?per_page=100&page=%d", owner, repo, n, page), nil, &rs); err != nil {
			return false, err
		}
		for _, r := range rs {
			if r.State == "APPROVED" || r.State == "CHANGES_REQUESTED" || r.State == "DISMISSED" {
				latest[r.User.Login] = r.State
			}
		}
		if len(rs) < 100 {
			break
		}
	}
	ok := false
	for _, s := range latest {
		switch s {
		case "CHANGES_REQUESTED":
			return false, nil
		case "APPROVED":
			ok = true
		}
	}
	return ok, nil
}

// SquashMerge squash-merges a pull request, pinned to the head sha that was tested.
func (c *Client) SquashMerge(ctx context.Context, owner, repo string, n int, sha string) error {
	return c.Do(ctx, http.MethodPut, fmt.Sprintf("/repos/%s/%s/pulls/%d/merge", owner, repo, n),
		map[string]string{"merge_method": "squash", "sha": sha}, nil)
}

// Issue is an open issue: its number and title.
type Issue struct {
	Number int
	Title  string
}

// ListOpenIssues lists open issues (not pull requests), at most 300.
func (c *Client) ListOpenIssues(ctx context.Context, owner, repo string) ([]Issue, error) {
	const limit = 300
	var all []Issue
	for page := 1; page <= 6 && len(all) < limit; page++ {
		var r []struct {
			Number      int             `json:"number"`
			Title       string          `json:"title"`
			PullRequest json.RawMessage `json:"pull_request"`
		}
		if err := c.Do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/%s/issues?state=open&per_page=100&page=%d", owner, repo, page), nil, &r); err != nil {
			return all, err
		}
		for _, x := range r {
			if len(x.PullRequest) == 0 && len(all) < limit {
				all = append(all, Issue{Number: x.Number, Title: x.Title})
			}
		}
		if len(r) < 100 {
			break
		}
	}
	return all, nil
}
