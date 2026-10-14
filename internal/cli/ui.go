package cli

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/MehdiMamas/gh-runners/internal/control"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/shortcut"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

func newUICmd() *cobra.Command {
	var noBrowser bool
	cmd := &cobra.Command{
		Use:   "ui",
		Short: "Open the dashboard in your browser (the default with no arguments)",
		Long: `ui starts gh-runners in the background if it is not running yet and opens
its dashboard: sign in to GitHub, pick repositories, add runners, and turn
on sync or on-demand runners, all by clicking.

The dashboard only listens on 127.0.0.1. The link it opens carries a secret
that unlocks it for this browser.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			url, err := dashboardURL()
			if err != nil {
				return err
			}
			if !noBrowser && canOpenBrowser() {
				if err := openBrowser(url); err == nil {
					fmt.Println("Opened the dashboard in your browser:")
					fmt.Println("  " + url)
					return nil
				}
			}
			fmt.Println("Open this link in a browser on this computer:")
			fmt.Println("  " + url)
			if os.Getenv("SSH_CONNECTION") != "" {
				info, _ := store.LoadDaemonInfo()
				fmt.Printf("\nOver SSH, forward the port first and open the link on your own computer:\n  ssh -L %s:%s <this host>\n", portOf(info.Addr), info.Addr)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&noBrowser, "no-browser", false, "only print the link")
	return cmd
}

// dashboardURL makes sure a daemon with the dashboard is running and
// returns the link that unlocks it.
func dashboardURL() (string, error) {
	if err := ensureDashboard(); err != nil {
		return "", err
	}
	info, err := store.LoadDaemonInfo()
	if err != nil {
		return "", err
	}
	secret, err := store.UISecret()
	if err != nil {
		return "", err
	}
	return "http://" + info.Addr + "/?k=" + secret, nil
}

func ensureDashboard() error {
	if _, err := control.GetStatus(); err != nil {
		fmt.Println("Starting gh-runners in the background ...")
		if service.Installed().Installed {
			return service.Start()
		}
		return service.StartDetached()
	}
	if hasDashboard() {
		return nil
	}
	// A daemon from before the dashboard existed: restart it on this binary.
	fmt.Println("Restarting the gh-runners daemon to load the dashboard ...")
	if service.Installed().Installed {
		return service.Restart()
	}
	_ = control.Shutdown()
	for i := 0; i < 300; i++ {
		if _, err := control.GetStatus(); err != nil {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	return service.StartDetached()
}

func hasDashboard() bool {
	info, err := store.LoadDaemonInfo()
	if err != nil {
		return false
	}
	c := http.Client{Timeout: 3 * time.Second}
	res, err := c.Get("http://" + info.Addr + "/assets/icon.svg")
	if err != nil {
		return false
	}
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func portOf(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return addr
}

func canOpenBrowser() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

func openBrowser(url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

func newShortcutCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "shortcut",
		Short: "Add gh-runners to the Start menu, Applications or the app menu",
	}
	cmd.AddCommand(
		&cobra.Command{Use: "install", Short: "Create the shortcut that opens the dashboard", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			where, err := shortcut.Install()
			if err != nil {
				return err
			}
			fmt.Println("Created", where)
			return nil
		}},
		&cobra.Command{Use: "uninstall", Short: "Remove the shortcut", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
			where, err := shortcut.Uninstall()
			if err != nil {
				return err
			}
			fmt.Println("Removed", where)
			return nil
		}},
	)
	return cmd
}

func newMenuCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "menu",
		Short: "Interactive menu in the terminal",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !interactive() {
				return fmt.Errorf("the menu needs a terminal; use `gh-runners ui` or the subcommands")
			}
			return runMenu(cmd.Context())
		},
	}
}
