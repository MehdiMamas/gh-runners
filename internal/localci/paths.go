package localci

import (
	"regexp"
	"strings"
)

// globToRegexp turns a paths-filter glob into a regexp: ** crosses
// directories, * and ? stay inside one, {a,b} picks one.
func globToRegexp(glob string) *regexp.Regexp {
	var b strings.Builder
	b.WriteString("^")
	braces := 0
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch c {
		case '*':
			if i+1 < len(glob) && glob[i+1] == '*' {
				i++
				if i+1 < len(glob) && glob[i+1] == '/' {
					i++
					b.WriteString("(?:.*/)?")
				} else {
					b.WriteString(".*")
				}
			} else {
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '{':
			braces++
			b.WriteString("(?:")
		case '}':
			if braces > 0 {
				braces--
				b.WriteString(")")
			} else {
				b.WriteString(`\}`)
			}
		case ',':
			if braces > 0 {
				b.WriteString("|")
			} else {
				b.WriteString(",")
			}
		default:
			b.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	b.WriteString("$")
	return regexp.MustCompile(b.String())
}

// MatchGlob reports whether path matches glob.
func MatchGlob(glob, path string) bool { return globToRegexp(glob).MatchString(path) }

// FilterMatches reports whether any changed file matches the patterns. A
// pattern starting with ! excludes files from the match.
func FilterMatches(patterns, files []string) bool {
	for _, f := range files {
		in := false
		for _, p := range patterns {
			if strings.HasPrefix(p, "!") {
				continue
			}
			if MatchGlob(p, f) {
				in = true
				break
			}
		}
		if !in {
			continue
		}
		excluded := false
		for _, p := range patterns {
			if strings.HasPrefix(p, "!") && MatchGlob(p[1:], f) {
				excluded = true
				break
			}
		}
		if !excluded {
			return true
		}
	}
	return false
}

// ChangesOutputs evaluates the changes job: for each filter name, whether
// the changed files touch it. The result maps the job's output names (which
// point at steps.<id>.outputs.<filter>) to "true" or "false".
func ChangesOutputs(j *Job, files []string) (map[string]string, bool) {
	filters, id, ok := j.PathFilters()
	if !ok {
		return nil, false
	}
	byFilter := map[string]string{}
	for name, pats := range filters {
		byFilter[name] = boolStr(FilterMatches(pats, files))
	}
	out := map[string]string{}
	for name := range filters {
		out[name] = byFilter[name]
	}
	ref := regexp.MustCompile(`steps\.` + regexp.QuoteMeta(id) + `\.outputs\.([A-Za-z0-9_-]+)`)
	for name, expr := range j.Outputs {
		if m := ref.FindStringSubmatch(expr); m != nil {
			out[name] = byFilter[m[1]]
		}
	}
	return out, true
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}
