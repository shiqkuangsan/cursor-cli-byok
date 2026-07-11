package command

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/textproto"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

type modelOptions struct {
	name           string
	baseURL        string
	endpoint       string
	apiKey         string
	apiKeyEnv      string
	headers        []string
	upstreamModel  string
	nonInteractive bool
}

const configHelp = `Usage:
  cursor-cli-byok config init [model flags] [--force]
  cursor-cli-byok config add [model flags]
  cursor-cli-byok config list
  cursor-cli-byok config use <name>
  cursor-cli-byok config remove <name>

Model flags:
  --name <alias>
  --base-url <url>
  --endpoint </v1/responses|/v1/chat/completions>
  --upstream-model <model>
  --api-key-env <environment-variable>  (preferred)
  --api-key <key>                       (stored inline)
  --header <name:value>                 (repeatable)
  --non-interactive
`

func (a App) runConfig(args []string) int {
	if len(args) == 0 {
		return usageExitCode
	}
	if args[0] == "--help" || args[0] == "-h" || args[0] == "help" {
		if _, err := io.WriteString(a.Stdout, configHelp); err != nil {
			return errorExitCode
		}
		return 0
	}

	switch args[0] {
	case "init":
		return a.runConfigInit(args[1:])
	case "add":
		return a.runConfigAdd(args[1:])
	case "list":
		return a.runConfigList(args[1:])
	case "use":
		return a.runConfigUse(args[1:])
	case "remove":
		return a.runConfigRemove(args[1:])
	default:
		return usageExitCode
	}
}

func (a App) runConfigRemove(args []string) int {
	if len(args) != 1 {
		return usageExitCode
	}
	name := args[0]
	resolvedPaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	store := config.NewStore(resolvedPaths.ConfigFile)
	cfg, err := store.Load()
	if err != nil {
		return a.fail(fmt.Errorf("config remove: %w", err))
	}
	if cfg.DefaultModel == name {
		return a.fail(fmt.Errorf("config remove: %q is the default model; run config use <other> first", name))
	}

	models := make([]config.Model, 0, len(cfg.Models))
	found := false
	for _, model := range cfg.Models {
		if model.Name == name {
			found = true
			continue
		}
		models = append(models, model)
	}
	if !found {
		return a.fail(fmt.Errorf("config remove: model %q does not exist", name))
	}
	cfg.Models = models
	if err := store.Save(cfg); err != nil {
		return a.fail(fmt.Errorf("config remove: %w", err))
	}
	if _, err := fmt.Fprintf(a.Stdout, "Removed model %s\n", name); err != nil {
		return errorExitCode
	}
	return 0
}

func (a App) runConfigUse(args []string) int {
	if len(args) != 1 {
		return usageExitCode
	}
	name := args[0]
	resolvedPaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	store := config.NewStore(resolvedPaths.ConfigFile)
	cfg, err := store.Load()
	if err != nil {
		return a.fail(fmt.Errorf("config use: %w", err))
	}
	found := false
	for _, model := range cfg.Models {
		if model.Name == name {
			found = true
			break
		}
	}
	if !found {
		return a.fail(fmt.Errorf("config use: model %q does not exist", name))
	}
	cfg.DefaultModel = name
	if err := store.Save(cfg); err != nil {
		return a.fail(fmt.Errorf("config use: %w", err))
	}
	if _, err := fmt.Fprintf(a.Stdout, "Default model: %s\n", name); err != nil {
		return errorExitCode
	}
	return 0
}

func (a App) runConfigList(args []string) int {
	if len(args) != 0 {
		return usageExitCode
	}
	resolvedPaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	cfg, err := config.NewStore(resolvedPaths.ConfigFile).Load()
	if err != nil {
		return a.fail(fmt.Errorf("config list: %w", err))
	}

	output := tabwriter.NewWriter(a.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(output, "MODEL\tPROTOCOL\tENDPOINT\tUPSTREAM\tKEY\tBASE URL"); err != nil {
		return errorExitCode
	}
	for _, model := range cfg.Models {
		marker := "  "
		if model.Name == cfg.DefaultModel {
			marker = "* "
		}
		keyStatus := "inline:[REDACTED]"
		if model.APIKeyEnv != "" {
			status := "unset"
			if a.Getenv(model.APIKeyEnv) != "" {
				status = "set"
			}
			keyStatus = fmt.Sprintf("env:%s (%s)", model.APIKeyEnv, status)
		}
		if _, err := fmt.Fprintf(
			output,
			"%s%s\t%s\t%s\t%s\t%s\t%s\n",
			marker,
			model.Name,
			model.Protocol,
			model.Endpoint,
			model.UpstreamModel,
			keyStatus,
			model.BaseURL,
		); err != nil {
			return errorExitCode
		}
	}
	if err := output.Flush(); err != nil {
		return errorExitCode
	}
	return 0
}

func (a App) runConfigAdd(args []string) int {
	options, _, err := parseModelOptions("config add", args, false)
	if err != nil {
		return a.fail(err)
	}
	resolvedPaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	store := config.NewStore(resolvedPaths.ConfigFile)
	cfg, err := store.Load()
	if err != nil {
		return a.fail(fmt.Errorf("config add: %w", err))
	}
	if !options.nonInteractive {
		options, err = a.completeModelOptions("config add", options)
		if err != nil {
			return a.fail(err)
		}
	}
	model, err := options.model()
	if err != nil {
		return a.fail(fmt.Errorf("config add: %w", err))
	}
	cfg.Models = append(cfg.Models, model)
	if err := store.Save(cfg); err != nil {
		return a.fail(fmt.Errorf("config add: %w", err))
	}
	if _, err := fmt.Fprintf(a.Stdout, "Added model %s\n", model.Name); err != nil {
		return errorExitCode
	}
	return 0
}

func (a App) runConfigInit(args []string) int {
	options, force, err := parseModelOptions("config init", args, true)
	if err != nil {
		return a.fail(err)
	}
	resolvedPaths, err := paths.Resolve(a.Getenv)
	if err != nil {
		return a.fail(err)
	}
	if !force {
		if _, err := os.Stat(resolvedPaths.ConfigFile); err == nil {
			return a.fail(errors.New("config init: configuration already exists; use --force to replace it"))
		} else if !errors.Is(err, os.ErrNotExist) {
			return a.fail(fmt.Errorf("config init: inspect existing configuration: %w", err))
		}
	}
	if !options.nonInteractive {
		options, err = a.completeModelOptions("config init", options)
		if err != nil {
			return a.fail(err)
		}
	}
	model, err := options.model()
	if err != nil {
		return a.fail(fmt.Errorf("config init: %w", err))
	}

	cfg := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: model.Name,
		Models:       []config.Model{model},
	}
	if err := config.NewStore(resolvedPaths.ConfigFile).Save(cfg); err != nil {
		return a.fail(fmt.Errorf("config init: %w", err))
	}
	if _, err := fmt.Fprintf(a.Stdout, "Initialized %s with model %s\n", resolvedPaths.ConfigFile, model.Name); err != nil {
		return errorExitCode
	}
	return 0
}

func (a App) completeModelOptions(command string, options modelOptions) (modelOptions, error) {
	reader := bufio.NewReader(a.Stdin)
	var err error
	if options.name == "" {
		options.name, err = a.promptLine(reader, "Model name: ", "")
		if err != nil {
			return modelOptions{}, fmt.Errorf("%s: %w", command, err)
		}
	}
	if options.baseURL == "" {
		options.baseURL, err = a.promptLine(reader, "Base URL: ", "")
		if err != nil {
			return modelOptions{}, fmt.Errorf("%s: %w", command, err)
		}
	}
	if options.endpoint == "" {
		options.endpoint, err = a.promptLine(reader, "Endpoint [/v1/responses]: ", config.EndpointResponses)
		if err != nil {
			return modelOptions{}, fmt.Errorf("%s: %w", command, err)
		}
	}
	if options.upstreamModel == "" {
		options.upstreamModel, err = a.promptLine(reader, "Upstream model: ", "")
		if err != nil {
			return modelOptions{}, fmt.Errorf("%s: %w", command, err)
		}
	}
	if options.apiKey == "" && options.apiKeyEnv == "" {
		source, err := a.promptLine(reader, "API key source (env/inline): ", "")
		if err != nil {
			return modelOptions{}, fmt.Errorf("%s: %w", command, err)
		}
		switch source {
		case "env":
			options.apiKeyEnv, err = a.promptLine(reader, "API key environment variable: ", "")
		case "inline":
			options.apiKey, err = a.promptLine(reader, "Inline API key: ", "")
		default:
			return modelOptions{}, fmt.Errorf("%s: API key source must be env or inline", command)
		}
		if err != nil {
			return modelOptions{}, fmt.Errorf("%s: %w", command, err)
		}
	}
	return options, nil
}

func (a App) promptLine(reader *bufio.Reader, prompt, defaultValue string) (string, error) {
	if _, err := io.WriteString(a.Stdout, prompt); err != nil {
		return "", errors.New("write prompt")
	}
	line, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", errors.New("read input")
	}
	if errors.Is(err, io.EOF) && line == "" {
		return "", errors.New("input ended before all fields were provided")
	}
	line = strings.TrimSuffix(line, "\n")
	line = strings.TrimSuffix(line, "\r")
	if line == "" {
		return defaultValue, nil
	}
	return line, nil
}

func parseModelOptions(name string, args []string, allowForce bool) (modelOptions, bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var options modelOptions
	var force bool
	fs.StringVar(&options.name, "name", "", "model alias")
	fs.StringVar(&options.baseURL, "base-url", "", "provider base URL")
	fs.StringVar(&options.endpoint, "endpoint", "", "provider endpoint")
	fs.StringVar(&options.upstreamModel, "upstream-model", "", "provider model")
	fs.StringVar(&options.apiKey, "api-key", "", "inline API key")
	fs.StringVar(&options.apiKeyEnv, "api-key-env", "", "API key environment variable")
	fs.Func("header", "provider header (repeatable)", func(value string) error {
		options.headers = append(options.headers, value)
		return nil
	})
	fs.BoolVar(&options.nonInteractive, "non-interactive", false, "disable prompts")
	if allowForce {
		fs.BoolVar(&force, "force", false, "replace existing configuration")
	}
	if err := fs.Parse(args); err != nil {
		return modelOptions{}, false, fmt.Errorf("%s: %w", name, err)
	}
	if fs.NArg() != 0 {
		return modelOptions{}, false, fmt.Errorf("%s: unexpected arguments", name)
	}
	return options, force, nil
}

func (o modelOptions) model() (config.Model, error) {
	headers, err := parseProviderHeaders(o.headers)
	if err != nil {
		return config.Model{}, err
	}
	model := config.Model{
		Name:          o.name,
		Protocol:      config.ProtocolOpenAI,
		BaseURL:       o.baseURL,
		Endpoint:      o.endpoint,
		APIKey:        o.apiKey,
		APIKeyEnv:     o.apiKeyEnv,
		Headers:       headers,
		UpstreamModel: o.upstreamModel,
	}
	cfg := config.Config{
		Version:      config.CurrentVersion,
		DefaultModel: model.Name,
		Models:       []config.Model{model},
	}
	if err := cfg.Validate(); err != nil {
		return config.Model{}, err
	}
	return model, nil
}

func parseProviderHeaders(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	headers := make(map[string]string, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, raw := range values {
		name, value, found := strings.Cut(raw, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !found || name == "" {
			return nil, errors.New("provider header must use name:value syntax")
		}
		canonicalName := textproto.CanonicalMIMEHeaderKey(name)
		lookupName := strings.ToLower(canonicalName)
		if _, duplicate := seen[lookupName]; duplicate {
			return nil, fmt.Errorf("provider header %q is duplicated", canonicalName)
		}
		seen[lookupName] = struct{}{}
		headers[canonicalName] = value
	}
	if err := config.ValidateProviderHeaders(headers); err != nil {
		return nil, err
	}
	return headers, nil
}
