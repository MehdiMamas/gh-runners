package web

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

const secret = "s3cret"

// fakeGitHub answers the few API calls the dashboard makes.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	var polls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("GET /user", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer good-token" && r.Header.Get("Authorization") != "Bearer device-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"message":"Bad credentials"}`)
			return
		}
		w.Header().Set("X-OAuth-Scopes", "repo")
		_, _ = io.WriteString(w, `{"login":"octo","name":"Octo Cat","avatar_url":"https://avatars.githubusercontent.com/u/1"}`)
	})
	mux.HandleFunc("GET /user/repos", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[
			{"full_name":"octo/private","name":"private","private":true,"owner":{"login":"octo","type":"User"},"permissions":{"admin":true}},
			{"full_name":"octo/public","name":"public","private":false,"owner":{"login":"octo","type":"User"},"permissions":{"admin":true}}]`)
	})
	mux.HandleFunc("POST /login/device/code", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "test-client" || r.Form.Get("scope") != "repo" {
			t.Errorf("device code request: %v", r.Form)
		}
		_, _ = io.WriteString(w, `{"device_code":"dc","user_code":"ABCD-1234","verification_uri":"https://github.com/login/device","expires_in":60,"interval":1}`)
	})
	mux.HandleFunc("POST /login/oauth/access_token", func(w http.ResponseWriter, r *http.Request) {
		if polls.Add(1) == 1 {
			_, _ = io.WriteString(w, `{"error":"authorization_pending"}`)
			return
		}
		_, _ = io.WriteString(w, `{"access_token":"device-token","token_type":"bearer","scope":"repo"}`)
	})
	mux.HandleFunc("GET /repos/octo/private/actions/runners", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"total_count":1,"runners":[{"id":7,"name":"laptop","os":"Windows","status":"online","busy":false,
			"labels":[{"name":"self-hosted","type":"read-only"},{"name":"Windows","type":"read-only"},{"name":"light","type":"custom"}]}]}`)
	})
	mux.HandleFunc("PUT /repos/octo/private/actions/runners/7/labels", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Labels []string `json:"labels"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if strings.Join(body.Labels, ",") != "heavy" {
			t.Errorf("labels sent: %v", body.Labels)
		}
		_, _ = io.WriteString(w, `{"labels":[{"name":"self-hosted","type":"read-only"},{"name":"heavy","type":"custom"}]}`)
	})
	mux.HandleFunc("GET /repos/octo/private/contents/.github/workflows", func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `[{"name":"ci.yml","type":"file"},{"name":"README.md","type":"file"}]`)
	})
	mux.HandleFunc("GET /repos/octo/private/contents/.github/workflows/ci.yml", func(w http.ResponseWriter, r *http.Request) {
		// A runs-on: [self-hosted, heavy] job in base64, wrapped as GitHub does.
		_, _ = io.WriteString(w, `{"encoding":"base64","content":"am9iczoKICB0OgogICAgcnVucy1vbjog\nW3NlbGYtaG9zdGVkLCBoZWF2eV0K\n"}`)
	})
	mux.HandleFunc("GET /repos/octo/public/contents/.github/workflows", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"message":"Not Found"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func setup(t *testing.T) (*Server, http.Handler) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("GH_RUNNERS_HOME", home)
	for _, k := range []string{"GH_RUNNERS_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		t.Setenv(k, "")
	}
	t.Setenv("PATH", "") // no `gh` CLI: only the saved token counts
	t.Setenv("GH_RUNNERS_OAUTH_CLIENT_ID", "test-client")
	gh := fakeGitHub(t)
	oldAPI, oldDev := github.APIBase, github.DeviceBase
	github.APIBase, github.DeviceBase = gh.URL, gh.URL
	oldSvc := serviceStatus
	serviceStatus = func() *service.Status { return &service.Status{Installed: true, Where: "test"} }
	t.Cleanup(func() { github.APIBase, github.DeviceBase, serviceStatus = oldAPI, oldDev, oldSvc })

	s := New(Hooks{Version: "test", Status: func() control.Status { return control.Status{PID: 42} }}, secret)
	s.spawn = func(string, ...string) error { return nil }
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "control:"+r.URL.Path) })
	return s, s.Handler(next)
}

type req struct {
	method, path, body string
	host               string
	cookie, header     bool
	origin             string
}

func do(t *testing.T, h http.Handler, q req) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if q.body != "" {
		body = strings.NewReader(q.body)
	}
	r := httptest.NewRequest(q.method, q.path, body)
	r.Host = "127.0.0.1:47321"
	if q.host != "" {
		r.Host = q.host
	}
	if q.cookie {
		r.AddCookie(&http.Cookie{Name: CookieName, Value: secret})
	}
	if q.header {
		r.Header.Set("X-GHR", "1")
	}
	if q.origin != "" {
		r.Header.Set("Origin", q.origin)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func api(t *testing.T, h http.Handler, method, path, body string, out any) int {
	t.Helper()
	w := do(t, h, req{method: method, path: path, body: body, cookie: true, header: true, origin: "http://127.0.0.1:47321"})
	if out != nil {
		if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, w.Body)
		}
	}
	return w.Code
}

func TestGuards(t *testing.T) {
	_, h := setup(t)
	cases := []struct {
		name string
		q    req
		want int
	}{
		{"foreign host (DNS rebinding)", req{method: "GET", path: "/", host: "evil.example:47321", cookie: true}, http.StatusMisdirectedRequest},
		{"locked page", req{method: "GET", path: "/"}, http.StatusUnauthorized},
		{"unlocked page", req{method: "GET", path: "/", cookie: true}, http.StatusOK},
		{"localhost name", req{method: "GET", path: "/", host: "localhost:47321", cookie: true}, http.StatusOK},
		{"api without header", req{method: "GET", path: "/api/state", cookie: true}, http.StatusForbidden},
		{"api without cookie", req{method: "GET", path: "/api/state", header: true}, http.StatusUnauthorized},
		{"api cross origin", req{method: "POST", path: "/api/logout", cookie: true, header: true, origin: "http://evil.example"}, http.StatusForbidden},
		{"api ok", req{method: "GET", path: "/api/state", cookie: true, header: true}, http.StatusOK},
		{"assets are public", req{method: "GET", path: "/assets/app.js"}, http.StatusOK},
	}
	for _, c := range cases {
		if w := do(t, h, c.q); w.Code != c.want {
			t.Errorf("%s: got %d, want %d (%s)", c.name, w.Code, c.want, w.Body)
		}
	}
	if w := do(t, h, req{method: "GET", path: "/status"}); w.Body.String() != "control:/status" {
		t.Errorf("control API not passed through: %q", w.Body)
	}
	if w := do(t, h, req{method: "GET", path: "/", cookie: true}); !strings.Contains(w.Header().Get("Content-Security-Policy"), "script-src 'self'") {
		t.Errorf("no CSP on the page: %v", w.Header())
	}
}

func TestSecretLinkSetsCookie(t *testing.T) {
	_, h := setup(t)
	w := do(t, h, req{method: "GET", path: "/?k=" + secret})
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
		t.Fatalf("got %d %v", w.Code, w.Header())
	}
	c := w.Result().Cookies()
	if len(c) != 1 || c[0].Value != secret || !c[0].HttpOnly || c[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie: %+v", c)
	}
	w = do(t, h, req{method: "GET", path: "/?k=wrong"})
	if w.Code != http.StatusSeeOther || len(w.Result().Cookies()) != 0 {
		t.Fatalf("a wrong secret set a cookie: %d %v", w.Code, w.Result().Cookies())
	}
}

func TestStateSignedOutThenToken(t *testing.T) {
	_, h := setup(t)
	var st stateView
	if code := api(t, h, "GET", "/api/state", "", &st); code != 200 {
		t.Fatalf("state: %d", code)
	}
	if st.Auth.SignedIn || !st.Auth.CanDevice || st.PID != 42 || st.RunnersDir != store.DefaultRunnersDir() {
		t.Fatalf("state: %+v", st)
	}
	var e apiError
	if code := api(t, h, "POST", "/api/login/token", `{"token":"bad-token"}`, &e); code != 400 || !strings.Contains(e.Error, "did not accept") {
		t.Fatalf("bad token: %d %+v", code, e)
	}
	var ok map[string]string
	if code := api(t, h, "POST", "/api/login/token", `{"token":" good-token "}`, &ok); code != 200 || ok["login"] != "octo" {
		t.Fatalf("good token: %d %v", code, ok)
	}
	if b, _ := os.ReadFile(store.TokenPath()); strings.TrimSpace(string(b)) != "good-token" {
		t.Fatalf("token file: %q", b)
	}
	api(t, h, "GET", "/api/state", "", &st)
	if !st.Auth.SignedIn || st.Auth.Login != "octo" || !st.Auth.Saved {
		t.Fatalf("after sign-in: %+v", st.Auth)
	}
	var repos struct {
		Me    string     `json:"me"`
		Repos []repoView `json:"repos"`
	}
	if code := api(t, h, "GET", "/api/repos", "", &repos); code != 200 || len(repos.Repos) != 2 || !repos.Repos[0].Admin {
		t.Fatalf("repos: %d %+v", code, repos)
	}
	if !repos.Repos[0].SyncMatch || repos.Repos[1].SyncMatch {
		t.Errorf("sync match: private %v (want true), public %v (want false)", repos.Repos[0].SyncMatch, repos.Repos[1].SyncMatch)
	}
	api(t, h, "POST", "/api/logout", "", &st.Auth)
	if st.Auth.SignedIn {
		t.Fatalf("still signed in after logout: %+v", st.Auth)
	}
}

func TestDeviceSignIn(t *testing.T) {
	_, h := setup(t)
	var v deviceView
	if code := api(t, h, "POST", "/api/login/device", "", &v); code != 200 || v.Code != "ABCD-1234" || v.State != "pending" {
		t.Fatalf("start: %d %+v", code, v)
	}
	deadline := time.Now().Add(15 * time.Second)
	for v.State == "pending" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
		api(t, h, "GET", "/api/login/device", "", &v)
	}
	if v.State != "done" || v.Login != "octo" {
		t.Fatalf("device flow ended as %+v", v)
	}
	if b, _ := os.ReadFile(store.TokenPath()); strings.TrimSpace(string(b)) != "device-token" {
		t.Fatalf("token file: %q", b)
	}
}

func TestConfigRoundTrip(t *testing.T) {
	_, h := setup(t)
	body := `{"labels":[" gpu ",""],"runner_name":" box ","sync":{"auto":false,"interval":"6h0m0s","owners":["octo"]},
		"on_demand":{"enabled":true,"max_concurrent":3,"poll":"30s","active_within":"168h0m0s"}}`
	var cfg store.Config
	if code := api(t, h, "PUT", "/api/config", body, &cfg); code != 200 {
		t.Fatalf("put: %d", code)
	}
	got, err := store.LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Labels, ",") != "gpu" || got.RunnerName != "box" || got.Sync.Interval.Duration != 6*time.Hour ||
		!got.OnDemand.Enabled || got.OnDemand.MaxConcurrent != 3 || got.OnDemand.Poll.Duration != 30*time.Second {
		t.Fatalf("saved config: %+v", got)
	}
	for _, bad := range []string{`{"on_demand":{"max_concurrent":65}}`, `{"sync":{"interval":"1m"}}`, `{"sync":{"interval":"soon"}}`} {
		if code := api(t, h, "PUT", "/api/config", bad, nil); code != 400 {
			t.Errorf("%s: got %d, want 400", bad, code)
		}
	}
}

func TestFolderBrowser(t *testing.T) {
	_, h := setup(t)
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "visible"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, ".hidden"), 0o755); err != nil {
		t.Fatal(err)
	}
	var list struct {
		Path string     `json:"path"`
		Dirs []dirEntry `json:"dirs"`
	}
	if code := api(t, h, "GET", "/api/fs?path="+dir, "", &list); code != 200 {
		t.Fatalf("fs: %d", code)
	}
	if len(list.Dirs) != 1 || list.Dirs[0].Name != "visible" {
		t.Fatalf("dirs: %+v", list.Dirs)
	}
	if code := api(t, h, "GET", "/api/fs?path=relative", "", nil); code != 400 {
		t.Errorf("relative path: got %d", code)
	}
	b, _ := json.Marshal(map[string]string{"parent": dir, "name": "runners"})
	if code := api(t, h, "POST", "/api/fs/mkdir", string(b), nil); code != 200 {
		t.Fatalf("mkdir: %d", code)
	}
	if fi, err := os.Stat(filepath.Join(dir, "runners")); err != nil || !fi.IsDir() {
		t.Fatalf("mkdir did not create the folder: %v", err)
	}
	for _, name := range []string{"..", "a/b", `a\b`, ""} {
		b, _ := json.Marshal(map[string]string{"parent": dir, "name": name})
		if code := api(t, h, "POST", "/api/fs/mkdir", string(b), nil); code != 400 {
			t.Errorf("mkdir %q: got %d, want 400", name, code)
		}
	}
}

func TestServiceActionsSpawnHelper(t *testing.T) {
	s, h := setup(t)
	var got [][]string
	s.spawn = func(_ string, args ...string) error { got = append(got, args); return nil }
	for _, a := range []string{"install", "uninstall", "stop", "restart"} {
		if code := api(t, h, "POST", "/api/service/"+a, "", nil); code != http.StatusAccepted {
			t.Errorf("%s: %d", a, code)
		}
	}
	if code := api(t, h, "POST", "/api/service/format-disk", "", nil); code != 404 {
		t.Errorf("unknown action: %d", code)
	}
	want := "service install|service uninstall --keep-running|service stop|service restart"
	var parts []string
	for _, g := range got {
		parts = append(parts, strings.Join(g, " "))
	}
	if strings.Join(parts, "|") != want {
		t.Fatalf("spawned %q, want %q", parts, want)
	}
}

func TestRunnerLabels(t *testing.T) {
	_, h := setup(t)
	api(t, h, "POST", "/api/login/token", `{"token":"good-token"}`, nil)

	var wf struct {
		Sets []struct {
			Labels []string `json:"labels"`
			Files  []string `json:"files"`
		} `json:"sets"`
	}
	if code := api(t, h, "GET", "/api/workflow-labels?repo=octo/private", "", &wf); code != 200 || len(wf.Sets) != 1 ||
		strings.Join(wf.Sets[0].Labels, ",") != "self-hosted,heavy" || strings.Join(wf.Sets[0].Files, ",") != "ci.yml" {
		t.Fatalf("workflow labels: %d %+v", code, wf)
	}
	if code := api(t, h, "GET", "/api/workflow-labels?repo=octo/public", "", &wf); code != 200 || len(wf.Sets) != 0 {
		t.Fatalf("no workflows folder: %d %+v", code, wf)
	}

	var rr struct {
		Runners map[string][]ghRunnerView `json:"runners"`
		Errors  map[string]string         `json:"errors"`
	}
	if code := api(t, h, "POST", "/api/repo-runners", `{"repos":["octo/private","bad"]}`, &rr); code != 200 {
		t.Fatalf("repo runners: %d", code)
	}
	got := rr.Runners["octo/private"]
	if len(got) != 1 || got[0].ID != 7 || strings.Join(got[0].Custom, ",") != "light" || rr.Errors["bad"] == "" {
		t.Fatalf("repo runners: %+v", rr)
	}

	var e apiError
	if code := api(t, h, "PUT", "/api/runner-labels", `{"repo":"octo/private","runner_id":7,"labels":["two words"]}`, &e); code != 400 {
		t.Fatalf("bad label: %d %+v", code, e)
	}
	var lv struct {
		Labels []string `json:"labels"`
	}
	if code := api(t, h, "PUT", "/api/runner-labels", `{"repo":"octo/private","runner_id":7,"labels":["heavy","self-hosted"]}`, &lv); code != 200 ||
		strings.Join(lv.Labels, ",") != "self-hosted,heavy" {
		t.Fatalf("set labels: %d %+v", code, lv)
	}
	if code := api(t, h, "PUT", "/api/runner-labels", `{"repo":"octo/private","runner_id":99,"labels":["heavy"]}`, &e); code == 200 {
		t.Fatal("labels set on a runner that does not exist")
	}
}

func TestAddTaskScopes(t *testing.T) {
	s, h := setup(t)
	api(t, h, "POST", "/api/login/token", `{"token":"good-token"}`, nil)
	var e apiError
	if code := api(t, h, "POST", "/api/add", `{"repos":["octo/private"],"labels":["two words"]}`, &e); code != 400 {
		t.Fatalf("bad label accepted: %d %+v", code, e)
	}
	// Hold the queue for good so both adds stay listed as waiting and never
	// run against a fake GitHub that is gone.
	block := make(chan struct{})
	s.startTask(httptest.NewRecorder(), "hold", func(io.Writer) error { <-block; return nil })
	for _, repo := range []string{"octo/private", "octo/public"} {
		if code := api(t, h, "POST", "/api/add", `{"repos":["`+repo+`"],"labels":["heavy"],"allow_public":true}`, nil); code != 202 && code != 200 {
			t.Fatalf("add %s: %d", repo, code)
		}
	}
	var tasks []taskView
	api(t, h, "GET", "/api/tasks", "", &tasks)
	scoped := map[string]string{}
	for _, tv := range tasks {
		for _, sc := range tv.Scopes {
			scoped[sc] = tv.State + " " + tv.Title
		}
	}
	if !strings.HasPrefix(scoped["octo/private"], "queued Add a runner to octo/private (heavy)") || !strings.HasPrefix(scoped["octo/public"], "queued") {
		t.Fatalf("task scopes: %v", scoped)
	}
}

func TestSeveralRunnersPerRepo(t *testing.T) {
	s, h := setup(t)
	api(t, h, "POST", "/api/login/token", `{"token":"good-token"}`, nil)
	for _, r := range []store.Runner{
		{ID: "octo__private", Kind: store.KindRepo, Owner: "octo", Repo: "private", Name: "laptop"},
		{ID: "octo__private__laptop-2", Kind: store.KindRepo, Owner: "octo", Repo: "private", Name: "laptop-2", Labels: []string{"light"}},
	} {
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	var repos struct {
		Repos []repoView `json:"repos"`
	}
	api(t, h, "GET", "/api/repos", "", &repos)
	ids := map[string]string{}
	for _, r := range repos.Repos {
		ids[r.FullName] = r.RunnerID + " " + strings.Join(r.RunnerIDs, ",")
	}
	if ids["octo/private"] != "octo__private octo__private,octo__private__laptop-2" || ids["octo/public"] != " " {
		t.Fatalf("runner ids: %v", ids)
	}

	var e apiError
	if code := api(t, h, "POST", "/api/add", `{"repos":["octo/private","octo/public"],"another":true,"name":"x"}`, &e); code != 400 {
		t.Fatalf("one name for two repos: %d %+v", code, e)
	}
	block := make(chan struct{})
	s.startTask(httptest.NewRecorder(), "hold", func(io.Writer) error { <-block; return nil })
	// Two clicks on Add runner in a row both queue.
	for _, body := range []string{`{"repos":["octo/private"],"another":true,"labels":["light"]}`, `{"repos":["octo/private"],"another":true,"name":"small"}`} {
		if code := api(t, h, "POST", "/api/add", body, nil); code != 202 && code != 200 {
			t.Fatalf("add %s: %d", body, code)
		}
	}
	var tasks []taskView
	api(t, h, "GET", "/api/tasks", "", &tasks)
	var titles []string
	for _, tv := range tasks {
		// "hold" itself may still be queued until the worker goroutine picks it up.
		if tv.State == "queued" && tv.Title != "hold" {
			titles = append(titles, tv.Title)
		}
	}
	sort.Strings(titles)
	if strings.Join(titles, "|") != "Add another runner to octo/private (light)|Add another runner to octo/private named small" {
		t.Fatalf("queued: %q", titles)
	}
}
