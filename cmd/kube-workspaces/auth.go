package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"golang.org/x/term"

	"github.com/kube-workspaces/desktop-client/internal/config"
	"github.com/kube-workspaces/desktop-client/internal/kwclient"
)

// clientFor builds an API client for the named profile, or the active one when
// name is empty. The stored session token is attached if present.
func clientFor(profileName string) (*kwclient.Client, *config.Profile, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	var profile *config.Profile
	if profileName != "" {
		profile = cfg.Get(profileName)
		if profile == nil {
			return nil, nil, fmt.Errorf("no such profile %q", profileName)
		}
	} else {
		profile, err = cfg.Active()
		if err != nil {
			return nil, nil, err
		}
	}

	opts := []kwclient.Option{kwclient.WithUserAgent("kube-workspaces-desktop/" + version)}
	if profile.InsecureSkipVerify {
		opts = append(opts, kwclient.WithInsecureSkipVerify(true))
	}
	token, err := config.LoadToken(profile.Name)
	switch {
	case err == nil:
		opts = append(opts, kwclient.WithToken(token))
	case errors.Is(err, config.ErrNoToken):
		// Leave the client unauthenticated; the caller reports the failure in
		// context, which is friendlier than erroring here.
	default:
		return nil, nil, err
	}

	client, err := kwclient.New(profile.Server, opts...)
	if err != nil {
		return nil, nil, err
	}
	return client, profile, nil
}

func runLogin(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "", "instance base URL, e.g. https://workspaces.example.com (required for a new profile)")
	email := fs.String("email", "", "account email for local authentication")
	token := fs.String("token", "", "use a pre-issued session token instead of logging in (also reads KUBE_WORKSPACES_TOKEN)")
	name := fs.String("name", "", "profile name (defaults to the server hostname)")
	namespace := fs.String("namespace", "", "pin this profile to a single namespace")
	insecure := fs.Bool("insecure", false, "skip TLS certificate verification (dev clusters only)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: kube-workspaces login --server <url> [--email <email>]\n\n")
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	profileName := *name
	if profileName == "" && *server != "" {
		profileName = config.ProfileNameForServer(*server)
	}
	// Allow re-login to an existing profile without repeating --server.
	if *server == "" {
		existing, err := cfg.Active()
		if err != nil {
			return errors.New("--server is required when no profile exists yet")
		}
		*server = existing.Server
		profileName = existing.Name
		if *namespace == "" {
			*namespace = existing.Namespace
		}
		if !*insecure {
			*insecure = existing.InsecureSkipVerify
		}
	}

	profile := &config.Profile{
		Name:               profileName,
		Server:             strings.TrimRight(*server, "/"),
		Email:              *email,
		Namespace:          *namespace,
		InsecureSkipVerify: *insecure,
	}

	opts := []kwclient.Option{kwclient.WithUserAgent("kube-workspaces-desktop/" + version)}
	if profile.InsecureSkipVerify {
		opts = append(opts, kwclient.WithInsecureSkipVerify(true))
	}
	client, err := kwclient.New(profile.Server, opts...)
	if err != nil {
		return err
	}

	sessionToken := *token
	if sessionToken == "" {
		sessionToken = os.Getenv("KUBE_WORKSPACES_TOKEN")
	}

	if sessionToken == "" {
		sessionToken, err = interactiveLogin(ctx, client, profile)
		if err != nil {
			return err
		}
	}

	// Validate before persisting: storing a token that does not work would
	// leave the user with a profile that fails mysteriously on every command.
	client.SetToken(sessionToken)
	identity, err := client.Me(ctx)
	if err != nil {
		return fmt.Errorf("verify session: %w", err)
	}
	if !identity.Authenticated && identity.AuthEnabled {
		return errors.New("session token was rejected by the server")
	}

	storage, err := config.SaveToken(profile.Name, sessionToken)
	if err != nil {
		return err
	}
	if identity.Email != "" {
		profile.Email = identity.Email
	}
	cfg.Put(profile)
	if err := cfg.Save(); err != nil {
		return err
	}

	who := identity.Email
	if who == "" {
		who = "(authentication disabled)"
	}
	fmt.Printf("Logged in to %s as %s", profile.Server, who)
	if identity.Role != "" {
		fmt.Printf(" [%s]", identity.Role)
	}
	fmt.Printf("\nProfile %q saved; token stored in %s.\n", profile.Name, storage)
	if storage == config.StorageFile {
		fmt.Fprintf(os.Stderr, "warning: no OS keychain available, token written to a 0600 file\n")
	}
	if claims, err := kwclient.ParseToken(sessionToken); err == nil {
		fmt.Printf("Session expires %s.\n", claims.ExpiresAt().Local().Format("2006-01-02 15:04:05 MST"))
	}
	return nil
}

// interactiveLogin performs local (username/password) authentication.
//
// OIDC is deliberately not attempted here: a native app must not collect
// credentials for a third-party identity provider, and the platform API does
// not yet offer the RFC 8252 loopback flow that would let the system browser
// do it properly. Until it does, OIDC users pass --token.
func interactiveLogin(ctx context.Context, client *kwclient.Client, profile *config.Profile) (string, error) {
	authCfg, err := client.AuthConfig(ctx)
	if err != nil {
		return "", fmt.Errorf("query auth configuration: %w", err)
	}
	if !authCfg.Enabled {
		return "", errors.New("authentication is disabled on this instance; no token is needed")
	}
	if !authCfg.LocalAuth.Enabled {
		return "", fmt.Errorf("this instance uses OIDC (%s); pass --token with a session token until browser login lands",
			authCfg.IssuerURL)
	}

	email := profile.Email
	if email == "" {
		email, err = prompt("Email: ")
		if err != nil {
			return "", err
		}
	}
	password, err := promptPassword("Password: ")
	if err != nil {
		return "", err
	}
	profile.Email = email

	token, mustChange, err := client.LoginLocal(ctx, email, password)
	if err != nil {
		return "", err
	}
	if mustChange {
		fmt.Fprintln(os.Stderr, "warning: this account must change its password in the web UI")
	}
	return token, nil
}

func prompt(label string) (string, error) {
	fmt.Fprint(os.Stderr, label)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

func promptPassword(label string) (string, error) {
	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		// Reading a password from a pipe is legitimate for scripting, but it
		// must not echo to a terminal that is not there.
		return prompt(label)
	}
	fmt.Fprint(os.Stderr, label)
	data, err := term.ReadPassword(fd)
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func runLogout(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("logout", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to log out of (defaults to the active profile)")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	profile := cfg.Get(*profileName)
	if *profileName == "" {
		profile, err = cfg.Active()
		if err != nil {
			return err
		}
	}
	if profile == nil {
		return fmt.Errorf("no such profile %q", *profileName)
	}
	if err := config.DeleteToken(profile.Name); err != nil {
		return err
	}
	fmt.Printf("Logged out of %s.\n", profile.Name)
	return nil
}

func runWhoAmI(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("whoami", flag.ExitOnError)
	profileName := fs.String("profile", "", "profile to use")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	client, profile, err := clientFor(*profileName)
	if err != nil {
		return err
	}
	identity, err := client.Me(ctx)
	if err != nil {
		return err
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "Profile:\t%s\n", profile.Name)
	fmt.Fprintf(w, "Server:\t%s\n", profile.Server)
	fmt.Fprintf(w, "Authenticated:\t%t\n", identity.Authenticated)
	if identity.Email != "" {
		fmt.Fprintf(w, "Email:\t%s\n", identity.Email)
	}
	if identity.Role != "" {
		fmt.Fprintf(w, "Role:\t%s\n", identity.Role)
	}
	if identity.PersonalNamespace != "" {
		fmt.Fprintf(w, "Personal namespace:\t%s\n", identity.PersonalNamespace)
	}
	if len(identity.Namespaces) > 0 {
		fmt.Fprintf(w, "Namespaces:\t%s\n", strings.Join(identity.Namespaces, ", "))
	}
	if token, err := config.LoadToken(profile.Name); err == nil {
		if claims, err := kwclient.ParseToken(token); err == nil {
			fmt.Fprintf(w, "Session expires:\t%s\n", claims.ExpiresAt().Local().Format("2006-01-02 15:04:05 MST"))
		}
	}
	return w.Flush()
}

func runProfile(_ context.Context, args []string) error {
	fs := flag.NewFlagSet("profile", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage:\n  kube-workspaces profile list\n"+
			"  kube-workspaces profile use <name>\n"+
			"  kube-workspaces profile remove <name>\n")
	}
	if err := parseFlags(fs, args); err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	sub := "list"
	if fs.NArg() > 0 {
		sub = fs.Arg(0)
	}
	switch sub {
	case "list":
		if len(cfg.Profiles) == 0 {
			fmt.Println("No profiles configured. Run `kube-workspaces login --server <url>`.")
			return nil
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "\tNAME\tSERVER\tEMAIL\tTOKEN")
		for _, n := range cfg.Names() {
			p := cfg.Profiles[n]
			marker := " "
			if n == cfg.Current {
				marker = "*"
			}
			tokenState := "none"
			if _, err := config.LoadToken(n); err == nil {
				tokenState = "stored"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", marker, p.Name, p.Server, p.Email, tokenState)
		}
		return w.Flush()

	case "use":
		if fs.NArg() < 2 {
			return errors.New("usage: kube-workspaces profile use <name>")
		}
		target := fs.Arg(1)
		if cfg.Get(target) == nil {
			return fmt.Errorf("no such profile %q", target)
		}
		cfg.Current = target
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Active profile is now %q.\n", target)
		return nil

	case "remove":
		if fs.NArg() < 2 {
			return errors.New("usage: kube-workspaces profile remove <name>")
		}
		if err := cfg.Remove(fs.Arg(1)); err != nil {
			return err
		}
		if err := cfg.Save(); err != nil {
			return err
		}
		fmt.Printf("Removed profile %q.\n", fs.Arg(1))
		return nil

	default:
		return fmt.Errorf("unknown profile subcommand %q", sub)
	}
}
