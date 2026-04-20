package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/projects"
)

// runProject is the entry point for the `drem project ...` subcommand.
// It is invoked from main() when os.Args[1] == "project" and calls
// os.Exit on failure. The args slice is everything after "project"
// (i.e. os.Args[2:]).
func runProject(args []string) {
	if err := dispatchProject(args, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// dispatchProject parses the subcommand name and routes to the correct
// handler. Extracted from runProject so tests can call it without
// touching os.Exit.
func dispatchProject(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem project <register|list|remove|show> [flags]")
	}
	cmd := args[0]
	rest := args[1:]
	switch cmd {
	case "register":
		return cmdProjectRegister(rest, stdout)
	case "list":
		return cmdProjectList(rest, stdout)
	case "remove":
		return cmdProjectRemove(rest, stdout)
	case "show":
		return cmdProjectShow(rest, stdout)
	default:
		return fmt.Errorf("unknown project subcommand %q", cmd)
	}
}

// extractPositional pulls the single non-flag argument out of args. It
// understands the flags used by project subcommands (--home-dir takes a
// value). Returns the positional value and the remaining flag arguments.
func extractPositional(args []string) (string, []string, error) {
	flagsWithValue := map[string]bool{"--home-dir": true, "-home-dir": true}
	var positional string
	var remaining []string
	found := false
	i := 0
	for i < len(args) {
		a := args[i]
		if a == "--" {
			// Everything after -- is positional.
			if i+1 < len(args) && !found {
				positional = args[i+1]
				found = true
				remaining = append(remaining, args[i+2:]...)
				i = len(args)
				continue
			}
			i++
			continue
		}
		// Handle --flag=value form as a single token.
		if strings.HasPrefix(a, "-") {
			remaining = append(remaining, a)
			if flagsWithValue[a] && i+1 < len(args) {
				remaining = append(remaining, args[i+1])
				i += 2
				continue
			}
			i++
			continue
		}
		if !found {
			positional = a
			found = true
			i++
			continue
		}
		// Additional positional args are unexpected; return them so the
		// caller can surface a usage error.
		remaining = append(remaining, a)
		i++
	}
	if !found {
		return "", nil, fmt.Errorf("missing positional argument")
	}
	return positional, remaining, nil
}

// resolveRegistryPath returns the registry path, honouring an optional
// homeDir override. When homeDir is empty the canonical $HOME path is
// used.
func resolveRegistryPath(homeDir string) string {
	if homeDir == "" {
		return projects.DefaultPath()
	}
	return filepath.Join(homeDir, ".drem", "projects.toml")
}

// projectDir returns the per-project directory under the given home dir.
func projectDir(homeDir, name string) string {
	if homeDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return filepath.Join(".drem", "projects", name)
		}
		homeDir = home
	}
	return filepath.Join(homeDir, ".drem", "projects", name)
}

// cmdProjectRegister handles `drem project register`.
func cmdProjectRegister(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("project register", flag.ContinueOnError)
	name := fs.String("name", "", "project name (required)")
	bare := fs.String("bare", "", "bare repo path (required)")
	language := fs.String("language", "", "project language: go or cpp (required)")
	orchURL := fs.String("orch-url", "", "orchestrator HTTP URL (required)")
	homeDir := fs.String("home-dir", "", "override $HOME (testing)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *name == "" || *bare == "" || *language == "" || *orchURL == "" {
		return fmt.Errorf("--name, --bare, --language, and --orch-url are required")
	}

	registryPath := resolveRegistryPath(*homeDir)
	reg, err := projects.Load(registryPath)
	if err != nil {
		return err
	}

	p := projects.Project{
		Name:         *name,
		BareRepoPath: *bare,
		Language:     *language,
		OrchURL:      *orchURL,
	}
	if err := reg.Add(p); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}

	data := templateDataFor(p)
	configPath, err := projects.WriteProjectConfigAt(*homeDir, p.Name, data)
	if err != nil {
		return fmt.Errorf("write drem.toml: %w", err)
	}
	// Plumb the config path into the compose render so the bind-mount
	// target matches the file we just wrote.
	data.ConfigFilePath = configPath
	composePath, err := projects.WriteProjectComposeAt(*homeDir, p.Name, data)
	if err != nil {
		return fmt.Errorf("write compose file: %w", err)
	}

	fmt.Fprintf(stdout, "registered %q (language=%s)\n", p.Name, p.Language)
	fmt.Fprintf(stdout, "drem.toml:    %s\n", configPath)
	fmt.Fprintf(stdout, "compose file: %s\n", composePath)
	return nil
}

// templateDataFor populates a TemplateData from a Project using the
// per-language image defaults plus a freshly generated shared token.
func templateDataFor(p projects.Project) projects.TemplateData {
	workerImage := defaultWorkerImage(p.Language)
	if ov, ok := p.ContainerImageOverrides["coder"]; ok && ov != "" {
		workerImage = ov
	}
	mergerImage := "localhost:5000/drem-merger:latest"
	if ov, ok := p.ContainerImageOverrides["merger"]; ok && ov != "" {
		mergerImage = ov
	}
	csuiteImages := map[string]string{
		"mike": "localhost:5000/drem-csuite-mike:latest",
		"alex": "localhost:5000/drem-csuite-alex:latest",
		"ross": "localhost:5000/drem-csuite-ross:latest",
		"seth": "localhost:5000/drem-csuite-seth:latest",
	}
	for persona := range csuiteImages {
		if ov, ok := p.ContainerImageOverrides["csuite-"+persona]; ok && ov != "" {
			csuiteImages[persona] = ov
		}
	}
	return projects.TemplateData{
		ProjectName:  p.Name,
		OrchURL:      p.OrchURL,
		Language:     p.Language,
		WorkerImage:  workerImage,
		MergerImage:  mergerImage,
		CsuiteImages: csuiteImages,
		BareRepoPath: p.BareRepoPath,
		SharedToken:  uuid.NewString(),
	}
}

// defaultWorkerImage returns the per-language worker image tag.
func defaultWorkerImage(language string) string {
	switch language {
	case projects.LanguageCpp:
		return "localhost:5000/drem-worker-cpp:latest"
	default:
		return "localhost:5000/drem-worker-go:latest"
	}
}

// cmdProjectList handles `drem project list`.
func cmdProjectList(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
	homeDir := fs.String("home-dir", "", "override $HOME (testing)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	reg, err := projects.Load(resolveRegistryPath(*homeDir))
	if err != nil {
		return err
	}
	tw := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tLANGUAGE\tBARE PATH\tORCH URL")
	for _, p := range reg.Projects {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", p.Name, p.Language, p.BareRepoPath, p.OrchURL)
	}
	return tw.Flush()
}

// cmdProjectRemove handles `drem project remove <name>`.
func cmdProjectRemove(args []string, stdout io.Writer) error {
	name, flagArgs, err := extractPositional(args)
	if err != nil {
		return fmt.Errorf("usage: drem project remove <name>")
	}
	fs := flag.NewFlagSet("project remove", flag.ContinueOnError)
	homeDir := fs.String("home-dir", "", "override $HOME (testing)")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	registryPath := resolveRegistryPath(*homeDir)
	reg, err := projects.Load(registryPath)
	if err != nil {
		return err
	}
	if err := reg.Remove(name); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}

	dir := projectDir(*homeDir, name)
	composePath := filepath.Join(dir, "compose.yml")
	if err := os.Remove(composePath); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(stdout, "warning: could not delete compose file %s: %v\n", composePath, err)
	}
	// Clean up the per-project directory if empty.
	if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
		_ = os.Remove(dir)
	}

	fmt.Fprintf(stdout, "removed %q\n", name)
	return nil
}

// cmdProjectShow handles `drem project show <name>`.
func cmdProjectShow(args []string, stdout io.Writer) error {
	name, flagArgs, err := extractPositional(args)
	if err != nil {
		return fmt.Errorf("usage: drem project show <name>")
	}
	fs := flag.NewFlagSet("project show", flag.ContinueOnError)
	homeDir := fs.String("home-dir", "", "override $HOME (testing)")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	reg, err := projects.Load(resolveRegistryPath(*homeDir))
	if err != nil {
		return err
	}
	p, ok := reg.Get(name)
	if !ok {
		return fmt.Errorf("project %q not found", name)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Name:          %s\n", p.Name)
	fmt.Fprintf(&b, "Language:      %s\n", p.Language)
	fmt.Fprintf(&b, "BareRepoPath:  %s\n", p.BareRepoPath)
	fmt.Fprintf(&b, "OrchURL:       %s\n", p.OrchURL)
	if len(p.ContainerImageOverrides) > 0 {
		fmt.Fprintln(&b, "ContainerImageOverrides:")
		for k, v := range p.ContainerImageOverrides {
			fmt.Fprintf(&b, "  %s = %s\n", k, v)
		}
	}
	fmt.Fprintf(&b, "ComposeFile:   %s\n", filepath.Join(projectDir(*homeDir, p.Name), "compose.yml"))
	_, err = io.WriteString(stdout, b.String())
	return err
}
