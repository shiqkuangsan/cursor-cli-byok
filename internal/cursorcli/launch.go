package cursorcli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type LaunchOptions struct {
	CursorPath            string
	EndpointURL           string
	Model                 string
	CACertPath            string
	AuthToken             string
	ParentEnv             []string
	ProviderSecretEnvKeys []string
	UserArgs              []string
}

type LaunchSpec struct {
	Path string
	Args []string
	Env  []string
}

func SplitModelArgument(defaultModel string, args []string) (string, []string, error) {
	selectedModel := defaultModel
	selectionFound := false
	forwarded := make([]string, 0, len(args))
	afterSeparator := false
	for index := 0; index < len(args); index++ {
		argument := args[index]
		if afterSeparator {
			forwarded = append(forwarded, argument)
			continue
		}
		if argument == "--" {
			afterSeparator = true
			forwarded = append(forwarded, argument)
			continue
		}

		var model string
		matched := false
		switch {
		case argument == "--model":
			if index+1 >= len(args) {
				return "", nil, errors.New("split Cursor arguments: --model requires a value")
			}
			index++
			model = args[index]
			matched = true
		case strings.HasPrefix(argument, "--model="):
			model = strings.TrimPrefix(argument, "--model=")
			matched = true
		}
		if !matched {
			forwarded = append(forwarded, argument)
			continue
		}
		if selectionFound {
			return "", nil, errors.New("split Cursor arguments: --model may be specified only once")
		}
		if model == "" || hasControl(model) {
			return "", nil, errors.New("split Cursor arguments: --model value is invalid")
		}
		selectedModel = model
		selectionFound = true
	}
	if selectedModel == "" || hasControl(selectedModel) {
		return "", nil, errors.New("split Cursor arguments: default model is required")
	}
	return selectedModel, forwarded, nil
}

func Run(
	ctx context.Context,
	spec LaunchSpec,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	signals <-chan os.Signal,
) (int, error) {
	if ctx == nil {
		return -1, errors.New("run cursor-agent: context is required")
	}
	if !filepath.IsAbs(spec.Path) {
		return -1, errors.New("run cursor-agent: executable path must be absolute")
	}
	command := exec.CommandContext(ctx, spec.Path, spec.Args...)
	command.Env = spec.Env
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return -1, fmt.Errorf("run cursor-agent: start: %w", err)
	}

	waitResult := make(chan error, 1)
	go func() {
		waitResult <- command.Wait()
	}()

	for {
		select {
		case err := <-waitResult:
			if err == nil {
				return 0, nil
			}
			if ctx.Err() != nil {
				return -1, ctx.Err()
			}
			var exitError *exec.ExitError
			if errors.As(err, &exitError) {
				return normalizedExitCode(exitError), nil
			}
			return -1, fmt.Errorf("run cursor-agent: wait: %w", err)
		case signal, open := <-signals:
			if !open {
				signals = nil
				continue
			}
			if signal == nil {
				continue
			}
			if err := command.Process.Signal(signal); err != nil && !errors.Is(err, os.ErrProcessDone) {
				_ = command.Process.Kill()
				<-waitResult
				return -1, fmt.Errorf("run cursor-agent: forward signal: %w", err)
			}
		}
	}
}

func BuildLaunchSpec(options LaunchOptions) (LaunchSpec, error) {
	if !filepath.IsAbs(options.CursorPath) {
		return LaunchSpec{}, errors.New("build Cursor launch: cursor-agent path must be absolute")
	}
	if err := validateLocalEndpoint(options.EndpointURL); err != nil {
		return LaunchSpec{}, err
	}
	if options.Model == "" || hasSurroundingWhitespace(options.Model) || hasControl(options.Model) {
		return LaunchSpec{}, errors.New("build Cursor launch: model must be a nonempty safe alias")
	}
	if !filepath.IsAbs(options.CACertPath) {
		return LaunchSpec{}, errors.New("build Cursor launch: CA certificate path must be absolute")
	}
	if options.AuthToken == "" || hasControl(options.AuthToken) {
		return LaunchSpec{}, errors.New("build Cursor launch: local auth token is required")
	}
	if err := rejectReservedArguments(options.UserArgs); err != nil {
		return LaunchSpec{}, err
	}

	args := make([]string, 0, 4+len(options.UserArgs))
	args = append(args, "-e", options.EndpointURL, "--model", options.Model)
	args = append(args, options.UserArgs...)

	environment := parseEnvironment(options.ParentEnv)
	for _, key := range options.ProviderSecretEnvKeys {
		if !validEnvironmentKey(key) {
			return LaunchSpec{}, errors.New("build Cursor launch: provider secret environment name is invalid")
		}
		delete(environment, key)
	}
	for _, key := range []string{
		"CURSOR_API_KEY",
		"CURSOR_AGENT_CLI_AUTHLESS_MODE",
		"CURSOR_AGENT_CLI_LOCAL_MODE",
		"CURSOR_ENABLE_AUTHLESS",
		"CURSOR_ENABLE_BEDROCK",
		"CURSOR_ENABLE_LOCAL_BEDROCK",
		"CURSOR_LOCAL_AGENT_ALLOW_CURSOR_HOST",
		"CURSOR_LOCAL_AGENT_API_KEY",
		"CURSOR_LOCAL_AGENT_API_KEY_HELPER",
		"CURSOR_LOCAL_AGENT_BASE_URL",
	} {
		delete(environment, key)
	}
	environment["CURSOR_AUTH_TOKEN"] = options.AuthToken
	environment["CURSOR_API_ENDPOINT"] = options.EndpointURL
	environment["CURSOR_API_BASE_URL"] = options.EndpointURL
	environment["AGENT_CLI_CREDENTIAL_STORE"] = "file"
	environment["NODE_EXTRA_CA_CERTS"] = options.CACertPath
	environment["NO_OPEN_BROWSER"] = "1"

	return LaunchSpec{
		Path: options.CursorPath,
		Args: args,
		Env:  formatEnvironment(environment),
	}, nil
}

func validEnvironmentKey(value string) bool {
	if value == "" {
		return false
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if character == '_' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' {
			continue
		}
		if index > 0 && character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func validateLocalEndpoint(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("build Cursor launch: endpoint must be an absolute HTTPS URL")
	}
	if parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return errors.New("build Cursor launch: endpoint must be a bare loopback HTTPS origin")
	}
	host := parsed.Hostname()
	ip := net.ParseIP(host)
	if host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return errors.New("build Cursor launch: endpoint host must be loopback")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("build Cursor launch: endpoint must include a valid port")
	}
	return nil
}

func rejectReservedArguments(args []string) error {
	afterSeparator := false
	for _, argument := range args {
		if argument == "--" {
			afterSeparator = true
			continue
		}
		if afterSeparator {
			continue
		}
		if isReservedArgument(argument) {
			return errors.New("build Cursor launch: endpoint, model, and Cursor API key arguments are reserved")
		}
	}
	return nil
}

func isReservedArgument(argument string) bool {
	for _, name := range []string{"-e", "--endpoint", "--api-key", "--model"} {
		if argument == name || strings.HasPrefix(argument, name+"=") {
			return true
		}
	}
	return strings.HasPrefix(argument, "-e") && !strings.HasPrefix(argument, "--")
}

func parseEnvironment(entries []string) map[string]string {
	environment := make(map[string]string, len(entries))
	for _, entry := range entries {
		key, value, found := strings.Cut(entry, "=")
		if !found || key == "" {
			continue
		}
		environment[key] = value
	}
	return environment
}

func formatEnvironment(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	entries := make([]string, 0, len(keys))
	for _, key := range keys {
		entries = append(entries, fmt.Sprintf("%s=%s", key, environment[key]))
	}
	return entries
}

func hasSurroundingWhitespace(value string) bool {
	return value != strings.TrimSpace(value)
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
