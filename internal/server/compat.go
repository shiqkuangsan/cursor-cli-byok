package server

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"

	"github.com/shiqkuangsan/cursor-cli-byok/internal/config"
	"github.com/shiqkuangsan/cursor-cli-byok/internal/protocol"
)

const maxCompatibilityRequestBytes = 4 * 1024 * 1024

type ConfigLoader func() (config.Config, error)

func NewCompatibilityHandler(load ConfigLoader) http.Handler {
	return &compatibilityHandler{load: load}
}

type compatibilityHandler struct {
	load ConfigLoader
}

func (h *compatibilityHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	procedure, known := compatibilityProcedures[request.URL.Path]
	if !known {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		http.Error(writer, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(request.Body, maxCompatibilityRequestBytes+1))
	if err != nil {
		http.Error(writer, "read request", http.StatusBadRequest)
		return
	}
	if len(body) > maxCompatibilityRequestBytes {
		http.Error(writer, "request too large", http.StatusRequestEntityTooLarge)
		return
	}
	mediaType := request.Header.Get("Content-Type")
	if mediaType == "" {
		mediaType = "application/proto"
	} else {
		parsedMediaType, _, err := mime.ParseMediaType(mediaType)
		if err != nil {
			http.Error(writer, "unsupported content type", http.StatusUnsupportedMediaType)
			return
		}
		mediaType = parsedMediaType
	}
	if mediaType != "application/proto" && mediaType != "application/json" {
		http.Error(writer, "unsupported content type", http.StatusUnsupportedMediaType)
		return
	}

	response, err := h.response(procedure, mediaType)
	if err != nil {
		http.Error(writer, "compatibility response unavailable", http.StatusInternalServerError)
		return
	}
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Connect-Protocol-Version", "1")
	writer.Header().Set("Content-Type", mediaType)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(response)
}

func (h *compatibilityHandler) response(procedure compatibilityProcedure, mediaType string) ([]byte, error) {
	if mediaType == "application/json" {
		return h.jsonResponse(procedure)
	}
	switch procedure {
	case procedureAvailableModels, procedureUsableModels, procedureDefaultModel:
		cfg, err := h.loadConfig()
		if err != nil {
			return nil, err
		}
		names := modelNames(cfg)
		switch procedure {
		case procedureAvailableModels:
			return protocol.EncodeAvailableModels(names, cfg.DefaultModel)
		case procedureUsableModels:
			return protocol.EncodeUsableModels(names)
		default:
			return protocol.EncodeDefaultModel(cfg.DefaultModel)
		}
	case procedureGetMe:
		return protocol.EncodeLocalAccount(), nil
	case procedurePrivacy:
		return protocol.EncodeNoStoragePrivacyMode(), nil
	case procedureServerConfig:
		return protocol.EncodeServerConfig(), nil
	case procedureEmpty:
		return nil, nil
	default:
		return nil, errors.New("unknown compatibility procedure")
	}
}

func (h *compatibilityHandler) jsonResponse(procedure compatibilityProcedure) ([]byte, error) {
	switch procedure {
	case procedureAvailableModels, procedureUsableModels, procedureDefaultModel:
		cfg, err := h.loadConfig()
		if err != nil {
			return nil, err
		}
		names := modelNames(cfg)
		details := make([]map[string]any, len(names))
		available := make([]map[string]any, len(names))
		for index, name := range names {
			details[index] = map[string]any{
				"modelId":          name,
				"displayModelId":   name,
				"displayName":      name,
				"displayNameShort": name,
				"aliases":          []string{name},
			}
			available[index] = map[string]any{
				"name":              name,
				"defaultOn":         name == cfg.DefaultModel,
				"supportsAgent":     true,
				"clientDisplayName": name,
				"serverModelName":   name,
				"supportsPlanMode":  true,
				"isUserAdded":       true,
			}
		}
		var response any
		switch procedure {
		case procedureAvailableModels:
			response = map[string]any{"modelNames": names, "models": available}
		case procedureUsableModels:
			response = map[string]any{"models": details}
		default:
			for _, detail := range details {
				if detail["modelId"] == cfg.DefaultModel {
					response = map[string]any{"model": detail}
					break
				}
			}
		}
		return json.Marshal(response)
	case procedureGetMe:
		return json.Marshal(map[string]any{
			"authId": "cursor-cli-byok", "userId": 1, "email": "byok@localhost",
			"firstName": "Cursor", "lastName": "BYOK", "createdAt": "2026-01-01T00:00:00Z",
			"emailDomainType": "personal", "country": "local",
		})
	case procedurePrivacy:
		return json.Marshal(map[string]any{
			"privacyMode": "PRIVACY_MODE_NO_STORAGE", "hoursRemainingInGracePeriod": 0,
			"isEnforcedByTeam": false, "isNotMigratedToServerSourceOfTruth": false,
			"partnerDataShare": false, "hasAcknowledgedGracePeriodDisclaimer": true,
		})
	case procedureServerConfig:
		return json.Marshal(map[string]any{"configVersion": "cursor_cli_byok_v1", "cliSandboxDefaultEnabled": true})
	case procedureEmpty:
		return []byte("{}"), nil
	default:
		return nil, errors.New("unknown compatibility procedure")
	}
}

func (h *compatibilityHandler) loadConfig() (config.Config, error) {
	if h.load == nil {
		return config.Config{}, errors.New("config loader is required")
	}
	return h.load()
}

func modelNames(cfg config.Config) []string {
	names := make([]string, len(cfg.Models))
	for index, model := range cfg.Models {
		names[index] = model.Name
	}
	return names
}

type compatibilityProcedure uint8

const (
	procedureEmpty compatibilityProcedure = iota
	procedureAvailableModels
	procedureUsableModels
	procedureDefaultModel
	procedureGetMe
	procedurePrivacy
	procedureServerConfig
)

var compatibilityProcedures = map[string]compatibilityProcedure{
	"/aiserver.v1.AiService/AvailableModels":                               procedureAvailableModels,
	"/aiserver.v1.AiService/GetUsableModels":                               procedureUsableModels,
	"/aiserver.v1.AiService/GetDefaultModelForCli":                         procedureDefaultModel,
	"/aiserver.v1.DashboardService/GetMe":                                  procedureGetMe,
	"/aiserver.v1.DashboardService/GetUserPrivacyMode":                     procedurePrivacy,
	"/aiserver.v1.AiService/GetServerConfig":                               procedureServerConfig,
	"/aiserver.v1.ServerConfigService/GetServerConfig":                     procedureServerConfig,
	"/aiserver.v1.DashboardService/GetTeamAdminSettingsOrEmptyIfNotInTeam": procedureEmpty,
	"/aiserver.v1.DashboardService/ListMarketplaces":                       procedureEmpty,
	"/aiserver.v1.AnalyticsService/TrackEvents":                            procedureEmpty,
	"/aiserver.v1.AnalyticsService/BootstrapStatsig":                       procedureEmpty,
	"/aiserver.v1.AnalyticsService/GetFirstWindowStatsigDecision":          procedureEmpty,
	"/aiserver.v1.DashboardService/GetCurrentPeriodUsage":                  procedureEmpty,
	"/aiserver.v1.DashboardService/GetUsageLimitStatusAndActiveGrants":     procedureEmpty,
	"/aiserver.v1.DashboardService/IsOnNewPricing":                         procedureEmpty,
	"/aiserver.v1.DashboardService/GetManagedSkills":                       procedureEmpty,
	"/aiserver.v1.DashboardService/GetEffectiveUserPlugins":                procedureEmpty,
	"/aiserver.v1.AnalyticsService/SubmitLogs":                             procedureEmpty,
	"/aiserver.v1.DashboardService/RegisterMarketplaceAndPlugins":          procedureEmpty,
	"/aiserver.v1.DashboardService/GetCliDownloadUrl":                      procedureEmpty,
	"/aiserver.v1.DashboardService/GetGlobalCommands":                      procedureEmpty,
	"/v1/traces": procedureEmpty,
}
