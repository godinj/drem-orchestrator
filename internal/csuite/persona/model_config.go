package persona

import (
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/godinj/drem-orchestrator/internal/csuite"
)

type diskConfig struct {
	Model string `json:"model,omitempty"`
}

func (p *Poller) resolveModel() string {
	if model, ok := p.configModel(); ok {
		return model
	}
	return firstNonEmpty(os.Getenv("DREM_OPENCODE_MODEL"), os.Getenv("DREM_CODEX_MODEL"), csuite.DefaultPersonaModel)
}

func (p *Poller) configModel() (string, bool) {
	path := filepath.Join(filepath.Dir(p.cfg.StateFile), "config.json")
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", false
	}
	if err != nil {
		p.cfg.Logger.Warn("read persona config",
			p.personaLabel,
			slog.String("path", path),
			slog.Any("err", err))
		return "", false
	}

	var cfg diskConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		p.cfg.Logger.Warn("parse persona config",
			p.personaLabel,
			slog.String("path", path),
			slog.Any("err", err))
		return "", false
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return "", false
	}
	model, err := csuite.NormalizePersonaModel(cfg.Model)
	if err != nil {
		p.cfg.Logger.Warn("invalid persona config model",
			p.personaLabel,
			slog.String("path", path),
			slog.String("model", cfg.Model),
			slog.Any("err", err))
		return "", false
	}
	return model, true
}
