package benchv2

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const ServerUsageSourceProxy = "trusted_usage_proxy"

type UsageSession struct {
	CorrelationID string
	BaseURL       string
	APIKey        string
}

type ServerUsageAttestor interface {
	StartServerUsage(context.Context, TrialRequest) (UsageSession, error)
	AttestServerUsage(context.Context, UsageSession) (ServerUsage, error)
}

type UsageProxyServerConfig struct {
	UpstreamChatCompletions string
	UpstreamAPIKey          string
	PublicBaseURL           string
	AdminToken              string
	SourceState             string
	Image                   string
	ConfigSHA256            string
	HTTPClient              *http.Client
}

type UsageProxyAttestation struct {
	SourceState  string `json:"source_state"`
	Image        string `json:"image"`
	ConfigSHA256 string `json:"config_sha256"`
}

type usageProxyServer struct {
	config UsageProxyServerConfig
	client *http.Client
	mu     sync.Mutex
	trials map[string]*usageTrial
	keys   map[[sha256.Size]byte]string
}

type usageTrial struct {
	ID                  string
	KeyDigest           [sha256.Size]byte
	Policy              UsageProxyTrialPolicy
	Frozen              bool
	Consumed            bool
	InFlight            int
	RequestsTotal       int
	RequestsMeasured    int
	PromptTokens        int
	CompletionTokens    int
	MeasurementFailures int
}

// UsageProxyTrialPolicy is the benchmark-owned inference contract applied by
// the trusted proxy to every request in a trial. Harness request defaults are
// deliberately ignored so all adapters exercise the same model policy.
type UsageProxyTrialPolicy struct {
	ModelID          string  `json:"model_id"`
	Seed             int64   `json:"seed"`
	Temperature      float64 `json:"temperature"`
	TopP             float64 `json:"top_p"`
	TopK             int     `json:"top_k"`
	ContextWindow    int     `json:"context_window"`
	MaxOutputTokens  int     `json:"max_output_tokens"`
	PreserveThinking bool    `json:"preserve_thinking"`
}

type proxyUsage struct {
	PromptTokens     *int `json:"prompt_tokens"`
	CompletionTokens *int `json:"completion_tokens"`
}

type usageProxyAdminError struct {
	Status int
	Body   string
}

func (err *usageProxyAdminError) Error() string {
	return fmt.Sprintf("usage proxy admin returned HTTP %d: %s", err.Status, err.Body)
}

func NewUsageProxyHandler(config UsageProxyServerConfig) (http.Handler, error) {
	for name, value := range map[string]string{
		"upstream chat-completions URL": config.UpstreamChatCompletions,
		"public base URL":               config.PublicBaseURL,
		"admin token":                   config.AdminToken,
		"source state":                  config.SourceState,
		"image":                         config.Image,
		"config SHA-256":                config.ConfigSHA256,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s is required", name)
		}
	}
	upstreamURL, err := parseAbsoluteURL(config.UpstreamChatCompletions)
	if err != nil || strings.TrimRight(upstreamURL.Path, "/") != "/v1/chat/completions" {
		return nil, fmt.Errorf("upstream must be an absolute /v1/chat/completions URL")
	}
	publicURL, err := parseAbsoluteURL(config.PublicBaseURL)
	if err != nil || strings.TrimRight(publicURL.Path, "/") != "/v1" {
		return nil, fmt.Errorf("public base must be an absolute /v1 URL")
	}
	if !pinnedOCIImage.MatchString(config.Image) || !validSHA256Text(config.ConfigSHA256) {
		return nil, fmt.Errorf("usage proxy image and config SHA-256 must be content-addressed")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Minute}
	}
	return &usageProxyServer{
		config: config, client: client, trials: map[string]*usageTrial{}, keys: map[[sha256.Size]byte]string{},
	}, nil
}

func (server *usageProxyServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/admin/v1/attestation":
		server.liveAttestation(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/admin/v1/trials":
		server.startTrial(writer, request)
	case request.Method == http.MethodPost && strings.HasPrefix(request.URL.Path, "/admin/v1/trials/") && strings.HasSuffix(request.URL.Path, "/consume"):
		server.consumeTrial(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/v1/chat/completions":
		server.chatCompletions(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (server *usageProxyServer) liveAttestation(writer http.ResponseWriter, request *http.Request) {
	if !server.adminAuthorized(request) {
		writeProxyError(writer, http.StatusUnauthorized, "admin authentication failed")
		return
	}
	writeProxyJSON(writer, http.StatusOK, UsageProxyAttestation{
		SourceState:  server.config.SourceState,
		Image:        server.config.Image,
		ConfigSHA256: server.config.ConfigSHA256,
	})
}

func (server *usageProxyServer) startTrial(writer http.ResponseWriter, request *http.Request) {
	if !server.adminAuthorized(request) {
		writeProxyError(writer, http.StatusUnauthorized, "admin authentication failed")
		return
	}
	var policy UsageProxyTrialPolicy
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		writeProxyError(writer, http.StatusBadRequest, "invalid trial policy")
		return
	}
	if err := validateUsageProxyTrialPolicy(policy); err != nil {
		writeProxyError(writer, http.StatusBadRequest, err.Error())
		return
	}
	id, err := randomCredential(24)
	if err != nil {
		writeProxyError(writer, http.StatusInternalServerError, "credential generation failed")
		return
	}
	key, err := randomCredential(32)
	if err != nil {
		writeProxyError(writer, http.StatusInternalServerError, "credential generation failed")
		return
	}
	digest := sha256.Sum256([]byte(key))
	trial := &usageTrial{ID: id, KeyDigest: digest, Policy: policy}
	server.mu.Lock()
	server.trials[id] = trial
	server.keys[digest] = id
	server.mu.Unlock()
	writeProxyJSON(writer, http.StatusCreated, map[string]string{
		"correlation_id": id, "base_url": strings.TrimRight(server.config.PublicBaseURL, "/"), "api_key": key,
	})
}

func (server *usageProxyServer) consumeTrial(writer http.ResponseWriter, request *http.Request) {
	if !server.adminAuthorized(request) {
		writeProxyError(writer, http.StatusUnauthorized, "admin authentication failed")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/admin/v1/trials/"), "/consume")
	if id == "" || strings.Contains(id, "/") {
		http.NotFound(writer, request)
		return
	}
	server.mu.Lock()
	trial := server.trials[id]
	if trial == nil {
		server.mu.Unlock()
		http.NotFound(writer, request)
		return
	}
	if trial.Consumed {
		server.mu.Unlock()
		writeProxyError(writer, http.StatusConflict, "usage ledger was already consumed")
		return
	}
	if trial.InFlight != 0 {
		trial.Frozen = true
		delete(server.keys, trial.KeyDigest)
		server.mu.Unlock()
		writeProxyError(writer, http.StatusConflict, "usage ledger still has in-flight requests")
		return
	}
	trial.Frozen = true
	trial.Consumed = true
	delete(server.keys, trial.KeyDigest)
	usage := ServerUsage{
		Source: ServerUsageSourceProxy, CorrelationID: trial.ID,
		RequestsMeasured: trial.RequestsMeasured, RequestsTotal: trial.RequestsTotal,
		PromptTokens: trial.PromptTokens, CompletionTokens: trial.CompletionTokens,
	}
	usage.Complete = trial.RequestsTotal > 0 && trial.RequestsMeasured == trial.RequestsTotal && trial.MeasurementFailures == 0
	server.mu.Unlock()
	if !usage.Complete {
		writeProxyError(writer, http.StatusUnprocessableEntity, "usage ledger is incomplete")
		return
	}
	writeProxyJSON(writer, http.StatusOK, usage)
}

func (server *usageProxyServer) chatCompletions(writer http.ResponseWriter, request *http.Request) {
	trial, ok := server.beginRequest(request)
	if !ok {
		writeProxyError(writer, http.StatusUnauthorized, "trial authentication failed")
		return
	}
	usage, forwardErr := server.forwardChatCompletions(writer, request, trial.Policy)
	server.finishRequest(trial, usage, forwardErr)
}

func (server *usageProxyServer) beginRequest(request *http.Request) (*usageTrial, bool) {
	key, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok {
		return nil, false
	}
	digest := sha256.Sum256([]byte(key))
	server.mu.Lock()
	defer server.mu.Unlock()
	id := server.keys[digest]
	trial := server.trials[id]
	if trial == nil || trial.Frozen || trial.Consumed {
		return nil, false
	}
	trial.RequestsTotal++
	trial.InFlight++
	return trial, true
}

func (server *usageProxyServer) finishRequest(trial *usageTrial, usage *proxyUsage, forwardErr error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	trial.InFlight--
	if forwardErr != nil || usage == nil || usage.PromptTokens == nil || usage.CompletionTokens == nil || *usage.PromptTokens < 0 || *usage.CompletionTokens < 0 {
		trial.MeasurementFailures++
		return
	}
	trial.RequestsMeasured++
	trial.PromptTokens += *usage.PromptTokens
	trial.CompletionTokens += *usage.CompletionTokens
}

func (server *usageProxyServer) forwardChatCompletions(writer http.ResponseWriter, request *http.Request, policy UsageProxyTrialPolicy) (*proxyUsage, error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, 32<<20))
	if err != nil {
		writeProxyError(writer, http.StatusBadRequest, "read request body")
		return nil, err
	}
	var payload map[string]any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		writeProxyError(writer, http.StatusBadRequest, "invalid OpenAI request")
		return nil, err
	}
	applyUsageProxyTrialPolicy(payload, policy)
	stream, _ := payload["stream"].(bool)
	if stream {
		options, _ := payload["stream_options"].(map[string]any)
		if options == nil {
			options = map[string]any{}
		}
		options["include_usage"] = true
		payload["stream_options"] = options
	}
	forwardBody, err := json.Marshal(payload)
	if err != nil {
		writeProxyError(writer, http.StatusBadRequest, "invalid OpenAI request")
		return nil, err
	}
	// A harness may stop reading as soon as it reaches a tool/turn/wall budget.
	// Keep draining the already-authorized upstream request so the final
	// server-reported usage frame is still recorded. The proxy HTTP client's
	// timeout remains the hard upper bound.
	upstreamRequest, err := http.NewRequestWithContext(context.WithoutCancel(request.Context()), http.MethodPost, server.config.UpstreamChatCompletions, bytes.NewReader(forwardBody))
	if err != nil {
		writeProxyError(writer, http.StatusBadGateway, "upstream request failed")
		return nil, err
	}
	upstreamRequest.Header.Set("Content-Type", "application/json")
	if server.config.UpstreamAPIKey != "" {
		upstreamRequest.Header.Set("Authorization", "Bearer "+server.config.UpstreamAPIKey)
	}
	response, err := server.client.Do(upstreamRequest)
	if err != nil {
		writeProxyError(writer, http.StatusBadGateway, "upstream request failed")
		return nil, err
	}
	defer response.Body.Close()
	if contentType := response.Header.Get("Content-Type"); contentType != "" {
		writer.Header().Set("Content-Type", contentType)
	}
	writer.WriteHeader(response.StatusCode)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(writer, response.Body)
		return nil, fmt.Errorf("upstream returned HTTP %d", response.StatusCode)
	}
	if stream {
		return proxyStreamResponse(writer, response.Body)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 64<<20))
	if err != nil {
		return nil, err
	}
	_, _ = writer.Write(responseBody)
	usage, present, err := parseProxyUsage(responseBody)
	if err != nil || !present {
		return nil, errors.New("non-streaming response omitted valid usage")
	}
	return usage, nil
}

func validateUsageProxyTrialPolicy(policy UsageProxyTrialPolicy) error {
	if strings.TrimSpace(policy.ModelID) == "" || policy.ContextWindow <= 0 || policy.MaxOutputTokens <= 0 ||
		policy.Temperature < 0 || policy.TopP <= 0 || policy.TopP > 1 || policy.TopK < 0 {
		return errors.New("trial inference policy is invalid")
	}
	return nil
}

func applyUsageProxyTrialPolicy(payload map[string]any, policy UsageProxyTrialPolicy) {
	payload["model"] = policy.ModelID
	payload["seed"] = policy.Seed
	payload["temperature"] = policy.Temperature
	payload["top_p"] = policy.TopP
	payload["top_k"] = policy.TopK
	payload["max_tokens"] = policy.MaxOutputTokens
	kwargs, _ := payload["chat_template_kwargs"].(map[string]any)
	if kwargs == nil {
		kwargs = map[string]any{}
	}
	kwargs["preserve_thinking"] = policy.PreserveThinking
	payload["chat_template_kwargs"] = kwargs
}

func proxyStreamResponse(writer http.ResponseWriter, body io.Reader) (*proxyUsage, error) {
	reader := bufio.NewReader(body)
	var measured *proxyUsage
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			_, _ = writer.Write(line)
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
			trimmed := strings.TrimSpace(string(line))
			if strings.HasPrefix(trimmed, "data:") {
				data := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if data != "" && data != "[DONE]" {
					usage, present, parseErr := parseProxyUsage([]byte(data))
					if parseErr != nil {
						return nil, parseErr
					}
					if present {
						if measured != nil {
							return nil, errors.New("streaming response contained duplicate usage")
						}
						measured = usage
					}
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return nil, readErr
		}
	}
	if measured == nil {
		return nil, errors.New("streaming response omitted usage")
	}
	return measured, nil
}

func parseProxyUsage(raw []byte) (*proxyUsage, bool, error) {
	var envelope struct {
		Usage json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, false, err
	}
	if len(envelope.Usage) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Usage), []byte("null")) {
		return nil, false, nil
	}
	var usage proxyUsage
	if err := json.Unmarshal(envelope.Usage, &usage); err != nil {
		return nil, false, err
	}
	if usage.PromptTokens == nil || usage.CompletionTokens == nil {
		return nil, false, errors.New("usage fields are incomplete")
	}
	return &usage, true, nil
}

func (server *usageProxyServer) adminAuthorized(request *http.Request) bool {
	token, ok := bearerToken(request.Header.Get("Authorization"))
	if !ok || len(token) != len(server.config.AdminToken) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(token), []byte(server.config.AdminToken)) == 1
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) || len(header) == len(prefix) {
		return "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(header, prefix)), true
}

func randomCredential(size int) (string, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func writeProxyError(writer http.ResponseWriter, status int, message string) {
	writeProxyJSON(writer, status, map[string]string{"error": message})
}

func writeProxyJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

type UsageProxyClientConfig struct {
	AdminURL            string
	PublicBaseURL       string
	AdminToken          string
	ExpectedAttestation UsageProxyAttestation
	HTTPClient          *http.Client
}

type UsageProxyClient struct {
	config UsageProxyClientConfig
	client *http.Client
}

func ReadPrivateTokenFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode()&0o077 != 0 {
		return "", fmt.Errorf("token file must be regular and readable only by its owner")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(raw))
	if token == "" {
		return "", fmt.Errorf("token file is empty")
	}
	return token, nil
}

func NewUsageProxyClient(config UsageProxyClientConfig) (*UsageProxyClient, error) {
	for name, value := range map[string]string{"admin URL": config.AdminURL, "public base URL": config.PublicBaseURL, "admin token": config.AdminToken} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("usage proxy %s is required", name)
		}
	}
	adminURL, err := parseAbsoluteURL(config.AdminURL)
	if err != nil || (adminURL.Path != "" && adminURL.Path != "/") {
		return nil, fmt.Errorf("usage proxy admin URL must not contain a path")
	}
	publicURL, err := parseAbsoluteURL(config.PublicBaseURL)
	if err != nil || strings.TrimRight(publicURL.Path, "/") != "/v1" {
		return nil, fmt.Errorf("usage proxy public base must be an absolute /v1 URL")
	}
	if strings.TrimSpace(config.ExpectedAttestation.SourceState) == "" ||
		!pinnedOCIImage.MatchString(config.ExpectedAttestation.Image) ||
		!validSHA256Text(config.ExpectedAttestation.ConfigSHA256) {
		return nil, fmt.Errorf("expected usage proxy attestation is incomplete")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 15 * time.Second}
	}
	return &UsageProxyClient{config: config, client: client}, nil
}

func parseAbsoluteURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid absolute URL")
	}
	return parsed, nil
}

func (client *UsageProxyClient) StartServerUsage(ctx context.Context, request TrialRequest) (UsageSession, error) {
	if err := client.VerifyLiveAttestation(ctx); err != nil {
		return UsageSession{}, err
	}
	var response struct {
		CorrelationID string `json:"correlation_id"`
		BaseURL       string `json:"base_url"`
		APIKey        string `json:"api_key"`
	}
	policy := UsageProxyTrialPolicy{
		ModelID: request.Runtime.ModelID, Seed: request.Seed, Temperature: request.Temperature,
		TopP: request.TopP, TopK: request.TopK, ContextWindow: request.ContextWindow,
		MaxOutputTokens: request.Task.Budget.MaxOutputTokens, PreserveThinking: request.PreserveThinking,
	}
	if err := client.adminJSON(ctx, http.MethodPost, "/admin/v1/trials", policy, &response); err != nil {
		return UsageSession{}, err
	}
	if response.CorrelationID == "" || response.APIKey == "" || strings.TrimRight(response.BaseURL, "/") != strings.TrimRight(client.config.PublicBaseURL, "/") {
		return UsageSession{}, errors.New("usage proxy returned an invalid trial credential")
	}
	return UsageSession{CorrelationID: response.CorrelationID, BaseURL: response.BaseURL, APIKey: response.APIKey}, nil
}

func (client *UsageProxyClient) VerifyLiveAttestation(ctx context.Context) error {
	var live UsageProxyAttestation
	if err := client.adminJSON(ctx, http.MethodGet, "/admin/v1/attestation", nil, &live); err != nil {
		return fmt.Errorf("read live usage proxy attestation: %w", err)
	}
	expected := client.config.ExpectedAttestation
	if live != expected {
		return fmt.Errorf("live usage proxy attestation does not match the matrix")
	}
	return nil
}

func (client *UsageProxyClient) AttestServerUsage(ctx context.Context, session UsageSession) (ServerUsage, error) {
	path := "/admin/v1/trials/" + url.PathEscape(session.CorrelationID) + "/consume"
	for {
		var usage ServerUsage
		err := client.adminJSON(ctx, http.MethodPost, path, nil, &usage)
		if err == nil {
			if usage.Source != ServerUsageSourceProxy || usage.CorrelationID != session.CorrelationID || !usage.Complete ||
				usage.RequestsMeasured <= 0 || usage.RequestsMeasured != usage.RequestsTotal || usage.PromptTokens < 0 || usage.CompletionTokens < 0 {
				return ServerUsage{}, errors.New("trusted usage proxy returned incomplete usage")
			}
			return usage, nil
		}
		var adminErr *usageProxyAdminError
		if !errors.As(err, &adminErr) || adminErr.Status != http.StatusConflict || !strings.Contains(adminErr.Body, "in-flight") {
			return ServerUsage{}, err
		}
		select {
		case <-ctx.Done():
			return ServerUsage{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func (client *UsageProxyClient) adminJSON(ctx context.Context, method, path string, body, target any) error {
	var requestBody io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(client.config.AdminURL, "/")+path, requestBody)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+client.config.AdminToken)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return &usageProxyAdminError{Status: response.StatusCode, Body: strings.TrimSpace(string(raw))}
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
