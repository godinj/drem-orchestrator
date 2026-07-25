package benchv2

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

var audioProcessCall = regexp.MustCompile(`\bregisterAudioProcessActions\s*\(\s*\)\s*;`)

// verifyAudioProcessRegistration closes the exact production seam missed by
// the otherwise verified 2ff61e8 artifact. The fixture pins the declaration,
// definition, action implementation, keymap, and integration test at that
// commit, so the candidate may change only the registerAllActions call site.
func verifyAudioProcessRegistration(workDir string) VerifyOutcome {
	coordinator, err := readFixtureFile(workDir, "src/ui/ActionCoordinator.cpp")
	if err != nil {
		return registrationFailure(err)
	}
	header, err := readFixtureFile(workDir, "src/ui/ActionCoordinator.h")
	if err != nil {
		return registrationFailure(err)
	}
	actions, err := readFixtureFile(workDir, "src/ui/ActionAudioProcesses.cpp")
	if err != nil {
		return registrationFailure(err)
	}

	cleanCoordinator := stripCPPCommentsAndLiterals(coordinator)
	body, err := cppFunctionBody(cleanCoordinator, "void ActionCoordinator::registerAllActions()")
	if err != nil {
		return registrationFailure(err)
	}
	if matches := audioProcessCall.FindAllStringIndex(body, -1); len(matches) != 1 {
		return registrationFailure(fmt.Errorf("registerAllActions must call registerAudioProcessActions exactly once"))
	}
	if len(audioProcessCall.FindAllStringIndex(cleanCoordinator, -1)) != 1 {
		return registrationFailure(fmt.Errorf("audio-process registration call must exist only in registerAllActions"))
	}

	cleanHeader := stripCPPCommentsAndLiterals(header)
	if len(audioProcessCall.FindAllStringIndex(cleanHeader, -1)) != 1 {
		return registrationFailure(fmt.Errorf("ActionCoordinator must declare registerAudioProcessActions exactly once"))
	}
	cleanActions := stripCPPCommentsAndLiterals(actions)
	if strings.Count(cleanActions, "void ActionCoordinator::registerAudioProcessActions()") != 1 ||
		strings.Count(actions, `"audio.divide_transients"`) != 1 {
		return registrationFailure(fmt.Errorf("pinned audio-process definition or action registration is missing"))
	}

	keymap, err := readFixtureFile(workDir, "config/default_keymap.yaml")
	if err != nil {
		return registrationFailure(err)
	}
	var document map[string]any
	if err := yaml.Unmarshal([]byte(keymap), &document); err != nil {
		return registrationFailure(fmt.Errorf("keymap YAML: %w", err))
	}
	if yamlScalarForKey(document, "oh") != "audio.divide_transients" {
		return registrationFailure(fmt.Errorf("production keymap route oh -> audio.divide_transients is missing"))
	}

	return VerifyOutcome{Passed: true, Compiled: true}
}

func yamlScalarForKey(value any, wanted string) string {
	switch node := value.(type) {
	case map[string]any:
		if scalar, ok := node[wanted].(string); ok {
			return scalar
		}
		for _, child := range node {
			if scalar := yamlScalarForKey(child, wanted); scalar != "" {
				return scalar
			}
		}
	case []any:
		for _, child := range node {
			if scalar := yamlScalarForKey(child, wanted); scalar != "" {
				return scalar
			}
		}
	}
	return ""
}

func registrationFailure(err error) VerifyOutcome {
	return VerifyOutcome{Failures: []string{err.Error()}}
}

func readFixtureFile(root, relative string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", relative, err)
	}
	return string(raw), nil
}

func cppFunctionBody(source, signature string) (string, error) {
	start := strings.Index(source, signature)
	if start < 0 {
		return "", fmt.Errorf("missing %s", signature)
	}
	open := strings.Index(source[start+len(signature):], "{")
	if open < 0 {
		return "", fmt.Errorf("missing body for %s", signature)
	}
	open += start + len(signature)
	depth := 0
	for index := open; index < len(source); index++ {
		switch source[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[open+1 : index], nil
			}
		}
	}
	return "", fmt.Errorf("unterminated body for %s", signature)
}

func stripCPPCommentsAndLiterals(source string) string {
	var output strings.Builder
	output.Grow(len(source))
	for index := 0; index < len(source); {
		if index+1 < len(source) && source[index:index+2] == "//" {
			for index < len(source) && source[index] != '\n' {
				output.WriteByte(' ')
				index++
			}
			continue
		}
		if index+1 < len(source) && source[index:index+2] == "/*" {
			output.WriteString("  ")
			index += 2
			for index < len(source) && !(index+1 < len(source) && source[index:index+2] == "*/") {
				if source[index] == '\n' {
					output.WriteByte('\n')
				} else {
					output.WriteByte(' ')
				}
				index++
			}
			if index+1 < len(source) {
				output.WriteString("  ")
				index += 2
			}
			continue
		}
		if source[index] == '"' || source[index] == '\'' {
			quote := source[index]
			output.WriteByte(' ')
			index++
			for index < len(source) {
				if source[index] == '\\' && index+1 < len(source) {
					output.WriteString("  ")
					index += 2
					continue
				}
				character := source[index]
				output.WriteByte(' ')
				index++
				if character == quote {
					break
				}
			}
			continue
		}
		output.WriteByte(source[index])
		index++
	}
	return output.String()
}
