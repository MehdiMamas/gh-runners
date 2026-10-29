package fleet

// Running CI jobs on fleet members and on the account's other PCs.
//
// The sender (the host, or a PC of the account) sends a job (repo, sha, the
// parsed job) as a "ci" request. A member downloads the source tarball
// through the host's broker, so it needs no GitHub token; a peer downloads
// it with its own sign-in. Either runs the job with the local executor and
// its own emulator lock, streams the log back in one-way "cilog" frames and
// answers the request with the result. The sender stores the log under the
// run directory.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/localci"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

const (
	kindCI    = "ci"       // sender → runner: run a CI job
	tCILog    = "cilog"    // runner → sender: a chunk of a job's log (Path is the job id)
	tCICancel = "cicancel" // sender → runner: stop a job (Path is the job id)

	tarballChunk = 4 << 20
	logChunk     = 256 << 10
)

// CIInfo is what a member says about its ability to run CI jobs. A member
// that sends none gets no jobs.
type CIInfo struct {
	Tools      []string            `json:"tools"`
	EmulatorOK bool                `json:"emulator_ok"`
	Active     []localci.ActiveJob `json:"active,omitempty"` // jobs running there now
}

// ciJob is the request body of a "ci" request.
type ciJob struct {
	JobID  string         `json:"job_id"`
	Name   string         `json:"name"`
	Repo   string         `json:"repo"` // owner/repo
	SHA    string         `json:"sha"`
	Job    *localci.Job   `json:"job"`
	RunEnv localci.RunEnv `json:"run_env"`
}

// ciResult is the response body of a "ci" request.
type ciResult struct {
	Status     localci.Status `json:"status"`
	Reason     string         `json:"reason"`
	DurationMS int64          `json:"duration_ms"`
}

var tarballRe = regexp.MustCompile(`^/repos/(` + name + `)/(` + name + `)/tarball/([0-9a-fA-F]{7,40})$`)

// ---- host side ----

// CIMachines lists the connected members that can run CI jobs.
func (h *Host) CIMachines() []localci.Machine {
	ms, err := h.Machines()
	if err != nil {
		return nil
	}
	var out []localci.Machine
	for _, m := range ms {
		if !m.Online || m.Info.CI == nil {
			continue
		}
		out = append(out, localci.Machine{
			ID: m.ID, Name: m.Info.Hostname, OS: m.Info.OS, Arch: m.Info.Arch,
			Tools: m.Info.CI.Tools, EmulatorOK: m.Info.CI.EmulatorOK,
		})
	}
	return out
}

func (h *Host) ciRepoAllowed(repo string) bool {
	if h.CIRepos == nil {
		return false
	}
	for _, r := range h.CIRepos() {
		if strings.EqualFold(r, repo) {
			return true
		}
	}
	return false
}

// serveTarball answers a member's source download. It refuses repos that
// are not in the CI settings, downloads the archive once with the host's
// token and hands it out in chunks.
func (h *Host) serveTarball(f frame, m []string) frame {
	if !h.ciRepoAllowed(m[1] + "/" + m[2]) {
		return githubError(http.StatusForbidden, "the main host does not hand out the source of "+m[1]+"/"+m[2])
	}
	off := int64(0)
	if v := f.Header["X-Chunk-Offset"]; v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return githubError(http.StatusBadRequest, "bad chunk offset")
		}
		off = n
	}
	path, err := h.cachedTarball(m[1], m[2], strings.ToLower(m[3]))
	if err != nil {
		return githubError(http.StatusBadGateway, err.Error())
	}
	file, err := os.Open(path)
	if err != nil {
		return githubError(http.StatusBadGateway, err.Error())
	}
	defer file.Close()
	st, err := file.Stat()
	if err != nil {
		return githubError(http.StatusBadGateway, err.Error())
	}
	buf := make([]byte, tarballChunk)
	n, err := file.ReadAt(buf, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return githubError(http.StatusBadGateway, err.Error())
	}
	return frame{Status: http.StatusOK, Body: buf[:n], Header: map[string]string{
		"Content-Type": "application/gzip", "X-Total": strconv.FormatInt(st.Size(), 10),
	}}
}

func (h *Host) cachedTarball(owner, repo, sha string) (string, error) {
	h.tarMu.Lock()
	defer h.tarMu.Unlock()
	dir := filepath.Join(h.Dir, "tarballs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > 2*time.Hour {
				_ = os.Remove(filepath.Join(dir, e.Name()))
			}
		}
	}
	dest := filepath.Join(dir, strings.ToLower(owner+"-"+repo+"-"+sha)+".tgz")
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	token := ""
	if h.Token != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		token, _ = h.Token(ctx)
		cancel()
	}
	if token == "" {
		return "", errors.New("the main host is not signed in to GitHub")
	}
	c := github.New(token)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	tmp := dest + ".part"
	if err := c.DownloadTarball(ctx, owner, repo, sha, tmp); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("the main host could not download the source: %w", err)
	}
	return dest, os.Rename(tmp, dest)
}

// ciLogSet is where the logs of the jobs a machine sent out go, by job id.
type ciLogSet struct {
	mu sync.Mutex
	m  map[string]io.Writer
}

func (s *ciLogSet) add(id string, w io.Writer) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.m == nil {
		s.m = map[string]io.Writer{}
	}
	s.m[id] = w
}

func (s *ciLogSet) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}

func (s *ciLogSet) write(id string, b []byte) {
	s.mu.Lock()
	w := s.m[id]
	s.mu.Unlock()
	if w != nil {
		_, _ = w.Write(b)
	}
}

// RemoteExecutor runs jobs on one fleet member. It implements
// localci.Executor.
type RemoteExecutor struct {
	Host     *Host
	MemberID string
}

// RunJob sends the job to the member and waits for the result. The log is
// written to spec.Log as it arrives. A member that drops mid-job fails the
// job with "machine went offline".
func (r RemoteExecutor) RunJob(ctx context.Context, spec localci.JobSpec) localci.Result {
	h := r.Host
	h.mu.Lock()
	s := h.sessions[r.MemberID]
	h.mu.Unlock()
	if s == nil {
		return runRemote(ctx, spec, nil, &h.ciLogs)
	}
	return runRemote(ctx, spec, s.c, &h.ciLogs)
}

// runRemote sends the job down c and waits for the result; the log is
// written to spec.Log as it arrives through logs. A nil c, or a link that
// drops mid-job, fails the job with "machine went offline".
func runRemote(ctx context.Context, spec localci.JobSpec, c *conn, logs *ciLogSet) localci.Result {
	start := time.Now()
	res := localci.Result{Job: spec.Name, Log: spec.Log}
	fail := func(reason string) localci.Result {
		res.Status, res.Reason, res.Duration = localci.Fail, reason, time.Since(start)
		return res
	}
	if c == nil {
		return fail("machine went offline")
	}
	if err := os.MkdirAll(filepath.Dir(spec.Log), 0o755); err != nil {
		return fail(err.Error())
	}
	lf, err := os.Create(spec.Log)
	if err != nil {
		return fail(err.Error())
	}
	defer lf.Close()
	id := randomHex(8)
	logs.add(id, lf)
	defer logs.remove(id)
	body, err := json.Marshal(ciJob{
		JobID: id, Name: spec.Name, Repo: spec.RunEnv.Repo, SHA: spec.RunEnv.SHA, Job: spec.Job, RunEnv: spec.RunEnv,
	})
	if err != nil {
		return fail(err.Error())
	}
	resp, err := c.call(ctx, frame{Kind: kindCI, Body: body})
	switch {
	case ctx.Err() != nil:
		_ = c.send(frame{Type: tCICancel, Path: id})
		res.Status, res.Reason, res.Duration = localci.Skipped, "cancelled", time.Since(start)
		return res
	case err != nil:
		fmt.Fprintln(lf, "::error machine went offline:", err)
		return fail("machine went offline")
	case resp.Error != "":
		return fail(resp.Error)
	case resp.Status != http.StatusOK:
		return fail(fmt.Sprintf("the machine refused the job (HTTP %d)", resp.Status))
	}
	var cr ciResult
	if err := json.Unmarshal(resp.Body, &cr); err != nil {
		return fail("bad result from the machine")
	}
	res.Status, res.Reason, res.Duration = cr.Status, cr.Reason, time.Duration(cr.DurationMS)*time.Millisecond
	return res
}

// ---- runner side ----

// ciRunner is the side that runs jobs: a member for the main host, a peer
// for the account's other PCs.
type ciRunner struct {
	// Dir turns CI jobs on when set and is where they work. Tools and
	// EmulatorOK say what this machine can run.
	Dir        string
	Tools      func() []string
	EmulatorOK func() bool
	// Fetch downloads the source of repo at sha into dest.
	Fetch func(ctx context.Context, c *conn, repo, sha, dest string) error
	// For names who sent the job, on this machine's card ("the main host").
	For string
	// Reload, when set, runs when a job starts or ends.
	Reload func()

	mu   sync.Mutex
	jobs map[string]context.CancelFunc // running jobs by id
}

func (r *ciRunner) reload() {
	if r.Reload != nil {
		r.Reload()
	}
}

// busy reports how many jobs run now.
func (r *ciRunner) busy() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.jobs)
}

// info is what this machine advertises: nil when CI is not turned on.
func (r *ciRunner) info() *CIInfo {
	if r.Dir == "" {
		return nil
	}
	i := CIInfo{}
	if r.Tools != nil {
		i.Tools = r.Tools()
	}
	if r.EmulatorOK != nil {
		i.EmulatorOK = r.EmulatorOK()
	}
	return &i
}

func ciError(status int, msg string) frame {
	return frame{Status: status, Error: msg}
}

func (r *ciRunner) run(ctx context.Context, c *conn, f frame) frame {
	if r.Dir == "" {
		return ciError(http.StatusForbidden, "this PC does not run CI jobs")
	}
	var job ciJob
	if err := json.Unmarshal(f.Body, &job); err != nil || job.Job == nil {
		return ciError(http.StatusBadRequest, "bad job")
	}
	mt := tarballRe.FindStringSubmatch("/repos/" + job.Repo + "/tarball/" + job.SHA)
	if mt == nil || !regexp.MustCompile(`^[0-9a-f]{4,32}$`).MatchString(job.JobID) {
		return ciError(http.StatusBadRequest, "bad repo, sha or job id")
	}
	jctx, cancel := context.WithCancel(ctx)
	defer cancel()
	r.mu.Lock()
	if r.jobs == nil {
		r.jobs = map[string]context.CancelFunc{}
	}
	r.jobs[job.JobID] = cancel
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.jobs, job.JobID)
		r.mu.Unlock()
	}()
	go func() {
		select {
		case <-c.done:
			cancel()
		case <-jctx.Done():
		}
	}()

	// The sender sees the job on this machine's card at once, not only
	// with the next status.
	host, _ := os.Hostname()
	done := localci.StartActive(localci.ActiveJob{Repo: job.Repo, PR: localci.PRFromRef(job.RunEnv.Ref), Job: job.Name, Machine: host, For: r.For})
	r.reload()
	defer func() { done(); r.reload() }()

	dir := filepath.Join(r.Dir, "jobs", job.JobID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return ciError(http.StatusInternalServerError, err.Error())
	}
	defer removeJobDir(dir)
	localci.WriteOwner(dir)
	logPath := filepath.Join(dir, "job.log")
	tarball := filepath.Join(dir, "source.tgz")

	tailDone := make(chan struct{})
	stopTail := make(chan struct{})
	go r.tailLog(c, job.JobID, logPath, stopTail, tailDone)
	finish := func(res localci.Result) frame {
		close(stopTail)
		<-tailDone
		b, _ := json.Marshal(ciResult{Status: res.Status, Reason: res.Reason, DurationMS: res.Duration.Milliseconds()})
		return frame{Status: http.StatusOK, Body: b}
	}

	if err := r.Fetch(jctx, c, job.Repo, job.SHA, tarball); err != nil {
		_ = os.WriteFile(logPath, []byte("::error download source: "+err.Error()+"\n"), 0o644)
		return finish(localci.Result{Status: localci.Fail, Reason: "download source: " + err.Error()})
	}
	// This machine builds its own environment: the sender's PATH and
	// variables never reach it.
	env := localci.ScrubEnv(os.Environ(), job.RunEnv, nil)
	locks := &localci.Locks{Dir: filepath.Join(r.Dir, "locks")}
	if _, emu := job.Job.Requires(); emu {
		unlock, err := locks.Lock("emulator")
		if err != nil {
			return finish(localci.Result{Status: localci.Fail, Reason: "lock: " + err.Error()})
		}
		defer unlock()
	}
	started := time.Now()
	res := localci.LocalExecutor{}.RunJob(jctx, localci.JobSpec{
		Name: job.Name, Job: job.Job, Tarball: tarball, RunDir: dir, Log: logPath, Env: env, RunEnv: job.RunEnv,
	})
	if jctx.Err() != nil && res.Status == localci.Fail {
		res.Reason = "cancelled"
	}
	keepRemoteLog(job, r.For, logPath, res, started)
	return finish(res)
}

// keepRemoteLog keeps the log of a job this machine ran for another one
// under ~/.gh-runners/ci/<owner-repo>/ and adds it to ci/history.json, so
// the job can be looked at here after the job folder is removed. A failure
// to keep it is ignored: it never fails the job.
func keepRemoteLog(job ciJob, from, logPath string, res localci.Result, started time.Time) {
	repo := strings.ReplaceAll(job.Repo, "/", "-")
	if repo == "" {
		repo = "unknown"
	}
	pr := localci.PRFromRef(job.RunEnv.Ref)
	sha := job.SHA
	if len(sha) > 7 {
		sha = sha[:7]
	}
	if sha == "" {
		sha = "unknown"
	}
	runDir := filepath.Join(store.Home(), "ci", repo, fmt.Sprintf("pr%d-%s-%d-remote", pr, sha, started.Unix()))
	if err := os.MkdirAll(filepath.Join(runDir, "logs"), 0o755); err != nil {
		return
	}
	if b, err := os.ReadFile(logPath); err == nil {
		_ = os.WriteFile(filepath.Join(runDir, "logs", strings.NewReplacer("/", "_", "\\", "_").Replace(job.Name)+".log"), b, 0o644)
	}
	if from == "" {
		from = "unknown"
	}
	_ = localci.AddHistory(localci.HistoryRun{
		ID: strconv.FormatInt(started.UnixNano(), 36), Repo: job.Repo, PR: pr, SHA: job.SHA,
		Title: "remote from " + from, Started: started, Ended: time.Now(), Dir: runDir,
		Results: []localci.Result{res}, Passed: res.Status == localci.Pass,
		Outcome: localci.Outcome{State: localci.NotPosted, Reason: "Ran here for " + from + "; the sender posts the result."},
	})
}

// removeJobDir retries because Windows keeps the job log locked for a
// moment after a cancelled job's processes are killed.
func removeJobDir(dir string) {
	for i := 0; i < 50 && os.RemoveAll(dir) != nil; i++ {
		time.Sleep(100 * time.Millisecond)
	}
}

// PruneJobDirs removes each job folder under jobsDir that running says is
// not running, and returns how many it removed. Folders are left over when
// removeJobDir gave up because a detached process (a Gradle daemon) held
// files in them.
func PruneJobDirs(jobsDir string, running func(id string) bool) int {
	entries, err := os.ReadDir(jobsDir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() || running(e.Name()) {
			continue
		}
		if os.RemoveAll(filepath.Join(jobsDir, e.Name())) == nil {
			n++
		}
	}
	return n
}

// ciHandler wraps handleAPI: "ci" requests run a job, the rest are
// dashboard calls.
func (m *Member) ciHandler(ctx context.Context, c *conn) func(frame) frame {
	return func(f frame) frame {
		if f.Kind == kindCI {
			return m.ciRun().run(ctx, c, f)
		}
		return m.handleAPI(f)
	}
}

// fetchViaHost downloads the source through the main host in chunks.
func fetchViaHost(ctx context.Context, c *conn, repo, sha, dest string) error {
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()
	var off int64
	for {
		r, err := c.call(ctx, frame{Kind: kindGitHub, Method: http.MethodGet, Path: "/repos/" + repo + "/tarball/" + sha,
			Header: map[string]string{"X-Chunk-Offset": strconv.FormatInt(off, 10)}})
		if err != nil {
			return err
		}
		if r.Status != http.StatusOK {
			msg := strings.TrimSpace(string(r.Body))
			var e struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(r.Body, &e) == nil && e.Message != "" {
				msg = e.Message
			}
			return fmt.Errorf("HTTP %d: %s", r.Status, msg)
		}
		total, _ := strconv.ParseInt(r.Header["X-Total"], 10, 64)
		if _, err := out.Write(r.Body); err != nil {
			return err
		}
		off += int64(len(r.Body))
		if off >= total || len(r.Body) == 0 {
			if off != total {
				return errors.New("source download ended early")
			}
			return nil
		}
	}
}

// tailLog sends the growing log file to the host until stop closes, then
// once more so nothing is lost.
func (r *ciRunner) tailLog(c *conn, id, path string, stop <-chan struct{}, done chan<- struct{}) {
	defer close(done)
	var off int64
	flush := func() {
		f, err := os.Open(path)
		if err != nil {
			return
		}
		defer f.Close()
		for {
			buf := make([]byte, logChunk)
			n, _ := f.ReadAt(buf, off)
			if n == 0 {
				return
			}
			if c.send(frame{Type: tCILog, Path: id, Body: buf[:n]}) != nil {
				return
			}
			off += int64(n)
		}
	}
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-stop:
			flush()
			return
		case <-c.done:
			return
		case <-t.C:
			flush()
		}
	}
}

// cancel stops a running job; it reports nothing if the job is gone.
func (r *ciRunner) cancel(id string) {
	r.mu.Lock()
	cancel := r.jobs[id]
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
