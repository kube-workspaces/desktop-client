// Copyright The kube-workspaces Authors.
// SPDX-License-Identifier: Apache-2.0

// Command selkies-probe is the isolated Spike D transport diagnostic. It is not
// wired into the graphical client or automatic transport selection.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
	"github.com/kube-workspaces/desktop-client/internal/selkies"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "selkies-probe:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	cfg := selkies.DefaultProbeConfig()
	fs := flag.NewFlagSet("selkies-probe", flag.ContinueOnError)
	server := fs.String("server", "", "platform HTTP(S) origin (uses KW_SESSION from environment)")
	direct := fs.String("direct", "", "isolated agent HTTP(S) origin; never sends the platform token")
	namespace := fs.String("namespace", "", "spike workspace namespace")
	workspace := fs.String("workspace", "", "spike workspace name")
	base := fs.String("base-path", "/", "agent-relative base path")
	fs.BoolVar(&cfg.Takeover, "takeover", false, "take over the session if in use")
	fs.IntVar(&cfg.Width, "width", cfg.Width, "requested desktop width")
	fs.IntVar(&cfg.Height, "height", cfg.Height, "requested desktop height")
	fs.IntVar(&cfg.FPS, "fps", cfg.FPS, "requested frames per second")
	fs.IntVar(&cfg.BitrateKbps, "bitrate", cfg.BitrateKbps, "requested H.264 bitrate in kbit/s")
	fs.BoolVar(&cfg.Audio, "audio", cfg.Audio, "request and require Opus packets")
	fs.DurationVar(&cfg.StartupTimeout, "startup-timeout", cfg.StartupTimeout, "budget for first H.264 keyframe")
	fs.DurationVar(&cfg.Duration, "duration", cfg.Duration, "measurement interval after first keyframe")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: selkies-probe --server OR --direct [flags]\n\nTargets Selkies "+selkies.Revision+".\nChanges capture settings/resolution: use a dedicated spike guest.\nCounts received payload and frames; does not decode, play, or measure input latency.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 || (*server == "") == (*direct == "") {
		return errors.New("specify exactly one of --server and --direct, without positional arguments")
	}
	if err := cfg.Validate(); err != nil {
		return err
	}
	origin := *server
	var options []kwclient.Option
	options = append(options, kwclient.WithHandshakeTimeout(5*time.Second))
	if *direct != "" {
		if *namespace != "" || *workspace != "" {
			return errors.New("--direct cannot be combined with --namespace or --workspace")
		}
		origin = *direct
	} else {
		options = append(options, kwclient.WithToken(os.Getenv("KW_SESSION")))
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") ||
		u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("server/direct must be an HTTP(S) origin without credentials, path, query, or fragment")
	}
	client, err := kwclient.New(origin, options...)
	if err != nil {
		return err
	}
	var conn *websocket.Conn
	if *direct != "" {
		path, pathErr := kwclient.SelkiesAgentPath(*base)
		if pathErr != nil {
			return pathErr
		}
		conn, _, err = client.DialWS(ctx, path, nil, nil)
	} else {
		if cfg.Takeover {
			fmt.Fprintln(os.Stderr, "selkies-probe: requesting Tier 1 takeover...")
			if err := client.Tier1Takeover(ctx, *namespace, *workspace); err != nil {
				return err
			}
		}
		conn, err = client.DialSelkies(ctx, *namespace, *workspace, *base)
	}
	if err != nil {
		return err
	}
	stats, probeErr := selkies.Probe(ctx, conn, cfg)
	result := struct {
		Complete bool                `json:"complete"`
		Config   selkies.ProbeConfig `json:"requested"`
		Stats    selkies.ProbeStats  `json:"stats"`
	}{Complete: probeErr == nil, Config: cfg, Stats: stats}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		return err
	}
	return probeErr
}
