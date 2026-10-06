package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/alash3al/stash/internal/auth"
	"github.com/urfave/cli/v3"
)

// ssoCommand manages the OIDC providers people can sign in with. The
// environment registers the first one at startup; this is the host-side way
// to add or fix one without the console.
func ssoCommand() *cli.Command {
	fields := []cli.Flag{
		&cli.StringFlag{Name: "name", Usage: "Label shown on the login button"},
		&cli.StringFlag{Name: "issuer", Usage: "OIDC issuer URL (its /.well-known/openid-configuration must exist)"},
		&cli.StringFlag{Name: "client-id"},
		&cli.StringFlag{Name: "client-secret", Usage: "Client secret (prefer --client-secret-env to keep it out of shell history)"},
		&cli.StringFlag{Name: "client-secret-env", Usage: "Read the client secret from this environment variable"},
		&cli.StringFlag{Name: "redirect-url", Usage: "Callback URL registered at the issuer, usually https://<stash>/auth/callback"},
	}
	return &cli.Command{
		Name:  "sso",
		Usage: "Manage SSO (OIDC) providers for console login",
		Commands: []*cli.Command{
			{Name: "list", Usage: "List providers with their load status", Action: ssoListCmd},
			{Name: "add", Usage: "Register a provider", ArgsUsage: "<slug>", Action: ssoAddCmd, Flags: append([]cli.Flag{&cli.BoolFlag{Name: "disabled", Usage: "Store it without offering it on the login page"}}, fields...)},
			{Name: "set", Usage: "Change a provider", ArgsUsage: "<id>", Action: ssoSetCmd, Flags: append([]cli.Flag{&cli.StringFlag{Name: "slug"}, &cli.BoolFlag{Name: "enable"}, &cli.BoolFlag{Name: "disable"}}, fields...)},
			{Name: "remove", Usage: "Delete a provider (users who used it keep their accounts)", ArgsUsage: "<id>", Action: ssoRemoveCmd},
			{Name: "test", Usage: "Run OIDC discovery for a stored provider", ArgsUsage: "<id>", Action: ssoTestCmd},
			{Name: "import-env", Usage: "Copy STASH_AUTH_ISSUER and friends into the table once", Action: ssoImportCmd},
		},
	}
}

func ssoProvider(cmd *cli.Command) (*auth.Provider, error) {
	bc := getBootstrap(cmd)
	if bc == nil || bc.Auth == nil {
		return nil, fmt.Errorf("SSO providers need STASH_AUTH_MODE=token or oauth")
	}
	return bc.Auth, nil
}

func ssoIDArg(cmd *cli.Command) (int64, error) {
	if cmd.Args().Len() != 1 {
		return 0, fmt.Errorf("provider id is required (see 'stash sso list')")
	}
	var id int64
	if _, err := fmt.Sscanf(cmd.Args().First(), "%d", &id); err != nil || id <= 0 {
		return 0, fmt.Errorf("provider id must be a positive number")
	}
	return id, nil
}

func ssoInput(cmd *cli.Command) (auth.SSOProviderInput, error) {
	var in auth.SSOProviderInput
	set := func(flag string, target **string) {
		if cmd.IsSet(flag) {
			value := cmd.String(flag)
			*target = &value
		}
	}
	set("name", &in.DisplayName)
	set("issuer", &in.Issuer)
	set("client-id", &in.ClientID)
	set("redirect-url", &in.RedirectURL)
	set("slug", &in.Slug)
	if name := strings.TrimSpace(cmd.String("client-secret-env")); name != "" {
		value, ok := os.LookupEnv(name)
		if !ok {
			return in, fmt.Errorf("environment variable %s is not set", name)
		}
		in.ClientSecret = &value
	} else if cmd.IsSet("client-secret") {
		value := cmd.String("client-secret")
		in.ClientSecret = &value
	}
	return in, nil
}

func ssoListCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := ssoProvider(cmd)
	if err != nil {
		return err
	}
	providers, err := provider.ListSSOProviders(ctx)
	if err != nil {
		return err
	}
	return printJSON(providers)
}

func ssoAddCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := ssoProvider(cmd)
	if err != nil {
		return err
	}
	if cmd.Args().Len() != 1 {
		return fmt.Errorf("slug is required, for example 'stash sso add authentik --issuer ...'")
	}
	in, err := ssoInput(cmd)
	if err != nil {
		return err
	}
	slug := cmd.Args().First()
	in.Slug = &slug
	if cmd.Bool("disabled") {
		enabled := false
		in.Enabled = &enabled
	}
	created, err := provider.CreateSSOProvider(ctx, in)
	if err != nil {
		return err
	}
	return printJSON(created)
}

func ssoSetCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := ssoProvider(cmd)
	if err != nil {
		return err
	}
	id, err := ssoIDArg(cmd)
	if err != nil {
		return err
	}
	in, err := ssoInput(cmd)
	if err != nil {
		return err
	}
	if cmd.Bool("enable") || cmd.Bool("disable") {
		enabled := cmd.Bool("enable") && !cmd.Bool("disable")
		in.Enabled = &enabled
	}
	updated, err := provider.UpdateSSOProvider(ctx, id, in)
	if err != nil {
		return err
	}
	return printJSON(updated)
}

func ssoRemoveCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := ssoProvider(cmd)
	if err != nil {
		return err
	}
	id, err := ssoIDArg(cmd)
	if err != nil {
		return err
	}
	if err := provider.DeleteSSOProvider(ctx, id); err != nil {
		return err
	}
	return printJSON(map[string]any{"id": id, "deleted": true})
}

func ssoTestCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := ssoProvider(cmd)
	if err != nil {
		return err
	}
	id, err := ssoIDArg(cmd)
	if err != nil {
		return err
	}
	if err := provider.TestSSOProvider(ctx, id); err != nil {
		return printJSON(map[string]any{"id": id, "ok": false, "error": err.Error()})
	}
	return printJSON(map[string]any{"id": id, "ok": true})
}

func ssoImportCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := ssoProvider(cmd)
	if err != nil {
		return err
	}
	imported, ok, err := provider.ImportEnvironmentSSO(ctx)
	if err != nil {
		return err
	}
	if !ok {
		return printJSON(map[string]any{"imported": false})
	}
	return printJSON(map[string]any{"imported": true, "provider": imported})
}
