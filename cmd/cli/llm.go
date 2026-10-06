package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/alash3al/stash/internal/llm"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/urfave/cli/v3"
)

// llmCommand manages model providers from the server host. It talks to the
// database directly, so it works before any admin credential or console
// login exists, which is how a fresh deployment registers its first provider.
func llmCommand() *cli.Command {
	return &cli.Command{
		Name:  "llm",
		Usage: "Manage model providers and which feature uses which model",
		Commands: []*cli.Command{
			{
				Name:   "status",
				Usage:  "Show how each feature resolves to a provider and model",
				Action: llmStatusCmd,
				Flags:  []cli.Flag{&cli.BoolFlag{Name: "json", Usage: "Print JSON instead of a table"}},
			},
			{
				Name:  "provider",
				Usage: "Register and edit providers",
				Commands: []*cli.Command{
					{Name: "list", Usage: "List providers", Action: llmProviderListCmd},
					{
						Name:      "add",
						Usage:     "Register an OpenAI-compatible provider",
						ArgsUsage: "<name>",
						Action:    llmProviderAddCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "base-url", Usage: "Endpoint such as https://api.openai.com/v1", Required: true},
							&cli.StringFlag{Name: "api-key", Usage: "API key; omit for endpoints without authentication"},
							&cli.StringFlag{Name: "api-key-env", Usage: "Read the API key from this environment variable instead"},
							&cli.IntFlag{Name: "timeout", Value: 120, Usage: "Request timeout in seconds"},
						},
					},
					{
						Name:      "update",
						Usage:     "Change a provider",
						ArgsUsage: "<name|id>",
						Action:    llmProviderUpdateCmd,
						Flags: []cli.Flag{
							&cli.StringFlag{Name: "base-url"},
							&cli.StringFlag{Name: "api-key", Usage: "New API key; pass an empty string to clear it"},
							&cli.StringFlag{Name: "api-key-env"},
							&cli.IntFlag{Name: "timeout", Usage: "Request timeout in seconds"},
							&cli.BoolFlag{Name: "enabled"},
							&cli.BoolFlag{Name: "disabled"},
						},
					},
					{Name: "remove", Usage: "Delete an unassigned provider", ArgsUsage: "<name|id>", Action: llmProviderRemoveCmd},
					{Name: "probe", Usage: "List the models an endpoint exposes", ArgsUsage: "<name|id>", Action: llmProviderProbeCmd},
				},
			},
			{
				Name:      "assign",
				Usage:     "Route a feature to a provider and model",
				ArgsUsage: "<feature>",
				Action:    llmAssignCmd,
				Flags: []cli.Flag{
					&cli.StringFlag{Name: "provider", Usage: "Provider name or id", Required: true},
					&cli.StringFlag{Name: "model", Required: true},
					&cli.IntFlag{Name: "dimensions", Usage: "Vector size (embedding feature only)"},
					&cli.IntFlag{Name: "context-tokens", Usage: "Model context window; 0 learns it from the provider"},
					&cli.IntFlag{Name: "reserved-tokens", Usage: "Reasoning space kept for instructions and output"},
				},
			},
			{Name: "unassign", Usage: "Return a feature to the STASH_OPENAI_* environment provider", ArgsUsage: "<feature>", Action: llmUnassignCmd},
			{Name: "import-env", Usage: "Copy the STASH_OPENAI_* configuration into the database as the 'environment' provider", Action: llmImportEnvCmd},
		},
	}
}

func llmStatusCmd(ctx context.Context, cmd *cli.Command) error {
	bc := getBootstrap(cmd)
	if err := bc.LLM.Reload(ctx); err != nil {
		return err
	}
	if cmd.Bool("json") {
		return printJSON(bc.LLM.Status())
	}
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)
	t.SetStyle(table.StyleLight)
	t.AppendHeader(table.Row{"Feature", "Kind", "Source", "Provider", "Model", "Dims", "Status"})
	for _, route := range bc.LLM.Status() {
		status := "ok"
		if !route.Available {
			status = route.Error
		}
		dims := ""
		if route.Dimensions > 0 {
			dims = strconv.Itoa(route.Dimensions)
		}
		t.AppendRow(table.Row{route.Feature, route.Kind, route.Source, route.ProviderName, route.Model, dims, status})
	}
	t.Render()
	return nil
}

func llmProviderListCmd(ctx context.Context, cmd *cli.Command) error {
	providers, err := getBootstrap(cmd).LLMStore.ListProviders(ctx)
	if err != nil {
		return err
	}
	return printJSON(providers)
}

func llmAPIKeyFlag(cmd *cli.Command) (*string, error) {
	if envName := strings.TrimSpace(cmd.String("api-key-env")); envName != "" {
		value, ok := os.LookupEnv(envName)
		if !ok {
			return nil, fmt.Errorf("environment variable %s is not set", envName)
		}
		return &value, nil
	}
	if cmd.IsSet("api-key") {
		value := cmd.String("api-key")
		return &value, nil
	}
	return nil, nil
}

func llmProviderAddCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return fmt.Errorf("provider name is required")
	}
	name, baseURL, timeout := cmd.Args().First(), cmd.String("base-url"), int(cmd.Int("timeout"))
	apiKey, err := llmAPIKeyFlag(cmd)
	if err != nil {
		return err
	}
	provider, err := getBootstrap(cmd).LLMStore.CreateProvider(ctx, llm.ProviderInput{
		Name: &name, BaseURL: &baseURL, APIKey: apiKey, RequestTimeoutSeconds: &timeout,
	})
	if err != nil {
		return err
	}
	return printJSON(provider)
}

func llmProviderUpdateCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return fmt.Errorf("provider name or id is required")
	}
	bc := getBootstrap(cmd)
	provider, err := bc.LLMStore.FindProvider(ctx, cmd.Args().First())
	if err != nil {
		return err
	}
	var input llm.ProviderInput
	if cmd.IsSet("base-url") {
		value := cmd.String("base-url")
		input.BaseURL = &value
	}
	if input.APIKey, err = llmAPIKeyFlag(cmd); err != nil {
		return err
	}
	if cmd.IsSet("timeout") {
		value := int(cmd.Int("timeout"))
		input.RequestTimeoutSeconds = &value
	}
	if cmd.Bool("enabled") || cmd.Bool("disabled") {
		value := cmd.Bool("enabled") && !cmd.Bool("disabled")
		input.Enabled = &value
	}
	updated, err := bc.LLMStore.UpdateProvider(ctx, provider.ID, input)
	if err != nil {
		return err
	}
	return printJSON(updated)
}

func llmProviderRemoveCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return fmt.Errorf("provider name or id is required")
	}
	bc := getBootstrap(cmd)
	provider, err := bc.LLMStore.FindProvider(ctx, cmd.Args().First())
	if err != nil {
		return err
	}
	if err := bc.LLMStore.DeleteProvider(ctx, provider.ID); err != nil {
		return err
	}
	return printJSON(map[string]any{"deleted": provider.ID, "name": provider.Name})
}

func llmProviderProbeCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return fmt.Errorf("provider name or id is required")
	}
	bc := getBootstrap(cmd)
	provider, err := bc.LLMStore.FindProvider(ctx, cmd.Args().First())
	if err != nil {
		return err
	}
	apiKey, err := bc.LLMStore.OpenAPIKey(ctx, provider.ID)
	if err != nil {
		return err
	}
	return printJSON(llm.Probe(ctx, provider.BaseURL, apiKey, provider.Timeout()))
}

func llmAssignCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return fmt.Errorf("feature is required (one of: %s)", featureNames())
	}
	feature, err := llm.ParseFeature(cmd.Args().First())
	if err != nil {
		return err
	}
	bc := getBootstrap(cmd)
	provider, err := bc.LLMStore.FindProvider(ctx, cmd.String("provider"))
	if err != nil {
		return err
	}
	assignment, err := bc.LLMStore.SetAssignment(ctx, llm.Assignment{
		Feature:        feature,
		ProviderID:     provider.ID,
		Model:          cmd.String("model"),
		Dimensions:     int(cmd.Int("dimensions")),
		ContextTokens:  int(cmd.Int("context-tokens")),
		ReservedTokens: int(cmd.Int("reserved-tokens")),
	})
	if err != nil {
		return err
	}
	return printJSON(assignment)
}

func llmUnassignCmd(ctx context.Context, cmd *cli.Command) error {
	if cmd.Args().Len() != 1 {
		return fmt.Errorf("feature is required (one of: %s)", featureNames())
	}
	feature, err := llm.ParseFeature(cmd.Args().First())
	if err != nil {
		return err
	}
	if err := getBootstrap(cmd).LLMStore.ClearAssignment(ctx, feature); err != nil {
		return err
	}
	return printJSON(map[string]any{"cleared": feature})
}

func llmImportEnvCmd(ctx context.Context, cmd *cli.Command) error {
	provider, assignments, err := getBootstrap(cmd).LLM.ImportEnvironment(ctx)
	if err != nil {
		return err
	}
	return printJSON(map[string]any{"provider": provider, "assignments": assignments})
}

func featureNames() string {
	names := make([]string, 0, len(llm.Features()))
	for _, info := range llm.Features() {
		names = append(names, string(info.Feature))
	}
	return strings.Join(names, ", ")
}
