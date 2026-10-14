package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"time"

	"github.com/charmbracelet/huh"

	"github.com/MehdiMamas/gh-runners/internal/store"
)

func openDaemonLog() (*os.File, error) {
	if err := os.MkdirAll(store.Home(), 0o755); err != nil {
		return nil, err
	}
	p := store.DaemonLog()
	if st, err := os.Stat(p); err == nil && st.Size() > 10<<20 {
		_ = os.Rename(p, p+".1")
	}
	return os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
}

func newLogger(w io.Writer) *log.Logger { return log.New(w, "", log.LstdFlags) }

// tail prints the last n lines of path, then follows it if asked.
func tail(ctx context.Context, path string, n int, follow bool) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("no log yet at %s", path)
	}
	defer f.Close()
	// Read at most the last 1 MB to find the last n lines.
	st, _ := f.Stat()
	start := int64(0)
	if st != nil && st.Size() > 1<<20 {
		start = st.Size() - 1<<20
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return err
	}
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	for _, l := range lines {
		fmt.Println(l)
	}
	if !follow {
		return nil
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if line != "" {
			fmt.Print(line)
		}
		if err == io.EOF {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(500 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

func promptSecret(label string) (string, error) {
	if !interactive() {
		fmt.Fprint(os.Stderr, label)
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimSpace(line), err
	}
	var v string
	err := huh.NewInput().Title(strings.TrimSpace(label)).EchoMode(huh.EchoModePassword).Value(&v).Run()
	return v, err
}
