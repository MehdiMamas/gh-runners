package store

import (
	"encoding/json"
	"testing"
	"time"
)

func TestIDFor(t *testing.T) {
	if got := IDFor(KindRepo, "MehdiMamas", "spotter"); got != "MehdiMamas__spotter" {
		t.Errorf("repo id = %q", got)
	}
	if got := IDFor(KindOrg, "my-org", ""); got != "org__my-org" {
		t.Errorf("org id = %q", got)
	}
	if got := IDFor(KindRepo, "o", "a b/c"); got != "o__a-b-c" {
		t.Errorf("unsafe id = %q", got)
	}
}

func TestDurationJSON(t *testing.T) {
	var v struct{ D Duration }
	if err := json.Unmarshal([]byte(`{"D":"1h30m"}`), &v); err != nil || v.D.Duration != 90*time.Minute {
		t.Fatalf("unmarshal: %v %v", v.D, err)
	}
	b, _ := json.Marshal(v)
	if string(b) != `{"D":"1h30m0s"}` {
		t.Errorf("marshal = %s", b)
	}
	if err := json.Unmarshal([]byte(`{"D":""}`), &v); err != nil || v.D.Duration != 0 {
		t.Errorf("empty: %v %v", v.D, err)
	}
	if err := json.Unmarshal([]byte(`{"D":"soon"}`), &v); err == nil {
		t.Error("want error for bad duration")
	}
	if (Duration{}).Or(time.Minute) != time.Minute || (Duration{time.Second}).Or(time.Minute) != time.Second {
		t.Error("Or")
	}
}

func TestSaveLoadList(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	r := Runner{ID: IDFor(KindRepo, "o", "r"), Kind: KindRepo, Owner: "o", Repo: "r", Name: "box", Labels: []string{"x"}}
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	got, err := Load(r.ID)
	if err != nil || got.Name != "box" || got.Scope() != "o/r" {
		t.Fatalf("load: %+v %v", got, err)
	}
	all, err := List()
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %v %v", all, err)
	}
	if got.Configured() {
		t.Error("runner without .runner file should not be configured")
	}
	cfg, err := LoadConfig()
	if err != nil || len(cfg.Labels) == 0 {
		t.Errorf("default config: %+v %v", cfg, err)
	}
}
