package localci

import "strings"

// mergeRegistryEnv overlays machine then user variables onto base. Keys match
// case-insensitively; user wins over machine. Path is joined as machine;user
// followed by any base entries not already present.
func mergeRegistryEnv(base []string, machine, user map[string]string) []string {
	if len(machine) == 0 && len(user) == 0 {
		return base
	}
	out := append([]string(nil), base...)
	index := map[string]int{}
	for i, kv := range out {
		if k, _, ok := strings.Cut(kv, "="); ok {
			index[strings.ToUpper(k)] = i
		}
	}
	set := func(k, v string) {
		u := strings.ToUpper(k)
		if i, ok := index[u]; ok {
			out[i] = out[i][:len(k)] + "=" + v
			if ek, _, _ := strings.Cut(out[i], "="); ek != k {
				out[i] = k + "=" + v
			}
			return
		}
		index[u] = len(out)
		out = append(out, k+"="+v)
	}
	var basePath, mPath, uPath string
	if i, ok := index["PATH"]; ok {
		_, basePath, _ = strings.Cut(out[i], "=")
	}
	for k, v := range machine {
		if strings.EqualFold(k, "Path") {
			mPath = v
			continue
		}
		set(k, v)
	}
	for k, v := range user {
		if strings.EqualFold(k, "Path") {
			uPath = v
			continue
		}
		set(k, v)
	}
	if mPath != "" || uPath != "" {
		seen := map[string]bool{}
		var parts []string
		for _, src := range []string{mPath, uPath, basePath} {
			for _, p := range strings.Split(src, ";") {
				if p == "" || seen[strings.ToLower(p)] {
					continue
				}
				seen[strings.ToLower(p)] = true
				parts = append(parts, p)
			}
		}
		name := "Path"
		if i, ok := index["PATH"]; ok {
			name, _, _ = strings.Cut(out[i], "=")
		}
		set(name, strings.Join(parts, ";"))
	}
	return out
}
