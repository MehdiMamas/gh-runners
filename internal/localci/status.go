package localci

// filterSkipReason is why a job is skipped when no changed file matches its
// filter; such a job posts a success "Skipped" status.
const filterSkipReason = "no changed files match its filter"

// firstBad names the first job (by name) that failed, was blocked or could
// not run, or "" when none did. Its log is the one a status links to.
func firstBad(rs map[string]Result) string {
	for _, n := range sortedKeys(rs) {
		if s := rs[n].Status; s == Fail || s == Blocked || s == Unsupported {
			return n
		}
	}
	return ""
}

func resultMap(rs []Result) map[string]Result {
	m := map[string]Result{}
	for _, r := range rs {
		m[r.Job] = r
	}
	return m
}

// firstBlocked is the first blocked job and its reason.
func firstBlocked(rs []Result) (job, reason string) {
	for _, r := range rs {
		if r.Status == Blocked {
			return r.Job, r.Reason
		}
	}
	return "", ""
}
