package ops

import (
	"strings"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func repo(full string, private, admin bool) github.Repo {
	var r github.Repo
	r.FullName = full
	r.Owner.Login, r.Name, _ = strings.Cut(full, "/")
	r.Private = private
	r.Permissions.Admin = admin
	return r
}

func TestRepoFilter(t *testing.T) {
	archived := repo("me/old", true, true)
	archived.Archived = true
	fork := repo("me/fork", true, true)
	fork.Fork = true
	cases := []struct {
		f    RepoFilter
		r    github.Repo
		want bool
		why  string
	}{
		{RepoFilter{}, repo("me/app", true, true), true, ""},
		{RepoFilter{}, repo("Me/App", true, true), true, ""},
		{RepoFilter{}, repo("other/app", true, true), false, "owner not selected"},
		{RepoFilter{Owners: []string{"me", "org"}}, repo("org/app", true, true), true, ""},
		{RepoFilter{}, repo("me/pub", false, true), false, "public"},
		{RepoFilter{IncludePublic: true}, repo("me/pub", false, true), true, ""},
		{RepoFilter{}, repo("me/ro", true, false), false, "no admin rights"},
		{RepoFilter{}, archived, false, "archived"},
		{RepoFilter{}, fork, false, "fork"},
		{RepoFilter{IncludeForks: true}, fork, true, ""},
		{RepoFilter{Include: []string{"api-*"}}, repo("me/api-server", true, true), true, ""},
		{RepoFilter{Include: []string{"api-*"}}, repo("me/web", true, true), false, "not in include"},
		{RepoFilter{Exclude: []string{"me/web"}}, repo("me/web", true, true), false, "excluded"},
		{RepoFilter{Exclude: []string{"*/WEB"}}, repo("me/web", true, true), false, "excluded"},
	}
	for _, c := range cases {
		got, why := c.f.Match(c.r, "me")
		if got != c.want || why != c.why {
			t.Errorf("%+v on %s = %v %q; want %v %q", c.f, c.r.FullName, got, why, c.want, c.why)
		}
	}
}

func TestLabels(t *testing.T) {
	defer func(f func() bool) { DockerAvailable = f }(DockerAvailable)
	DockerAvailable = func() bool { return false }
	cfg := store.Config{Labels: []string{"gh-runners", "GPU"}, RunnerName: "Mehdi's MacBook Pro"}
	got := strings.Join(Labels(cfg, []string{"gpu", " big ", ""}), ",")
	if want := "gh-runners,GPU,mehdi-s-macbook-pro,big"; got != want {
		t.Errorf("Labels = %s, want %s", got, want)
	}
	if MachineName(cfg) != "Mehdi's MacBook Pro" {
		t.Error("MachineName should use RunnerName")
	}
}

func TestLabelsDocker(t *testing.T) {
	defer func(f func() bool) { DockerAvailable = f }(DockerAvailable)
	cfg := store.Config{Labels: []string{"gh-runners", "Docker"}, RunnerName: "Box"}
	DockerAvailable = func() bool { return true }
	if got := strings.Join(Labels(cfg, []string{"big"}), ","); got != "gh-runners,box,big,docker" {
		t.Errorf("with docker: %s", got)
	}
	DockerAvailable = func() bool { return false }
	if got := strings.Join(Labels(cfg, []string{"big"}), ","); got != "gh-runners,box,big" {
		t.Errorf("without docker: %s", got)
	}
}

func TestWithDockerLabel(t *testing.T) {
	cases := []struct {
		in   []string
		has  bool
		want string
	}{
		{[]string{"heavy", "gh-runners"}, true, "heavy,gh-runners,docker"},
		{[]string{"heavy", "docker", "gh-runners"}, true, "heavy,gh-runners,docker"},
		{[]string{"heavy", "Docker"}, false, "heavy"},
		{nil, false, ""},
	}
	for _, c := range cases {
		if got := strings.Join(WithDockerLabel(c.in, c.has), ","); got != c.want {
			t.Errorf("WithDockerLabel(%v, %v) = %q, want %q", c.in, c.has, got, c.want)
		}
	}
}
