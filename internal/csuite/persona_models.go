package csuite

import (
	"errors"
	"fmt"
	"strings"
)

const (
	PersonaModelGPT55     = "openai/gpt-5.5"
	PersonaModelGPT54Mini = "openai/gpt-5.4-mini"
	DefaultPersonaModel   = PersonaModelGPT55
)

var (
	ErrInvalidPersonaModel = errors.New("invalid persona model")

	KnownPersonas        = []string{"kyle", "mike", "alex", "seth"}
	AllowedPersonaModels = []string{PersonaModelGPT55, PersonaModelGPT54Mini}
)

func IsKnownPersona(persona string) bool {
	for _, p := range KnownPersonas {
		if persona == p {
			return true
		}
	}
	return false
}

func ValidatePersona(persona string) error {
	if IsKnownPersona(persona) {
		return nil
	}
	return fmt.Errorf("%w: %q", ErrUnknownPersona, persona)
}

func NormalizePersonaModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	for _, allowed := range AllowedPersonaModels {
		if model == allowed {
			return model, nil
		}
	}
	return "", fmt.Errorf("%w: %q", ErrInvalidPersonaModel, model)
}

func IsAllowedPersonaModel(model string) bool {
	_, err := NormalizePersonaModel(model)
	return err == nil
}
