// Command opencode-limits is a TUI that shows per-model spend and remaining
// budget for an OpenCode Go plan, across the rolling 5-hour, weekly, and
// monthly windows.
//
// It authenticates with the OpenCode Console OAuth credential already on disk
// (written by the opencode-console CLI), refreshing it in place. When no
// credential exists it runs the same device-code login the CLI uses.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/cydnirn/opencode-limits/internal/allowance"
	"github.com/cydnirn/opencode-limits/internal/budget"
	"github.com/cydnirn/opencode-limits/internal/config"
	"github.com/cydnirn/opencode-limits/internal/console"
	"github.com/cydnirn/opencode-limits/internal/tui"
)

// version is stamped at build time via -ldflags.
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "opencode-limits: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	var (
		once        = flag.Bool("once", false, "print a text report and exit instead of running the TUI")
		server      = flag.String("server", "", "Console API origin (default: from credential, else https://opencode.ai/console)")
		configPath  = flag.String("config", "", "path to config.toml (default: per-OS config dir)")
		refresh     = flag.Duration("refresh", 0, "refresh interval (default: from config, else 60s)")
		login       = flag.Bool("login", false, "force a fresh device-code login")
		showVersion = flag.Bool("version", false, "print version and exit")
		showAllow   = flag.Bool("allowances", false, "print the per-model monthly limits and exit")
		checkAllow  = flag.Bool("check-allowances", false, "compare the embedded limit table with the live one and exit")
		initConfig  = flag.Bool("init", false, "write a starter config.toml from the plan's models and exit")
		noLogin     = flag.Bool("no-login", false, "fail instead of starting a device login when unauthenticated")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("opencode-limits " + version)
		return nil
	}

	// The allowance table is public, so this works without signing in.
	if *showAllow || *checkAllow {
		return runAllowances(*checkAllow)
	}

	paths, err := config.FromOSEnv().Resolve()
	if err != nil {
		return err
	}
	if *configPath != "" {
		paths.ConfigFile = *configPath
	}
	if *server != "" {
		paths.Server = *server
	}

	cfg := config.Defaults()
	if loaded, lerr := config.Load(paths.ConfigFile); lerr == nil {
		cfg = loaded
	} else if !errors.Is(lerr, config.ErrNoConfig) {
		// A malformed config is worth failing on: silently ignoring a typo'd
		// model ID loses the allocation the operator just wrote.
		return lerr
	}

	store := &console.Store{Path: paths.CredentialFile}
	client := console.NewClient(store, paths.Server)

	if *initConfig {
		return writeStarterConfig(client, paths, cfg)
	}

	if *login {
		if err := deviceLogin(client, store, paths.Server); err != nil {
			return err
		}
	}

	if err := client.Prepare(); err != nil {
		if errors.Is(err, console.ErrNoCredential) {
			if *noLogin || *once {
				return fmt.Errorf("not signed in to OpenCode Console.\n"+
					"Run `opencode auth login` or `opencode-limits --login` first.\n"+
					"Expected credential at: %s", paths.CredentialFile)
			}
			if err := deviceLogin(client, store, paths.Server); err != nil {
				return err
			}
		} else if errors.Is(err, console.ErrConcurrentRefresh) {
			// Another opencode process is mid-refresh and the file has not been
			// updated yet. Starting a device login here would overwrite a
			// credential that is very likely still valid, so refuse instead.
			return fmt.Errorf("another opencode process appears to be refreshing the Console "+
				"session right now, so this tool will not start a new login.\n"+
				"Wait a moment and retry. If it keeps happening, run `opencode-limits --login`.\n(detail: %v)", err)
		} else {
			var ae *console.AuthError
			if errors.As(err, &ae) && ae.IsInvalidGrant() {
				if *noLogin || *once {
					return fmt.Errorf("Console session expired and the refresh token is no longer valid.\n"+
						"Run `opencode-limits --login` to sign in again.\n(detail: %s)", ae.Error())
				}
				fmt.Fprintln(os.Stderr, "Session expired — starting a fresh login.")
				if err := deviceLogin(client, store, paths.Server); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}

	// Warn about allocation keys that cannot match a real model, and about
	// non-positive or non-finite values. These would otherwise vanish without a
	// trace, which is the failure mode this tool exists to prevent.
	if known, kerr := client.SubscriptionModels(); kerr == nil {
		ids := make([]string, 0, len(known))
		for _, m := range known {
			ids = append(ids, m.ID)
		}
		// Models seen in usage are valid too: a model can be used without being
		// in the current plan list.
		if st, serr := client.GoStatus(); serr == nil && st.Access != nil {
			for _, w := range budget.All() {
				meter := budget.MeterOf(st.Access.Meters, w)
				since := meter.StartsAt
				if since.IsZero() {
					since = st.Access.StartsAt
				}
				if rows, uerr := client.UsageSince(since); uerr == nil {
					for _, r := range rows {
						ids = append(ids, r.Model)
					}
				}
			}
		}
		for _, problem := range config.CheckAllocations(cfg, ids) {
			fmt.Fprintf(os.Stderr, "config %s: %s\n", paths.ConfigFile, problem)
		}
	}

	loader := tui.Loader{
		Client:     client,
		Config:     cfg,
		ConfigPath: paths.ConfigFile,
		Allowances: &allowance.Resolver{},
		Timeout:    45 * time.Second,
	}

	if *once {
		out, rerr := tui.RunOnce(context.Background(), loader)
		if rerr != nil {
			return rerr
		}
		fmt.Print(out)
		return nil
	}

	interval := time.Duration(cfg.RefreshSeconds) * time.Second
	if *refresh > 0 {
		interval = *refresh
	}

	app := tui.NewApp(loader, interval, cfg.WarningPercent, cfg.DangerPercent)
	p := tea.NewProgram(app, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	return nil
}

// deviceLogin runs the same device authorization flow the opencode CLI uses,
// then persists the credential in the exact shape that CLI writes so both tools
// stay signed in.
func deviceLogin(client *console.Client, store *console.Store, server string) error {
	hc := &http.Client{Timeout: 30 * time.Second}
	dc, err := console.BeginDeviceLogin(hc, server)
	if err != nil {
		return fmt.Errorf("start device login: %w", err)
	}
	fmt.Println()
	fmt.Println("  Sign in to OpenCode Console to continue.")
	fmt.Println()
	fmt.Printf("    Code: %s\n", dc.UserCode)
	if dc.VerifyURL != "" {
		fmt.Printf("    URL:  %s\n", dc.VerifyURL)
	}
	fmt.Println()
	fmt.Println("  Waiting for approval…")

	cred, err := console.PollDeviceToken(hc, server, dc, nil)
	if err != nil {
		return fmt.Errorf("device login: %w", err)
	}
	// Fill account and org identity so the file matches what the CLI writes.
	cred, err = console.DescribeAccount(hc, cred)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read account details: %v\n", err)
	}
	if err := store.Save(cred); err != nil {
		return fmt.Errorf("save credential: %w", err)
	}
	fmt.Printf("  Signed in as %s\n\n", cred.Email)
	if err := client.Prepare(); err != nil {
		return err
	}
	return nil
}

// writeStarterConfig seeds a config file from the plan's models so the operator
// has real model IDs to allocate rather than guesses.
func writeStarterConfig(client *console.Client, paths config.Paths, cfg config.Config) error {
	if _, err := os.Stat(paths.ConfigFile); err == nil {
		return fmt.Errorf("config already exists at %s; not overwriting", paths.ConfigFile)
	}
	if err := client.Prepare(); err != nil {
		return err
	}
	models, err := client.SubscriptionModels()
	if err != nil {
		return fmt.Errorf("read plan models: %w", err)
	}
	status, err := client.GoStatus()
	if err != nil {
		return fmt.Errorf("read plan status: %w", err)
	}

	// Seed every enabled model at 0 so the file documents real IDs; the
	// operator fills in the numbers they care about.
	allocs := map[string]float64{}
	for _, m := range models {
		if m.Enabled {
			allocs[m.ID] = 0
		}
	}
	cfg.Allocations = allocs

	if err := config.Write(paths.ConfigFile, cfg); err != nil {
		return err
	}
	fmt.Printf("Wrote %s with %d models.\n\n", paths.ConfigFile, len(allocs))
	if status.Access != nil {
		fmt.Printf("Plan allowance: 5h $%.2f, weekly $%.2f, monthly $%.2f\n",
			status.Access.Meters.FiveHour.LimitMicroCents.USD(),
			status.Access.Meters.Week.LimitMicroCents.USD(),
			status.Access.Meters.Month.LimitMicroCents.USD())
		fmt.Println("Set each model's monthly USD share in [allocations], then run opencode-limits.")
	}
	return nil
}

// runAllowances prints the per-model monthly limits, optionally diffing the
// embedded copy against the live one so upstream price changes are visible.
func runAllowances(check bool) error {
	ctx := context.Background()
	embedded := allowance.Embedded()

	if !check {
		fmt.Printf("%-30s %10s %10s %10s\n", "MODEL", "GO", "GO PLUS", "GO TEAMS")
		fmt.Println(strings.Repeat("-", 64))
		for _, id := range embedded.ModelIDs() {
			l, _ := embedded.Lookup(id)
			fmt.Printf("%-30s %10s %10s %10s\n", id,
				l.String("go"), l.String("go-plus"), l.String("go-teams"))
		}
		fmt.Printf("\n%d models. These are the plan's published per-model monthly caps,\n", len(embedded))
		fmt.Println("independent of each other - not a shared budget.")
		return nil
	}

	live, err := allowance.Fetch(ctx, nil)
	if err != nil {
		return fmt.Errorf("refresh allowances: %w", err)
	}
	diffs := allowance.Compare(embedded, live)
	if len(diffs) == 0 {
		fmt.Println("embedded allowance table matches the live one.")
		return nil
	}
	fmt.Printf("%d differences between the embedded table and the live one:\n", len(diffs))
	for _, d := range diffs {
		fmt.Println("  " + d)
	}
	return nil
}
