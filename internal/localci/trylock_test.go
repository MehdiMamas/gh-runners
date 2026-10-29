package localci

import "testing"

func TestTryLock(t *testing.T) {
	dir := t.TempDir()
	a, b := &Locks{Dir: dir}, &Locks{Dir: dir}
	rel, ok, err := a.TryLock("emulator")
	if err != nil || !ok {
		t.Fatalf("first TryLock: ok=%v err=%v", ok, err)
	}
	// A second holder (own mutex, same file) is another process in effect.
	if _, ok, err := b.TryLock("emulator"); err != nil || ok {
		t.Fatalf("TryLock while held: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := a.TryLock("emulator"); ok {
		t.Fatal("TryLock succeeded on the same Locks while held")
	}
	rel()
	rel2, ok, err := b.TryLock("emulator")
	if err != nil || !ok {
		t.Fatalf("TryLock after release: ok=%v err=%v", ok, err)
	}
	rel2()
}
