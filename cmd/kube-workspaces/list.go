package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

func runList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to use")
	namespace := fs.String("namespace", "", "namespace to list (defaults to the profile's, or all)")
	runningOnly := fs.Bool("running", false, "only show workspaces that can be connected to")
	wide := fs.Bool("wide", false, "show image and resource columns")
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	client, profile, err := clientFor(*profileName)
	if err != nil {
		return err
	}

	ns := *namespace
	if ns == "" {
		ns = profile.Namespace
	}
	if ns == "" {
		ns = kwclient.AllNamespaces
	}

	workspaces, err := client.ListWorkspaces(ctx, ns)
	if err != nil {
		return err
	}

	// Stable, human-friendly ordering: connectable first, then by namespace and
	// name, so the thing you most likely want is at the top.
	sort.Slice(workspaces, func(i, j int) bool {
		a, b := workspaces[i], workspaces[j]
		if a.Running() != b.Running() {
			return a.Running()
		}
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})

	shown := 0
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	if *wide {
		fmt.Fprintln(w, "NAMESPACE\tNAME\tTYPE\tSTATUS\tIMAGE\tCPU\tMEMORY")
	} else {
		fmt.Fprintln(w, "NAMESPACE\tNAME\tTYPE\tSTATUS")
	}
	for _, ws := range workspaces {
		if *runningOnly && !ws.Running() {
			continue
		}
		shown++
		if *wide {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
				ws.Namespace, ws.Name, ws.Type, workspaceStatus(ws), ws.Image,
				orDash(ws.CPULimit), orDash(ws.MemoryLimit))
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ws.Namespace, ws.Name, ws.Type, workspaceStatus(ws))
		}
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if shown == 0 {
		fmt.Println("No workspaces found.")
	}
	return nil
}

// workspaceStatus renders the same status the web UI shows. The platform has no
// phase field, so the state is derived: stopped wins, then readiness, and
// anything else is still coming up (with the container's reason when known).
func workspaceStatus(ws kwclient.Workspace) string {
	switch {
	case ws.Stopped:
		return "stopped"
	case ws.ReadyReplicas > 0:
		return "running"
	case ws.ContainerState != nil && ws.ContainerState.Reason != "":
		return "starting (" + ws.ContainerState.Reason + ")"
	default:
		return "starting"
	}
}

func orDash(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}
