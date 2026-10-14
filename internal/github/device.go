package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// OAuthClientID is the gh-runners OAuth app used for "Sign in with GitHub"
// (device flow, no client secret needed). Override with
// $GH_RUNNERS_OAUTH_CLIENT_ID or -ldflags "-X .../github.OAuthClientID=...".
var OAuthClientID = ""

// SignInScopes are the classic scopes asked for at sign-in: repo covers
// runner registration on your repositories and reading Actions jobs.
const SignInScopes = "repo"

// ClientID returns the OAuth client id in use, or "" if sign-in is unavailable.
func ClientID() string {
	if v := strings.TrimSpace(os.Getenv("GH_RUNNERS_OAUTH_CLIENT_ID")); v != "" {
		return v
	}
	return OAuthClientID
}

// DeviceCode is the first step of the device flow.
type DeviceCode struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURI string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// DeviceBase is github.com; tests point it at a fake server.
var DeviceBase = "https://github.com"

func postForm(ctx context.Context, path string, form url.Values, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, DeviceBase+path, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gh-runners")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("POST %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// StartDeviceFlow asks GitHub for a code the user types at VerificationURI.
func StartDeviceFlow(ctx context.Context, clientID, scopes string) (DeviceCode, error) {
	var d DeviceCode
	var e struct {
		Error string `json:"error"`
		Desc  string `json:"error_description"`
	}
	var raw json.RawMessage
	if err := postForm(ctx, "/login/device/code", url.Values{"client_id": {clientID}, "scope": {scopes}}, &raw); err != nil {
		return d, err
	}
	_ = json.Unmarshal(raw, &e)
	if e.Error != "" {
		return d, fmt.Errorf("github sign-in: %s %s", e.Error, e.Desc)
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return d, err
	}
	if d.Interval <= 0 {
		d.Interval = 5
	}
	return d, nil
}

// ErrSignInDenied means the user pressed Cancel on github.com.
var ErrSignInDenied = errors.New("sign-in was cancelled on GitHub")

// WaitDeviceToken polls until the user has entered the code, then returns
// the access token.
func WaitDeviceToken(ctx context.Context, clientID string, d DeviceCode) (string, error) {
	interval := time.Duration(d.Interval) * time.Second
	deadline := time.Now().Add(time.Duration(d.ExpiresIn) * time.Second)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(interval):
		}
		if d.ExpiresIn > 0 && time.Now().After(deadline) {
			return "", errors.New("the sign-in code expired; start again")
		}
		var r struct {
			AccessToken string `json:"access_token"`
			Error       string `json:"error"`
			Desc        string `json:"error_description"`
			Interval    int    `json:"interval"`
		}
		err := postForm(ctx, "/login/oauth/access_token", url.Values{
			"client_id":   {clientID},
			"device_code": {d.DeviceCode},
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
		}, &r)
		if err != nil {
			continue // network blip: keep polling until the deadline
		}
		switch r.Error {
		case "":
			if r.AccessToken != "" {
				return r.AccessToken, nil
			}
		case "authorization_pending":
		case "slow_down":
			if r.Interval > 0 {
				interval = time.Duration(r.Interval) * time.Second
			} else {
				interval += 5 * time.Second
			}
		case "access_denied":
			return "", ErrSignInDenied
		case "expired_token":
			return "", errors.New("the sign-in code expired; start again")
		default:
			return "", fmt.Errorf("github sign-in: %s %s", r.Error, r.Desc)
		}
	}
}
