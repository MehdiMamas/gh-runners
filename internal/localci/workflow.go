// Package localci runs a repository's GitHub Actions ci.yml jobs natively on
// this machine, without registering a runner, and can report the results
// back to the pull request.
package localci

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkflowPath is the one workflow the local CI understands.
const WorkflowPath = ".github/workflows/ci.yml"

// Workflow is a parsed ci.yml. Order keeps the file's job order.
type Workflow struct {
	Jobs  map[string]*Job
	Order []string
}

// Job is one job of the workflow.
type Job struct {
	Name    string
	Needs   []string
	If      string
	RunsOn  string
	WorkDir string // defaults.run.working-directory
	Env     map[string]string
	Outputs map[string]string
	Steps   []Step
	// Raw is the job's YAML text, used to spot secrets and aggregate jobs.
	Raw string
}

// Step is one step of a job.
type Step struct {
	ID      string
	Name    string
	If      string
	Uses    string
	Run     string
	With    map[string]string
	Env     map[string]string
	WorkDir string
}

type rawJob struct {
	Needs    stringList `yaml:"needs"`
	If       string     `yaml:"if"`
	RunsOn   yaml.Node  `yaml:"runs-on"`
	Defaults struct {
		Run struct {
			WorkDir string `yaml:"working-directory"`
		} `yaml:"run"`
	} `yaml:"defaults"`
	Env     map[string]string `yaml:"env"`
	Outputs map[string]string `yaml:"outputs"`
	Steps   []struct {
		ID      string            `yaml:"id"`
		Name    string            `yaml:"name"`
		If      string            `yaml:"if"`
		Uses    string            `yaml:"uses"`
		Run     string            `yaml:"run"`
		With    map[string]string `yaml:"with"`
		Env     map[string]string `yaml:"env"`
		WorkDir string            `yaml:"working-directory"`
	} `yaml:"steps"`
}

// stringList reads a YAML scalar or a list as a list of strings.
type stringList []string

func (s *stringList) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*s = []string{n.Value}
		return nil
	}
	var l []string
	if err := n.Decode(&l); err != nil {
		return err
	}
	*s = l
	return nil
}

// ParseWorkflow parses ci.yml.
func ParseWorkflow(data []byte) (*Workflow, error) {
	var doc struct {
		Jobs yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse %s: %w", WorkflowPath, err)
	}
	if doc.Jobs.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%s has no jobs", WorkflowPath)
	}
	w := &Workflow{Jobs: map[string]*Job{}}
	for i := 0; i+1 < len(doc.Jobs.Content); i += 2 {
		name, node := doc.Jobs.Content[i].Value, doc.Jobs.Content[i+1]
		var rj rawJob
		if err := node.Decode(&rj); err != nil {
			return nil, fmt.Errorf("job %s: %w", name, err)
		}
		raw, _ := yaml.Marshal(node)
		j := &Job{
			Name: name, Needs: rj.Needs, If: rj.If, WorkDir: rj.Defaults.Run.WorkDir,
			Env: rj.Env, Outputs: rj.Outputs, Raw: string(raw),
		}
		if rj.RunsOn.Kind == yaml.ScalarNode {
			j.RunsOn = rj.RunsOn.Value
		} else if rj.RunsOn.Kind == yaml.SequenceNode && len(rj.RunsOn.Content) > 0 {
			j.RunsOn = rj.RunsOn.Content[0].Value
		}
		for _, s := range rj.Steps {
			j.Steps = append(j.Steps, Step{ID: s.ID, Name: s.Name, If: s.If, Uses: s.Uses, Run: s.Run, With: s.With, Env: s.Env, WorkDir: s.WorkDir})
		}
		w.Jobs[name] = j
		w.Order = append(w.Order, name)
	}
	return w, nil
}

// NeedsSecrets reports whether the job reads any secret.
func (j *Job) NeedsSecrets() bool { return strings.Contains(j.Raw, "secrets.") }

// NeedsJava reports whether the job sets up Java, which here means it starts
// the Firebase emulators on their fixed ports.
func (j *Job) NeedsJava() bool {
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "actions/setup-java") {
			return true
		}
	}
	return false
}

// IsAggregate reports whether the job only summarizes its needs (it reads
// needs.*.result); the local CI computes it instead of running its script.
func (j *Job) IsAggregate() bool { return strings.Contains(j.Raw, "needs.*.result") }

// PathFilters returns the dorny/paths-filter filters of the job, if it has that step.
func (j *Job) PathFilters() (map[string][]string, string, bool) {
	for _, s := range j.Steps {
		if strings.HasPrefix(s.Uses, "dorny/paths-filter") {
			f := map[string][]string{}
			if err := yaml.Unmarshal([]byte(s.With["filters"]), &f); err != nil {
				return nil, "", false
			}
			return f, s.ID, true
		}
	}
	return nil, "", false
}
