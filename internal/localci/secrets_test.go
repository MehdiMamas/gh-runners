package localci

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

const secretWF = `jobs:
  up:
    runs-on: ubuntu-latest
    env:
      TOK: ${{ secrets.UP_TOKEN }}
      PLAIN: x
    steps:
      - uses: actions/setup-node@v4
        with:
          node-version: ${{secrets.NODE_V}}
      - env:
          STEP_KEY: ${{  secrets.STEP_KEY  }}
        run: echo ${{ secrets.UP_TOKEN }} ${{ secrets.KEYFILE }}
`

func TestApplySecretsSubstitutes(t *testing.T) {
	wf, err := ParseWorkflow([]byte(secretWF))
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "key.p8")
	sec := map[string]store.Secret{
		"UP_TOKEN": {Value: "tok-123"}, "NODE_V": {Value: "20"}, "STEP_KEY": {Value: "sk-9"}, "KEYFILE": {File: key},
	}
	j, env := ApplySecrets(wf.Jobs["up"], sec)
	if j.Env["TOK"] != "tok-123" || j.Env["PLAIN"] != "x" {
		t.Errorf("job env %v", j.Env)
	}
	if j.Steps[0].With["node-version"] != "20" {
		t.Errorf("with %v", j.Steps[0].With)
	}
	if j.Steps[1].Env["STEP_KEY"] != "sk-9" {
		t.Errorf("step env %v", j.Steps[1].Env)
	}
	if want := "echo tok-123 " + key; j.Steps[1].Run != want {
		t.Errorf("run %q, want %q", j.Steps[1].Run, want)
	}
	if len(env) != 2 || env["TOK"] != "tok-123" || env["STEP_KEY"] != "sk-9" {
		t.Errorf("secret env %v", env)
	}
	if strings.Contains(wf.Jobs["up"].Steps[1].Run, "tok-123") || wf.Jobs["up"].Env["TOK"] != "${{ secrets.UP_TOKEN }}" {
		t.Error("the original job was changed")
	}
}

func decideWith(t *testing.T, g *SecretGate) Result {
	t.Helper()
	wf, err := ParseWorkflow([]byte(secretWF))
	if err != nil {
		t.Fatal(err)
	}
	return decideJob(wf, wf.Jobs["up"], nil, nil, false, g)
}

func TestSecretJobDecisions(t *testing.T) {
	all := map[string]store.Secret{"UP_TOKEN": {Value: "a"}, "NODE_V": {Value: "20"}, "STEP_KEY": {Value: "k"}, "KEYFILE": {File: "/x"}}
	if r := decideWith(t, &SecretGate{OK: true, Secrets: all}); r.Status != "" {
		t.Errorf("all set: %v %s", r.Status, r.Reason)
	}
	partial := map[string]store.Secret{"UP_TOKEN": {Value: "a"}, "NODE_V": {Value: "20"}, "KEYFILE": {File: "/x"}}
	r := decideWith(t, &SecretGate{OK: true, Secrets: partial})
	if r.Status != Skipped || r.Reason != "needs secrets: missing STEP_KEY" {
		t.Errorf("missing: %v %q", r.Status, r.Reason)
	}
	all["GITHUB_TOKEN"] = store.Secret{Value: "never"}
	if r := decideWith(t, &SecretGate{OK: true, Secrets: map[string]store.Secret{"GITHUB_TOKEN": {Value: "never"}}}); !strings.Contains(r.Reason, "missing") {
		t.Errorf("GITHUB_TOKEN must not count as set: %q", r.Reason)
	}
	r = decideWith(t, &SecretGate{OK: false, Allow: []string{"me"}, SHA: "abcdef1234", Secrets: all})
	if r.Status != Skipped || r.Reason != "needs secrets; runs after me approves commit abcdef1" {
		t.Errorf("not approved: %v %q", r.Status, r.Reason)
	}
	if r := decideWith(t, nil); r.Status != Skipped {
		t.Errorf("no gate: %v", r.Status)
	}
}

func TestHeadApproved(t *testing.T) {
	rv := func(login, state, sha string) github.Review {
		return github.Review{Login: login, State: state, CommitID: sha}
	}
	allow := []string{"me"}
	cases := []struct {
		name string
		rs   []github.Review
		want bool
	}{
		{"none", nil, false},
		{"older commit", []github.Review{rv("me", "APPROVED", "old")}, false},
		{"head by allowed", []github.Review{rv("me", "APPROVED", "head")}, true},
		{"head by allowed, other case", []github.Review{rv("Me", "APPROVED", "head")}, true},
		{"head by someone else", []github.Review{rv("bob", "APPROVED", "head")}, false},
		{"approval then change request", []github.Review{rv("me", "APPROVED", "head"), rv("me", "CHANGES_REQUESTED", "head")}, false},
		{"comment keeps approval", []github.Review{rv("me", "APPROVED", "head"), rv("me", "COMMENTED", "head")}, true},
		{"dismissed", []github.Review{rv("me", "APPROVED", "head"), rv("me", "DISMISSED", "head")}, false},
	}
	for _, c := range cases {
		if got := HeadApproved(c.rs, "head", allow); got != c.want {
			t.Errorf("%s: got %v", c.name, got)
		}
	}
	if got := ApprovalLogins(store.CIRepo{Allow: []string{"collaborators", "alice"}}, "me"); len(got) != 1 || got[0] != "alice" {
		t.Errorf("collaborators must not approve: %v", got)
	}
	if got := ApprovalLogins(store.CIRepo{}, "me"); len(got) != 1 || got[0] != "me" {
		t.Errorf("default %v", got)
	}
}

func TestSecretJobRunsWithValueAndMasksIt(t *testing.T) {
	wf, err := ParseWorkflow([]byte("jobs:\n  j:\n    runs-on: ubuntu-latest\n    env:\n      API_KEY: ${{ secrets.K }}\n    steps:\n      - run: |\n          test \"$API_KEY\" = \"s3cr3t-value\"\n          echo key is ${{ secrets.K }} and $API_KEY\n"))
	if err != nil {
		t.Fatal(err)
	}
	sec := map[string]store.Secret{"K": {Value: "s3cr3t-value"}}
	j, env := ApplySecrets(wf.Jobs["j"], sec)
	dir := t.TempDir()
	res := LocalExecutor{}.RunJob(context.Background(), JobSpec{
		Name: "j", Job: j, Tarball: makeTarball(t, 9000), RunDir: filepath.Join(dir, "j"), Log: filepath.Join(dir, "j.log"),
		SecretEnv: env, SecretValues: secretValues(wf.Jobs["j"], sec),
	})
	b, _ := os.ReadFile(res.Log)
	if res.Status != Pass {
		t.Fatalf("got %v %q\n%s", res.Status, res.Reason, b)
	}
	if strings.Contains(string(b), "s3cr3t-value") || !strings.Contains(string(b), "key is *** and ***") {
		t.Errorf("log not masked:\n%s", b)
	}
}
