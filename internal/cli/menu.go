package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/charmbracelet/huh"

	"github.com/MehdiMamas/gh-runners/internal/github"
	"github.com/MehdiMamas/gh-runners/internal/ops"
	"github.com/MehdiMamas/gh-runners/internal/service"
	"github.com/MehdiMamas/gh-runners/internal/store"
)

// runMenu is the interactive mode: `gh-runners` with no arguments.
func runMenu(ctx context.Context) error {
	for {
		s := service.Get()
		daemonState := "daemon not running"
		if s.Running {
			daemonState = "daemon running"
		}
		local, _ := store.List()
		var action string
		opts := []huh.Option[string]{
			huh.NewOption("Add runners to repos (pick from a list)", "add"),
			huh.NewOption("Show runners and their status", "list"),
			huh.NewOption("Sync: a runner for every private repo I own", "sync"),
			huh.NewOption("Run what was on before it closed", "last"),
			huh.NewOption("Run all", "all"),
			huh.NewOption("Stop / start a runner", "toggle"),
			huh.NewOption("Remove runners", "remove"),
			huh.NewOption(onOff(s.Installed, "Stop starting at login", "Start runners now and at every login"), "service"),
			huh.NewOption("On-demand mode (no per-repo setup)", "on-demand"),
			huh.NewOption("Check this machine (doctor)", "doctor"),
			huh.NewOption("Quit", "quit"),
		}
		err := huh.NewSelect[string]().
			Title(fmt.Sprintf("gh-runners %s — %d runners here, %s", Version, len(local), daemonState)).
			Options(opts...).Value(&action).Run()
		if errors.Is(err, huh.ErrUserAborted) || action == "quit" {
			return nil
		}
		if err != nil {
			return err
		}
		var runErr error
		switch action {
		case "add":
			runErr = menuAdd(ctx)
		case "list":
			runErr = runSub("list")
		case "sync":
			runErr = runSub("sync")
		case "last":
			runErr = startSet(false)
		case "all":
			runErr = startSet(true)
		case "toggle":
			runErr = menuToggle()
		case "remove":
			runErr = menuRemove(ctx, local)
		case "service":
			if s.Installed {
				runErr = runSub("service", "uninstall")
			} else {
				runErr = runSub("service", "install")
			}
		case "on-demand":
			runErr = menuOnDemand()
		case "doctor":
			runErr = runSub("doctor")
		}
		if runErr != nil && !errors.Is(runErr, huh.ErrUserAborted) {
			fmt.Fprintln(os.Stderr, "error:", runErr)
		}
		fmt.Println()
	}
}

func onOff(b bool, yes, no string) string {
	if b {
		return yes
	}
	return no
}

// runSub runs a subcommand on a fresh command tree, so flags parsed by an
// earlier menu action do not leak into the next one.
func runSub(args ...string) error {
	root := newRoot()
	root.SetArgs(args)
	return root.Execute()
}

func menuAdd(ctx context.Context) error {
	c, err := client()
	if err != nil {
		return err
	}
	fmt.Println("Loading your repositories ...")
	repos, err := c.ListMyRepos(ctx)
	if err != nil {
		return err
	}
	local, _ := store.List()
	have := map[string]bool{}
	for _, r := range local {
		have[strings.ToLower(r.Scope())] = true
	}
	var opts []huh.Option[string]
	for _, r := range repos {
		if !r.Permissions.Admin || r.Archived || have[strings.ToLower(r.FullName)] {
			continue
		}
		label := r.FullName
		if !r.Private {
			label += "  (public: not recommended)"
		}
		opts = append(opts, huh.NewOption(label, r.FullName))
	}
	if len(opts) == 0 {
		fmt.Println("Every repo you administer already has a runner on this machine.")
		return nil
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].Value < opts[j].Value })
	var picked []string
	if err := huh.NewMultiSelect[string]().
		Title("Which repos should get a runner on this machine? (space to pick, / to filter, enter to confirm)").
		Options(opts...).Filterable(true).Height(18).Value(&picked).Run(); err != nil {
		return err
	}
	for _, full := range picked {
		scope, _ := github.ParseScope(full, false)
		fmt.Printf("\n== %s\n", full)
		_, err := ops.Add(ctx, c, scope, ops.AddOptions{Source: "add", AllowPublic: true}, os.Stdout)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
		}
	}
	if len(picked) > 0 {
		afterChange()
	}
	return nil
}

func menuToggle() error {
	local, _ := store.List()
	if len(local) == 0 {
		fmt.Println("No runners yet.")
		return nil
	}
	var opts []huh.Option[string]
	for _, r := range local {
		opts = append(opts, huh.NewOption(fmt.Sprintf("%s  [%s]", r.Display(), onOff(r.Disabled, "stopped", "on")), r.ID))
	}
	var id string
	if err := huh.NewSelect[string]().Title("Toggle which runner?").Options(opts...).Value(&id).Run(); err != nil {
		return err
	}
	r, err := store.Load(id)
	if err != nil {
		return err
	}
	if err := ops.SetDisabled(r, !r.Disabled); err != nil {
		return err
	}
	fmt.Printf("%s is now %s.\n", r.Display(), onOff(!r.Disabled, "stopped", "on"))
	return nil
}

func menuRemove(ctx context.Context, local []store.Runner) error {
	if len(local) == 0 {
		fmt.Println("No runners yet.")
		return nil
	}
	var opts []huh.Option[string]
	for _, r := range local {
		opts = append(opts, huh.NewOption(r.Display(), r.ID))
	}
	var ids []string
	if err := huh.NewMultiSelect[string]().Title("Remove which runners?").Options(opts...).Value(&ids).Run(); err != nil {
		return err
	}
	if len(ids) == 0 {
		return nil
	}
	sure := false
	if err := huh.NewConfirm().Title(fmt.Sprintf("Unregister and delete %d runners from this machine?", len(ids))).Value(&sure).Run(); err != nil || !sure {
		return err
	}
	c, err := client()
	if err != nil {
		return err
	}
	for _, id := range ids {
		r, err := store.Load(id)
		if err != nil {
			continue
		}
		if err := ops.Remove(ctx, c, r, false, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			continue
		}
		fmt.Printf("Removed %s.\n", r.Display())
	}
	return nil
}

func menuOnDemand() error {
	cfg, _ := store.LoadConfig()
	on := cfg.OnDemand.Enabled
	if err := huh.NewConfirm().
		Title("On-demand mode").
		Description("Watch all your private repos and start a one-job runner here whenever a job\nwith matching runs-on labels is queued. No runner idles when nothing is queued.").
		Affirmative("On").Negative("Off").Value(&on).Run(); err != nil {
		return err
	}
	return runSub("on-demand", onOff(on, "on", "off"))
}
