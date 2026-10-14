package ops

import (
	"reflect"
	"testing"

	"github.com/MehdiMamas/gh-runners/internal/github"
)

func TestWorkflowLabelSets(t *testing.T) {
	files := []github.WorkflowFile{
		{Name: "heavy-tests.yml", Content: `
jobs:
  test:
    runs-on: ${{ inputs.os == 'any' && fromJSON('["self-hosted", "heavy"]') || fromJSON(format('["self-hosted", "heavy", "{0}"]', inputs.os)) }}
`},
		{Name: "package.yml", Content: "jobs:\r\n  package:\r\n    runs-on: [self-hosted, heavy, '${{ inputs.os }}'] # per OS\r\n"},
		{Name: "triage.yml", Content: `
jobs:
  triage:
    runs-on: [self-hosted, light]
  hosted:
    runs-on: ubuntu-latest
  matrix:
    runs-on: ${{ matrix.os }}
`},
		{Name: "block.yml", Content: `
jobs:
  a:
    runs-on:
      - self-hosted
      - "heavy"   # comment
      - macOS
    steps: []
  b:
    runs-on:
      group: big
      labels: [gpu]
  c:
    runs-on: { group: big, labels: [gpu] }
  d:
    runs-on: self-hosted
  e:
    runs-on: windows-2022
`},
	}
	got := WorkflowLabelSets(files)
	want := []LabelSet{
		{Labels: []string{"self-hosted", "heavy"}, Files: []string{"heavy-tests.yml", "package.yml"}, Jobs: 2},
		{Labels: []string{"gpu"}, Files: []string{"block.yml"}, Jobs: 2},
		{Labels: []string{"self-hosted", "light"}, Files: []string{"triage.yml"}, Jobs: 1},
		{Labels: []string{"self-hosted", "heavy", "macOS"}, Files: []string{"block.yml"}, Jobs: 1},
		{Labels: []string{"self-hosted"}, Files: []string{"block.yml"}, Jobs: 1},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got  %+v\nwant %+v", got, want)
	}
}

func TestCleanLabels(t *testing.T) {
	got, err := CleanLabels([]string{" heavy ", "", "self-hosted", "macOS", "Heavy", "gpu"})
	if err != nil || !reflect.DeepEqual(got, []string{"heavy", "gpu"}) {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := CleanLabels([]string{"two words"}); err == nil {
		t.Fatal("a label with a space was accepted")
	}
}
