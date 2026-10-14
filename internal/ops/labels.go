package ops

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// LabelSet is one runs-on label combination a repository's workflows ask
// for, e.g. [self-hosted, heavy, macOS], with the files that use it.
type LabelSet struct {
	Labels []string `json:"labels"`
	Files  []string `json:"files"`
	Jobs   int      `json:"jobs"`
}

var (
	runsOnRe   = regexp.MustCompile(`^(\s*)(?:-\s+)?runs-on:\s*(.*)$`)
	listItemRe = regexp.MustCompile(`^(\s*)-\s+(.*)$`)
	jsonListRe = regexp.MustCompile(`\[[^\[\]]*\]`)
	quotedRe   = regexp.MustCompile(`["']([A-Za-z0-9._-]+)["']`)
	labelRe    = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	hostedRe   = regexp.MustCompile(`(?i)^(ubuntu|windows|macos)-|-latest$`)
)

// WorkflowLabelSets reads the runs-on values of workflow files and returns
// the label sets that a self-hosted runner can serve, most used first.
// It is a line-based reading of YAML: enough for runs-on, which is a
// string, a flow list, a block list or a {group, labels} map. Expressions
// such as ${{ matrix.os }} are skipped, but quoted labels inside them
// (fromJSON('["self-hosted", "heavy"]')) are kept.
func WorkflowLabelSets(files []github.WorkflowFile) []LabelSet {
	byKey := map[string]*LabelSet{}
	var order []string
	for _, f := range files {
		lines := strings.Split(strings.ReplaceAll(f.Content, "\r\n", "\n"), "\n")
		for i := 0; i < len(lines); i++ {
			m := runsOnRe.FindStringSubmatch(lines[i])
			if m == nil {
				continue
			}
			value := stripComment(m[2])
			if value == "" || value == "|" || value == ">" {
				// Block form: the following deeper lines.
				indent := len(m[1])
				var block []string
				for j := i + 1; j < len(lines); j++ {
					l := lines[j]
					if strings.TrimSpace(l) == "" || strings.HasPrefix(strings.TrimSpace(l), "#") {
						continue
					}
					if len(l)-len(strings.TrimLeft(l, " \t")) <= indent {
						break
					}
					block = append(block, stripComment(l))
				}
				value = blockValue(block)
			}
			labels := parseRunsOn(value)
			if !selfHostedSet(labels) {
				continue
			}
			key := strings.ToLower(strings.Join(sortedCopy(labels), ","))
			ls := byKey[key]
			if ls == nil {
				ls = &LabelSet{Labels: labels}
				byKey[key] = ls
				order = append(order, key)
			}
			ls.Jobs++
			if !contains(ls.Files, f.Name) {
				ls.Files = append(ls.Files, f.Name)
			}
		}
	}
	out := make([]LabelSet, 0, len(order))
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Jobs > out[j].Jobs })
	return out
}

func stripComment(s string) string {
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '"' || c == '\'':
			inQuote = c
		case c == '#' && (i == 0 || s[i-1] == ' ' || s[i-1] == '\t'):
			return strings.TrimSpace(s[:i])
		}
	}
	return strings.TrimSpace(s)
}

// blockValue turns a block list or a block map with labels into a flow list.
func blockValue(block []string) string {
	// List items are labels, whether directly under runs-on or under
	// labels:; a group: line is not.
	var items []string
	for _, l := range block {
		if m := listItemRe.FindStringSubmatch(l); m != nil {
			items = append(items, m[2])
			continue
		}
		key, val, ok := strings.Cut(strings.TrimSpace(l), ":")
		if ok && strings.TrimSpace(key) == "labels" && strings.TrimSpace(val) != "" {
			return strings.TrimSpace(val)
		}
	}
	return "[" + strings.Join(items, ",") + "]"
}

// parseRunsOn returns the labels of one runs-on value.
func parseRunsOn(v string) []string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "${{") {
		// Only quoted labels inside JSON lists: fromJSON('["a", "b"]').
		var out []string
		for _, list := range jsonListRe.FindAllString(v, -1) {
			for _, m := range quotedRe.FindAllStringSubmatch(list, -1) {
				out = appendLabel(out, m[1])
			}
		}
		return out
	}
	if strings.HasPrefix(v, "{") {
		// {group: x, labels: [a, b]} or {labels: a}
		i := strings.Index(v, "labels:")
		if i < 0 {
			return nil
		}
		rest := strings.TrimSpace(v[i+len("labels:"):])
		if strings.HasPrefix(rest, "[") {
			if j := strings.Index(rest, "]"); j >= 0 {
				return parseRunsOn(rest[:j+1])
			}
		}
		rest, _, _ = strings.Cut(rest, ",")
		return parseRunsOn(strings.TrimSuffix(strings.TrimSpace(rest), "}"))
	}
	v = strings.TrimSuffix(strings.TrimPrefix(v, "["), "]")
	var out []string
	for _, item := range splitFlow(v) {
		item = strings.Trim(strings.TrimSpace(item), `"'`)
		out = appendLabel(out, item)
	}
	return out
}

// splitFlow splits a flow list on commas outside ${{ }} and quotes.
func splitFlow(s string) []string {
	var out []string
	depth, start := 0, 0
	inQuote := byte(0)
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case inQuote != 0:
			if c == inQuote {
				inQuote = 0
			}
		case c == '"' || c == '\'':
			inQuote = c
		case c == '{':
			depth++
		case c == '}':
			depth--
		case c == ',' && depth == 0:
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func appendLabel(out []string, l string) []string {
	if !labelRe.MatchString(l) {
		return out // empty, an expression, or not a label
	}
	for _, x := range out {
		if strings.EqualFold(x, l) {
			return out
		}
	}
	return append(out, l)
}

// selfHostedSet reports whether a self-hosted runner can take the job: it
// names self-hosted, or labels that are not GitHub-hosted images.
func selfHostedSet(labels []string) bool {
	if len(labels) == 0 {
		return false
	}
	for _, l := range labels {
		if strings.EqualFold(l, "self-hosted") {
			return true
		}
	}
	for _, l := range labels {
		if !hostedRe.MatchString(l) {
			return true
		}
	}
	return false
}

// builtinLabels are the labels GitHub gives every self-hosted runner.
var builtinLabels = map[string]bool{"self-hosted": true, "linux": true, "macos": true, "windows": true, "x64": true, "arm64": true, "arm": true}

// IsBuiltinLabel reports whether GitHub sets the label itself.
func IsBuiltinLabel(l string) bool { return builtinLabels[strings.ToLower(l)] }

// CustomLabels drops the labels GitHub sets itself.
func CustomLabels(labels []string) []string {
	var out []string
	for _, l := range labels {
		if !IsBuiltinLabel(l) {
			out = append(out, l)
		}
	}
	return out
}

func sortedCopy(in []string) []string {
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ToLower(s)
	}
	sort.Strings(out)
	return out
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// CleanLabels trims, drops empty and duplicate labels and the ones GitHub
// sets itself, and rejects labels GitHub would refuse.
func CleanLabels(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, l := range in {
		l = strings.TrimSpace(l)
		if l == "" || IsBuiltinLabel(l) || seen[strings.ToLower(l)] {
			continue
		}
		if strings.ContainsAny(l, ", ") || len(l) > 256 {
			return nil, fmt.Errorf("%q is not a valid label", l)
		}
		seen[strings.ToLower(l)] = true
		out = append(out, l)
	}
	return out, nil
}

// SetLabels replaces the custom labels of runner id in scope on GitHub,
// and in the store when the runner is one of ours.
func SetLabels(ctx context.Context, c *github.Client, scope github.Scope, id int64, labels []string) ([]github.Label, error) {
	labels, err := CleanLabels(labels)
	if err != nil {
		return nil, err
	}
	list, err := c.ListRunners(ctx, scope)
	if err != nil {
		return nil, err
	}
	name := ""
	for _, gr := range list {
		if gr.ID == id {
			name = gr.Name
		}
	}
	if name == "" {
		return nil, fmt.Errorf("runner %d is not registered to %s any more", id, scope)
	}
	got, err := c.SetRunnerLabels(ctx, scope, id, labels)
	if err != nil {
		return nil, err
	}
	local, _ := store.List()
	for _, r := range local {
		if strings.EqualFold(r.Scope(), scope.String()) && r.Name == name {
			r.Labels = labels
			if err := store.Save(r); err != nil {
				return got, err
			}
		}
	}
	return got, nil
}
