// Command kube-workspaces is the Kube Workspaces desktop client.
//
// Run with no arguments it opens the graphical shell, which is what a desktop
// application does when it is launched from a menu or a dock. The subcommands
// remain for scripting and for diagnosing the client libraries against a real
// instance; `kube-workspaces shell` names the default explicitly.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"

	"github.com/kube-workspaces/desktop-client/internal/console"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string) error
}

func main() {
	// First, before anything can print. The Windows build is linked as a GUI
	// subsystem binary so that launching it from Explorer does not open a
	// stray console window alongside the shell; the cost is that the
	// subcommands below inherit no standard streams when they are run from
	// cmd.exe or PowerShell. This reattaches them. No-op everywhere else —
	// see internal/console.
	console.AttachParent()

	commands := []command{
		shellCommand(),
		{"login", "Authenticate against a kube-workspaces instance", runLogin},
		{"logout", "Forget the stored session token for a profile", runLogout},
		{"profile", "List, select and remove instance profiles", runProfile},
		{"whoami", "Show the authenticated identity", runWhoAmI},
		{"list", "List workspaces", runList},
		connectCommand(),
		webCommand(),
		{"probe", "Probe a VM workspace's display capabilities and bandwidth", runProbe},
		{"screenshot", "Capture a VM workspace's display to a PNG file", runScreenshot},
		{"version", "Print the client version", runVersion},
	}

	// Ctrl-C should tear down a live session cleanly: the server's console slot
	// is single-session, so an abrupt exit would hold the display until the
	// idle timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// No arguments opens the shell. This is a desktop application: launched
	// from a menu, a dock or a .desktop file it gets no argv, and printing a
	// usage message to a stderr nobody is reading would look like a crash.
	name, args := "shell", []string(nil)
	if len(os.Args) > 1 {
		name, args = os.Args[1], os.Args[2:]
	}
	if name == "-h" || name == "--help" || name == "help" {
		usage(commands)
		return
	}
	// A leading flag belongs to the default command, so `kube-workspaces -v`
	// means what it looks like rather than "unknown command -v". An unknown
	// flag is still rejected — by the shell's own flag set, which can say
	// which flag it was.
	if strings.HasPrefix(name, "-") {
		name, args = "shell", os.Args[1:]
	}

	for _, c := range commands {
		if c.name != name {
			continue
		}
		if err := c.run(ctx, args); err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			fmt.Fprintf(os.Stderr, "kube-workspaces: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Fprintf(os.Stderr, "kube-workspaces: unknown command %q\n\n", name)
	usage(commands)
	os.Exit(2)
}

func usage(commands []command) {
	fmt.Fprintf(os.Stderr, "Kube Workspaces desktop client %s\n\n", version)
	fmt.Fprintf(os.Stderr, "Usage:\n  kube-workspaces [<command>] [flags]\n\n"+
		"With no command it opens the graphical shell.\n\nCommands:\n")
	w := tabwriter.NewWriter(os.Stderr, 0, 0, 2, ' ', 0)
	for _, c := range commands {
		fmt.Fprintf(w, "  %s\t%s\n", c.name, c.summary)
	}
	_ = w.Flush()
	fmt.Fprintf(os.Stderr, "\nRun `kube-workspaces <command> -h` for command flags.\n")
}

func runVersion(_ context.Context, _ []string) error {
	fmt.Println(version)
	return nil
}
