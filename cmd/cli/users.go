package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/alash3al/stash/internal/auth"
	"github.com/urfave/cli/v3"
)

// userCommand manages local console accounts from the server host. The first
// administrator usually comes from STASH_ADMIN_USER; this is how more people
// get accounts without an SSO provider.
func userCommand() *cli.Command {
	passwordFlags := []cli.Flag{
		&cli.StringFlag{Name: "password", Usage: "Password (prefer --password-env or --password-stdin to keep it out of shell history)"},
		&cli.StringFlag{Name: "password-env", Usage: "Read the password from this environment variable"},
		&cli.BoolFlag{Name: "password-stdin", Usage: "Read the password from the first line of standard input"},
	}
	return &cli.Command{
		Name:  "user",
		Usage: "Manage console users (passwords, admin flag, SSO identities)",
		Commands: []*cli.Command{
			{Name: "list", Usage: "List users with their identities", Action: userListCmd},
			{
				Name: "add", Usage: "Create a user", ArgsUsage: "<username>", Action: userAddCmd,
				Flags: append([]cli.Flag{&cli.BoolFlag{Name: "admin", Usage: "Grant the server settings pages"}, &cli.StringFlag{Name: "display-name"}, &cli.BoolFlag{Name: "no-password", Usage: "Create the user without a password (for SSO or a later 'stash user passwd')"}}, passwordFlags...),
			},
			{Name: "passwd", Usage: "Set or change a user's password", ArgsUsage: "<username>", Action: userPasswdCmd, Flags: passwordFlags},
			{
				Name: "set", Usage: "Change admin, enabled, or display name", ArgsUsage: "<username>", Action: userSetCmd,
				Flags: []cli.Flag{&cli.BoolFlag{Name: "admin"}, &cli.BoolFlag{Name: "no-admin"}, &cli.BoolFlag{Name: "enable"}, &cli.BoolFlag{Name: "disable"}, &cli.StringFlag{Name: "display-name"}},
			},
			{Name: "remove", Usage: "Delete a user and revoke its API tokens (its memory stays)", ArgsUsage: "<username>", Action: userRemoveCmd},
		},
	}
}

func userProvider(cmd *cli.Command) (*auth.Provider, error) {
	bc := getBootstrap(cmd)
	if bc == nil || bc.Auth == nil {
		return nil, fmt.Errorf("user accounts need STASH_AUTH_MODE=token or oauth")
	}
	return bc.Auth, nil
}

func userArg(cmd *cli.Command) (string, error) {
	if cmd.Args().Len() != 1 {
		return "", fmt.Errorf("username is required")
	}
	return cmd.Args().First(), nil
}

func userPassword(cmd *cli.Command) (string, error) {
	if name := strings.TrimSpace(cmd.String("password-env")); name != "" {
		value, ok := os.LookupEnv(name)
		if !ok {
			return "", fmt.Errorf("environment variable %s is not set", name)
		}
		return value, nil
	}
	if cmd.Bool("password-stdin") {
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return "", fmt.Errorf("read password from stdin: %w", err)
		}
		return strings.TrimRight(line, "\r\n"), nil
	}
	if cmd.IsSet("password") {
		return cmd.String("password"), nil
	}
	return "", fmt.Errorf("--password, --password-env, or --password-stdin is required")
}

func userListCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := userProvider(cmd)
	if err != nil {
		return err
	}
	users, err := provider.ListUsers(ctx)
	if err != nil {
		return err
	}
	return printJSON(users)
}

func userAddCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := userProvider(cmd)
	if err != nil {
		return err
	}
	username, err := userArg(cmd)
	if err != nil {
		return err
	}
	if cmd.Bool("no-password") {
		user, err := provider.CreateUser(ctx, username, cmd.String("display-name"), cmd.Bool("admin"))
		if err != nil {
			return err
		}
		return printJSON(user)
	}
	password, err := userPassword(cmd)
	if err != nil {
		return err
	}
	user, err := provider.CreateLocalUser(ctx, username, password, cmd.String("display-name"), cmd.Bool("admin"))
	if err != nil {
		return err
	}
	return printJSON(user)
}

func userPasswdCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := userProvider(cmd)
	if err != nil {
		return err
	}
	username, err := userArg(cmd)
	if err != nil {
		return err
	}
	password, err := userPassword(cmd)
	if err != nil {
		return err
	}
	if err := provider.SetLocalPassword(ctx, username, password); err != nil {
		return err
	}
	return printJSON(map[string]any{"username": username, "password_changed": true})
}

func userSetCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := userProvider(cmd)
	if err != nil {
		return err
	}
	username, err := userArg(cmd)
	if err != nil {
		return err
	}
	var update auth.UserUpdate
	if cmd.Bool("admin") || cmd.Bool("no-admin") {
		admin := cmd.Bool("admin") && !cmd.Bool("no-admin")
		update.IsAdmin = &admin
	}
	if cmd.Bool("enable") || cmd.Bool("disable") {
		disabled := cmd.Bool("disable") && !cmd.Bool("enable")
		update.Disabled = &disabled
	}
	if cmd.IsSet("display-name") {
		name := cmd.String("display-name")
		update.DisplayName = &name
	}
	user, err := provider.UpdateUser(ctx, username, update)
	if err != nil {
		return err
	}
	return printJSON(user)
}

func userRemoveCmd(ctx context.Context, cmd *cli.Command) error {
	provider, err := userProvider(cmd)
	if err != nil {
		return err
	}
	username, err := userArg(cmd)
	if err != nil {
		return err
	}
	if err := provider.DeleteUser(ctx, username); err != nil {
		return err
	}
	return printJSON(map[string]any{"username": username, "deleted": true})
}
