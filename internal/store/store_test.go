package store

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
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

func TestRunnersDirAndUISecret(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	if RunnersDir() != DefaultRunnersDir() {
		t.Fatalf("RunnersDir without config = %s", RunnersDir())
	}
	other := filepath.Join(t.TempDir(), "runners")
	cfg := DefaultConfig()
	cfg.RunnersDir = other
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if RunnersDir() != other {
		t.Fatalf("RunnersDir = %s, want %s", RunnersDir(), other)
	}
	cfg.RunnersDir = ""
	if err := SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if RunnersDir() != DefaultRunnersDir() {
		t.Fatalf("RunnersDir after reset = %s", RunnersDir())
	}

	a, err := UISecret()
	if err != nil || len(a) != 48 {
		t.Fatalf("UISecret = %q, %v", a, err)
	}
	b, _ := UISecret()
	if a != b {
		t.Fatalf("UISecret changed between calls: %s then %s", a, b)
	}
}

// A reader must never miss a runner while its metadata is being rewritten
// (on Windows the rename and an open reader block each other).
func TestConcurrentSaveLoad(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	r := Runner{ID: "a", Kind: KindRepo, Owner: "o", Repo: "a", Name: "n"}
	if err := Save(r); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	loadErrs := make(chan error, 1)
	go func() {
		defer close(loadErrs)
		for {
			select {
			case <-done:
				return
			default:
			}
			if rs, err := List(); err != nil || len(rs) != 1 {
				loadErrs <- fmt.Errorf("list: %v, %d runners", err, len(rs))
				return
			}
		}
	}()
	for i := 0; i < 200; i++ {
		r.Disabled = i%2 == 0
		if err := Save(r); err != nil {
			close(done)
			t.Fatal(err)
		}
	}
	close(done)
	if err := <-loadErrs; err != nil {
		t.Fatal(err)
	}
}

func TestMoreRunnersPerScope(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	base := IDFor(KindRepo, "o", "r")
	if got := IDWithName(base, "box-2"); got != "o__r__box-2" {
		t.Errorf("IDWithName = %q", got)
	}
	if got := IDWithName(base, " my light/box "); got != "o__r__my-light-box" {
		t.Errorf("IDWithName unsafe = %q", got)
	}
	// The first runner keeps the v0.4.0 folder; more get base__name.
	for _, r := range []Runner{
		{ID: base, Kind: KindRepo, Owner: "o", Repo: "r", Name: "box"},
		{ID: IDWithName(base, "box-2"), Kind: KindRepo, Owner: "o", Repo: "r", Name: "box-2"},
		{ID: IDFor(KindRepo, "o", "r2"), Kind: KindRepo, Owner: "o", Repo: "r2", Name: "box"},
	} {
		if err := Save(r); err != nil {
			t.Fatal(err)
		}
	}
	got, err := ForScope("O/R")
	if err != nil || len(got) != 2 || got[0].ID != "o__r" || got[1].ID != "o__r__box-2" {
		t.Fatalf("ForScope: %+v %v", got, err)
	}
	if got[1].Display() != "o/r (box-2)" {
		t.Errorf("Display = %q", got[1].Display())
	}
}

func TestClaimIsExclusive(t *testing.T) {
	t.Setenv("GH_RUNNERS_HOME", t.TempDir())
	const n = 32
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := Claim("o__r__box-2")
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d goroutines claimed the same folder", wins.Load())
	}
	if ok, err := Claim("o__r__box-3"); !ok || err != nil {
		t.Fatalf("claim a free folder: %v %v", ok, err)
	}
}
