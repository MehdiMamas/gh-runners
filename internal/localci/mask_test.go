package localci

import (
	"bytes"
	"strings"
	"testing"
)

func TestMaskWriterSplitAcrossWrites(t *testing.T) {
	var out bytes.Buffer
	m := newMaskWriter(&out, []string{"hunter2hunter2", "ab"})
	for _, part := range []string{"start hunter2", "hun", "ter2 end\nab stays\n"} {
		if n, err := m.Write([]byte(part)); err != nil || n != len(part) {
			t.Fatalf("write %q: %d %v", part, n, err)
		}
	}
	if err := m.Flush(); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "hunter") || got != "start *** end\nab stays\n" {
		t.Errorf("got %q", got)
	}
}

func TestMaskWriterByteByByteAndFlush(t *testing.T) {
	var out bytes.Buffer
	m := newMaskWriter(&out, []string{"s3cr3t-value"})
	for _, b := range []byte("x s3cr3t-value y s3cr3t-val") {
		m.Write([]byte{b})
	}
	m.Flush()
	if got := out.String(); got != "x *** y s3cr3t-val" {
		t.Errorf("got %q", got)
	}
}

func TestMaskString(t *testing.T) {
	if got := MaskString("step failed: echo abcd1234 and xyz", []string{"abcd1234", "xyz"}); got != "step failed: echo *** and xyz" {
		t.Errorf("got %q", got)
	}
}
