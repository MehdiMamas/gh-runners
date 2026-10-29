package localci

import (
	"fmt"
	"path"
	"strings"
)

// ValidateWorkflowPath accepts a relative .yml/.yaml path inside
// .github/workflows/, and nothing else.
func ValidateWorkflowPath(p string) error {
	bad := func(why string) error { return fmt.Errorf("workflow path %q: %s", p, why) }
	if strings.Contains(p, `\`) || strings.HasPrefix(p, "/") {
		return bad("must be a relative path with / separators")
	}
	if path.Clean(p) != p {
		return bad("must be a clean path (no .., ., or double slashes)")
	}
	if !strings.HasPrefix(p, ".github/workflows/") {
		return bad("must be inside .github/workflows/")
	}
	if e := path.Ext(p); e != ".yml" && e != ".yaml" {
		return bad("must end in .yml or .yaml")
	}
	return nil
}
