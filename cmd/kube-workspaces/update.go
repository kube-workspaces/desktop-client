package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/update"
)

func runUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	check := fs.Bool("check", false, "report the latest release without installing")
	yes := fs.Bool("yes", false, "install without prompting (close other client processes first)")
	recoverUpdate := fs.Bool("recover", false, "restore an interrupted update after closing all other client processes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected update arguments")
	}
	if *recoverUpdate {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		return update.Recover(exe)
	}
	p, err := config.SentinelPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		return fmt.Errorf("updates disabled by %s", p)
	}
	in := &update.Installer{}
	r, err := in.Check(ctx, version)
	if err != nil {
		return err
	}
	fmt.Printf("Current: %s; latest: %s\n", version, r.Latest)
	if *check || !r.Available {
		return nil
	}
	if !*yes {
		fmt.Print("Close other client windows before updating. Install update? [y/N] ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil {
			return err
		}
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			return nil
		}
	}
	if err := in.Install(ctx, r.Release, nil); err != nil {
		return err
	}
	fmt.Println("Update installed. Launch Kube Workspaces to confirm it.")
	return nil
}
