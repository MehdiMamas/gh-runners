package github

import "testing"

func TestParseScope(t *testing.T) {
	cases := []struct {
		in   string
		org  bool
		want Scope
		err  bool
	}{
		{"MehdiMamas/spotter", false, Scope{"MehdiMamas", "spotter"}, false},
		{"https://github.com/MehdiMamas/spotter.git", false, Scope{"MehdiMamas", "spotter"}, false},
		{"git@github.com:MehdiMamas/spotter.git", false, Scope{"MehdiMamas", "spotter"}, false},
		{"ssh://git@github.com/MehdiMamas/spotter", false, Scope{"MehdiMamas", "spotter"}, false},
		{"github.com/MehdiMamas/spotter/", false, Scope{"MehdiMamas", "spotter"}, false},
		{"my-org", true, Scope{Owner: "my-org"}, false},
		{"https://github.com/my-org", true, Scope{Owner: "my-org"}, false},
		{"just-a-name", false, Scope{}, true},
		{"a/b/c", false, Scope{}, true},
		{"a/b", true, Scope{}, true},
		{"", true, Scope{}, true},
	}
	for _, c := range cases {
		got, err := ParseScope(c.in, c.org)
		if (err != nil) != c.err || got != c.want {
			t.Errorf("ParseScope(%q, %v) = %+v, %v; want %+v, err=%v", c.in, c.org, got, err, c.want, c.err)
		}
	}
}

func TestScopeURLs(t *testing.T) {
	r := Scope{"o", "r"}
	if r.URL() != "https://github.com/o/r" || r.apiBase() != "/repos/o/r/actions/runners" || r.IsOrg() {
		t.Errorf("repo scope: %s %s", r.URL(), r.apiBase())
	}
	o := Scope{Owner: "o"}
	if o.URL() != "https://github.com/o" || o.apiBase() != "/orgs/o/actions/runners" || !o.IsOrg() {
		t.Errorf("org scope: %s %s", o.URL(), o.apiBase())
	}
}

func TestReleaseSHA(t *testing.T) {
	body := "notes\n- win-x64 <!-- BEGIN SHA win-x64 -->abc123<!-- END SHA win-x64 -->\n" +
		"- osx-arm64 <!-- BEGIN SHA osx-arm64 --> def456 <!-- END SHA osx-arm64 -->\n"
	if got := releaseSHA(body, "win-x64"); got != "abc123" {
		t.Errorf("win-x64 = %q", got)
	}
	if got := releaseSHA(body, "osx-arm64"); got != "def456" {
		t.Errorf("osx-arm64 = %q", got)
	}
	if got := releaseSHA(body, "linux-arm"); got != "" {
		t.Errorf("linux-arm = %q", got)
	}
}

func TestAssetRe(t *testing.T) {
	for name, want := range map[string]bool{
		"actions-runner-win-x64-2.338.0.zip":                  true,
		"actions-runner-osx-arm64-2.338.0.tar.gz":             true,
		"actions-runner-linux-arm-2.338.0.tar.gz":             true,
		"actions-runner-linux-x64-2.338.0-noexternals.tar.gz": false,
		"actions-runner-win-arm64-2.338.0.zip.sha256":         false,
	} {
		if got := assetRe.MatchString(name); got != want {
			t.Errorf("%s: %v, want %v", name, got, want)
		}
	}
}
