package command

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/cursorcli"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/daemon"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/paths"
)

const doctorCheckTimeout = 3 * time.Second

func (a App) runDoctor(args []string) int {
	if len(args) != 0 {
		return usageExitCode
	}

	runtimePaths, pathError := paths.Resolve(a.Getenv)
	allHealthy := pathError == nil
	var model config.ResolvedModel
	modelReady := false
	if pathError != nil {
		if !a.writeDoctorLine("config: fail (paths unavailable)") {
			return errorExitCode
		}
	} else {
		cfg, err := config.NewStore(runtimePaths.ConfigFile).Load()
		if err != nil {
			allHealthy = false
			if !a.writeDoctorLine("config: fail (configuration is unavailable)") {
				return errorExitCode
			}
		} else {
			model, err = cfg.ResolveModel("", a.Getenv)
			if err != nil || !validDoctorAPIKey(model.APIKey) {
				allHealthy = false
				if !a.writeDoctorLine("config: fail (default model API key is unavailable)") {
					return errorExitCode
				}
			} else {
				modelReady = true
				if !a.writeDoctorLine(fmt.Sprintf("config: ok (%s)", model.Name)) {
					return errorExitCode
				}
			}
		}
	}

	cursorReady := false
	cursorPath, err := cursorcli.Find(a.Getenv)
	if err == nil {
		checkContext, cancel := context.WithTimeout(a.Context, doctorCheckTimeout)
		version, versionError := cursorcli.ReadVersion(checkContext, cursorPath)
		cancel()
		if versionError == nil {
			cursorReady = true
			line := fmt.Sprintf("cursor-agent: ok (%s)", version)
			if !cursorcli.IsVerifiedVersion(version) {
				line = fmt.Sprintf("cursor-agent: warn (untested %s)", version)
			}
			if !a.writeDoctorLine(line) {
				return errorExitCode
			}
		}
	}
	if !cursorReady {
		allHealthy = false
		if !a.writeDoctorLine("cursor-agent: fail (official CLI is unavailable)") {
			return errorExitCode
		}
	}

	if modelReady {
		status, probeError := probeProvider(a.Context, model)
		if probeError != nil || !doctorProviderStatusOK(status) {
			allHealthy = false
			message := "provider: fail (request failed)"
			if probeError == nil {
				message = fmt.Sprintf("provider: fail (HTTP %d)", status)
			}
			if !a.writeDoctorLine(message) {
				return errorExitCode
			}
		} else if !a.writeDoctorLine(fmt.Sprintf("provider: ok (HTTP %d)", status)) {
			return errorExitCode
		}
	} else if !a.writeDoctorLine("provider: skipped (configuration unavailable)") {
		return errorExitCode
	}

	if pathError != nil {
		allHealthy = false
		if !a.writeDoctorLine("daemon: fail (state path unavailable)") {
			return errorExitCode
		}
	} else {
		state, stateError := daemon.NewStateStore(daemon.StatePath(runtimePaths)).Load()
		switch {
		case errors.Is(stateError, os.ErrNotExist):
			if !a.writeDoctorLine("daemon: stopped") {
				return errorExitCode
			}
		case stateError != nil || !a.ProcessAlive(state.PID):
			allHealthy = false
			if !a.writeDoctorLine("daemon: fail (stale state)") {
				return errorExitCode
			}
		case a.DaemonProbe.Check(a.Context, state) != nil:
			allHealthy = false
			if !a.writeDoctorLine("daemon: fail (health check failed)") {
				return errorExitCode
			}
		default:
			if !a.writeDoctorLine("daemon: ok (running)") {
				return errorExitCode
			}
		}
	}

	if allHealthy {
		if !a.writeDoctorLine("doctor: ok") {
			return errorExitCode
		}
		return 0
	}
	if !a.writeDoctorLine("doctor: failed") {
		return errorExitCode
	}
	return errorExitCode
}

func probeProvider(parent context.Context, model config.ResolvedModel) (int, error) {
	if err := config.ValidateProviderHeaders(model.Headers); err != nil {
		return 0, errors.New("validate provider probe headers")
	}
	endpointURL, err := url.JoinPath(model.BaseURL, strings.TrimPrefix(model.Endpoint, "/"))
	if err != nil {
		return 0, errors.New("build provider probe URL")
	}
	checkContext, cancel := context.WithTimeout(parent, doctorCheckTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(checkContext, http.MethodHead, endpointURL, nil)
	if err != nil {
		return 0, errors.New("create provider probe")
	}
	applyProviderHeaders(request, model.Headers)
	request.Header.Set("Authorization", "Bearer "+model.APIKey)
	client := &http.Client{
		Timeout: doctorCheckTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("provider redirects are disabled")
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return 0, errors.New("provider probe failed")
	}
	status := response.StatusCode
	_ = response.Body.Close()
	if status != http.StatusNotFound {
		return status, nil
	}

	fallback, err := http.NewRequestWithContext(checkContext, http.MethodPost, endpointURL, strings.NewReader("{}"))
	if err != nil {
		return 0, errors.New("create provider fallback probe")
	}
	applyProviderHeaders(fallback, model.Headers)
	fallback.Header.Set("Authorization", "Bearer "+model.APIKey)
	fallback.Header.Set("Content-Type", "application/json")
	response, err = client.Do(fallback)
	if err != nil {
		return 0, errors.New("provider fallback probe failed")
	}
	defer response.Body.Close()
	return response.StatusCode, nil
}

func applyProviderHeaders(request *http.Request, headers map[string]string) {
	for name, value := range headers {
		request.Header.Set(name, value)
	}
}

func doctorProviderStatusOK(status int) bool {
	return status == http.StatusBadRequest ||
		status == http.StatusMethodNotAllowed ||
		status == http.StatusUnprocessableEntity ||
		status >= 200 && status < 300
}

func validDoctorAPIKey(value string) bool {
	return value != "" && value == strings.TrimSpace(value) && strings.IndexFunc(value, unicode.IsControl) < 0
}

func (a App) writeDoctorLine(line string) bool {
	_, err := fmt.Fprintln(a.Stdout, line)
	return err == nil
}
