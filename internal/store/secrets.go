package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Secret is one local CI secret: an inline value, or the path of a file
// (the job gets the path, never the contents).
type Secret struct {
	Value string `json:"value,omitempty"`
	File  string `json:"file,omitempty"`
}

var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidSecretName reports whether name may be a secret name.
func ValidSecretName(name string) bool { return secretName.MatchString(name) }

// secretsPath is the file holding one repo's secrets. It lives only on this
// machine, mode 0600 in a 0700 folder.
func secretsPath(full string) (string, error) {
	owner, repo, ok := strings.Cut(full, "/")
	if !ok || owner == "" || repo == "" || strings.ContainsAny(full[len(owner)+1:], "/\\") ||
		strings.Contains(owner, "..") || strings.Contains(repo, "..") || strings.ContainsAny(owner, "\\") {
		return "", fmt.Errorf("%q is not owner/repo", full)
	}
	return filepath.Join(Home(), "secrets", owner, repo+".json"), nil
}

// LoadSecrets reads the secrets of owner/repo; none set is an empty map.
func LoadSecrets(full string) (map[string]Secret, error) {
	p, err := secretsPath(full)
	if err != nil {
		return nil, err
	}
	b, err := os.ReadFile(p)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]Secret{}, nil
	}
	if err != nil {
		return nil, err
	}
	m := map[string]Secret{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("read %s: %w", p, err)
	}
	return m, nil
}

func saveSecrets(full string, m map[string]Secret) error {
	p, err := secretsPath(full)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	_ = os.Chmod(filepath.Dir(p), 0o700)
	if err := writeJSON(p, m, 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600)
}

// SetSecret stores one secret of owner/repo.
func SetSecret(full, name string, s Secret) error {
	if !ValidSecretName(name) {
		return fmt.Errorf("%q is not a valid secret name (letters, digits and _, not starting with a digit)", name)
	}
	if (s.Value == "") == (s.File == "") {
		return errors.New("a secret has either a value or a file")
	}
	if s.File != "" && !filepath.IsAbs(s.File) {
		return fmt.Errorf("the file %q must be an absolute path", s.File)
	}
	m, err := LoadSecrets(full)
	if err != nil {
		return err
	}
	m[name] = s
	return saveSecrets(full, m)
}

// RemoveSecret deletes one secret; it reports whether it existed.
func RemoveSecret(full, name string) (bool, error) {
	m, err := LoadSecrets(full)
	if err != nil {
		return false, err
	}
	if _, ok := m[name]; !ok {
		return false, nil
	}
	delete(m, name)
	return true, saveSecrets(full, m)
}

// SecretNames lists the secret names of owner/repo, sorted.
func SecretNames(full string) ([]string, error) {
	m, err := LoadSecrets(full)
	if err != nil {
		return nil, err
	}
	var names []string
	for n := range m {
		names = append(names, n)
	}
	sort.Strings(names)
	return names, nil
}
