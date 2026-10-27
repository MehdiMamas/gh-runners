package store

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// humanPart matches one "10 min", "2h" or "1.5 days" piece of a duration.
var humanPart = regexp.MustCompile(`(\d+(?:\.\d+)?)\s*([a-z]+)`)

var humanUnits = map[string]time.Duration{
	"s": time.Second, "sec": time.Second, "secs": time.Second, "second": time.Second, "seconds": time.Second,
	"m": time.Minute, "min": time.Minute, "mins": time.Minute, "minute": time.Minute, "minutes": time.Minute,
	"h": time.Hour, "hr": time.Hour, "hrs": time.Hour, "hour": time.Hour, "hours": time.Hour,
	"d": 24 * time.Hour, "day": 24 * time.Hour, "days": 24 * time.Hour,
	"w": 7 * 24 * time.Hour, "wk": 7 * 24 * time.Hour, "week": 7 * 24 * time.Hour, "weeks": 7 * 24 * time.Hour,
}

// ParseHumanDuration reads a duration the way people type it: Go forms like
// "10m" and "1h30m", and also "10min", "10 minutes", "2 hours", "1d" or
// "1 day 6 hours". Parts may be separated by spaces, commas or "and".
func ParseHumanDuration(s string) (time.Duration, error) {
	if d, err := time.ParseDuration(strings.TrimSpace(s)); err == nil {
		return d, nil
	}
	in := strings.ToLower(strings.TrimSpace(s))
	rest := humanPart.ReplaceAllString(in, "")
	rest = strings.NewReplacer(",", "", "and", "", " ", "").Replace(rest)
	parts := humanPart.FindAllStringSubmatch(in, -1)
	if len(parts) == 0 || rest != "" {
		return 0, fmt.Errorf("%q is not a duration like 10m, 10 minutes or 2 hours", s)
	}
	var total time.Duration
	for _, p := range parts {
		unit, ok := humanUnits[p[2]]
		if !ok {
			return 0, fmt.Errorf("%q: unknown unit %q (use minutes, hours or days)", s, p[2])
		}
		n, err := strconv.ParseFloat(p[1], 64)
		if err != nil {
			return 0, fmt.Errorf("%q is not a duration like 10m, 10 minutes or 2 hours", s)
		}
		total += time.Duration(n * float64(unit))
	}
	return total, nil
}
