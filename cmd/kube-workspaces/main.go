// Command kube-workspaces is the Kube Workspaces desktop client.
//
// The graphical shell is still being built; today the binary exposes the
// command-line surface used to drive and diagnose the underlying client
// libraries against a real instance.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, args []string) error
}

func main() {
	commands := []command{
		{"login", "Authenticate against a kube-workspaces instance", runLogin},
		{"logout", "Forget the stored session token for a profile", runLogout},
		{"profile", "List, select and remove instance profiles", runProfile},
		{"whoami", "Show the authenticated identity", runWhoAmI},
		{"list", "List workspaces", runList},
		{"probe", "Probe a VM workspace's display capabilities and bandwidth", runProbe},
		{"screenshot", "Capture a VM workspace's display to a PNG file", runScreenshot},
		{"version", "Print the client version", runVersion},
	}

	// Ctrl-C should tear down a live session cleanly: the server's console slot
	// is single-session, so an abrupt exit would hold the display until the
	// idle timeout.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if len(os.Args) < 2 {
		usage(commands)
		os.Exit(2)
	}
	name := os.Args[1]
	if name == "-h" || name == "--help" || name == "help" {
		usage(commands)
		return
	}

	for _, c := range commands {
		if c.name != name {
			continue
		}
		if err := c.run(ctx, os.Args[2:]); err != nil {
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
	fmt.Fprintf(os.Stderr, "Usage:\n  kube-workspaces <command> [flags]\n\nCommands:\n")
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

// parseFlags parses args allowing flags and positional arguments to appear in
// any order.
//
// The standard flag package stops parsing at the first non-flag argument, so
// `screenshot my-vm -o out.png` would silently ignore -o. Users reasonably
// expect the subject of a command to come first, so permute the arguments and
// hand flag a list it can handle.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) < 2 || arg[0] != '-' {
			positional = append(positional, arg)
			continue
		}
		flags = append(flags, arg)
		// "-flag=value" carries its own value; "-flag value" consumes the next
		// argument, but only for flags that actually take one.
		if strings.Contains(arg, "=") {
			continue
		}
		name := strings.TrimLeft(arg, "-")
		if f := fs.Lookup(name); f != nil && !isBoolFlag(f) && i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return fs.Parse(append(flags, positional...))
}

// isBoolFlag reports whether a flag is a boolean, which the flag package
// signals through an optional method on the value.
func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}
