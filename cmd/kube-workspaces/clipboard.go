package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/kube-workspaces/desktop-client/internal/agent"
	"github.com/kube-workspaces/desktop-client/internal/cmdutil"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/wsio"
)

// runClipboard uses the independent admitted agent seat, so VNC can remain
// open for keyboard/pointer interaction. No media decoders are required.
func runClipboard(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("clipboard", flag.ContinueOnError)
	profileName := fs.String("profile", "", "profile to use")
	namespace := fs.String("namespace", "", "workspace namespace")
	write := fs.Bool("write", false, "write UTF-8 text from stdin to the guest clipboard")
	read := fs.Bool("read", false, "read guest clipboard text to stdout (default)")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: kube-workspaces clipboard <workspace> [--read | --write]")
		fs.PrintDefaults()
	}
	if err := cmdutil.ParseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 || (*read && *write) {
		return fmt.Errorf("clipboard requires one workspace and either --read or --write")
	}
	var text *string
	if *write {
		raw, err := io.ReadAll(io.LimitReader(os.Stdin, agent.MaxClipboardBytes+1))
		if err != nil {
			return fmt.Errorf("read clipboard input: %w", err)
		}
		if len(raw) > agent.MaxClipboardBytes {
			return fmt.Errorf("clipboard input exceeds %d bytes", agent.MaxClipboardBytes)
		}
		value := string(raw)
		text = &value
	}
	client, profile, err := cmdutil.For(version, *profileName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	ns, err := cmdutil.ResolveNamespace(ctx, client, profile, *namespace, fs.Arg(0))
	if err != nil {
		return err
	}
	result, err := exchangeClipboard(ctx, client, ns, fs.Arg(0), text)
	if err != nil {
		return err
	}
	if !*write {
		if result == nil {
			return fmt.Errorf("guest clipboard has no text format")
		}
		_, err = io.WriteString(os.Stdout, *result)
	}
	return err
}

func exchangeClipboard(ctx context.Context, client *kwclient.Client, ns, name string, text *string) (*string, error) {
	ticket, err := client.AgentAttach(ctx, ns, name, "clipboard-cli")
	if err != nil {
		return nil, err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = client.AgentRelease(releaseCtx, ns, name, ticket.ID)
	}()
	conn, _, err := client.DialAgentWS(ctx, ns, name)
	if err != nil {
		return nil, err
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	session, err := agent.Attach(wsio.New(conn), ticket.Ticket)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer session.Close() //nolint:errcheck // best-effort teardown
	return session.Clipboard(text)
}
