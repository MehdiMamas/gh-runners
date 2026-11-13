package store

import "testing"

func TestProtectedFolderIn(t *testing.T) {
	h := "/Users/me"
	for dir, want := range map[string]string{
		"/Users/me/Documents/runners":          "~/Documents",
		"/Users/me/documents":                  "~/Documents",
		"/Users/me/Library/Mobile Documents/x": "~/Library/Mobile Documents",
		"/Users/me/.gh-runners/runners":        "",
		"/Users/me/actions-runner/_work":       "",
		"/Users/me/DocumentsX":                 "",
	} {
		if got := protectedFolderIn(dir, h); got != want {
			t.Errorf("%s: got %q want %q", dir, got, want)
		}
	}
}
