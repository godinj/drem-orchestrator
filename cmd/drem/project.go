package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/godinj/drem-orchestrator/internal/model"
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

// splitPositional separates the first non-flag argument from the flag
// arguments in args. It understands the multi-value flags used by the
// register subcommand so a value like `--home-dir /tmp` does not get
// mistaken for two separate tokens. Unlike extractPositional, it
// returns an empty positional rather than an error when args contains
// only flags — register has flag-only invocations (fresh register
// without a positional), so missing-positional is not a usage error
// here.
func splitPositional(args []string) (string, []string) {
	flagsWithValue := map[string]bool{
		"--home-dir": true, "-home-dir": true,
		"--name": true, "-name": true,
		"--bare": true, "-bare": true,
		"--language": true, "-language": true,
		"--orch-url": true, "-orch-url": true,
		"--inference-endpoint": true, "-inference-endpoint": true,
		"--inference-model": true, "-inference-model": true,
		"--integration-policy": true, "-integration-policy": true,
		"--verification-policy": true, "-verification-policy": true,
		"--plan-review-policy": true, "-plan-review-policy": true,
		"--test-review-policy": true, "-test-review-policy": true,
		"--test-command": true, "-test-command": true,
		"--compile-command": true, "-compile-command": true,
	}
	var positional string
	var remaining []string
	found := false
	i := 0
	for i < len(args) {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			remaining = append(remaining, a)
			// --flag=value is a single token; flags-with-value
			// (--flag value) consume the next token too.
			if !strings.Contains(a, "=") && flagsWithValue[a] && i+1 < len(args) {
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
		remaining = append(remaining, a)
		i++
	}
	return positional, remaining
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

// cmdProjectRegister handles `drem project register`. A --update flag
// routes to cmdProjectRegisterUpdate, which re-renders the per-project
// compose.yml + drem.toml from current master templates while
// preserving on-disk state (SharedToken above all). See
// plans/drem-project-register-update.md.
func cmdProjectRegister(args []string, stdout io.Writer) error {
	// Extract any positional argument (project name) from args FIRST so
	// Go's flag parser doesn't stop at it. This supports the documented
	// invocation `drem project register --update drem-orchestrator
	// --force --home-dir ...` where the positional sits in the middle.
	positional, flagArgs := splitPositional(args)

	fs := flag.NewFlagSet("project register", flag.ContinueOnError)
	name := fs.String("name", "", "project name (required for fresh register; identifier for --update)")
	bare := fs.String("bare", "", "bare repo path (required for fresh register)")
	language := fs.String("language", "", "project language: go or cpp (required for fresh register)")
	orchURL := fs.String("orch-url", "", "orchestrator HTTP URL (required for fresh register)")
	inferenceEndpoint := fs.String("inference-endpoint", "", "OpenAI-compatible chat-completions URL for direct workers")
	inferenceModel := fs.String("inference-model", "", "served model ID for direct workers")
	integrationPolicy := fs.String("integration-policy", string(model.IntegrationAutoMerge), "delivery integration policy: auto_merge or prepare_branch")
	verificationPolicy := fs.String("verification-policy", string(model.VerificationLocalAutomated), "delivery verification policy: local_automated or external_ack")
	planReviewPolicy := fs.String("plan-review-policy", "", "plan approval policy: manual or sglang_safe_auto")
	testReviewPolicy := fs.String("test-review-policy", "", "test approval policy: manual or sglang_safe_auto")
	testCommand := fs.String("test-command", "", "project preliminary/test gate command")
	compileCommand := fs.String("compile-command", "", "project test-phase compilation gate command")
	homeDir := fs.String("home-dir", "", "override $HOME (testing)")
	update := fs.Bool("update", false, "regenerate per-project compose.yml + drem.toml from current templates (preserves SharedToken)")
	dryRun := fs.Bool("dry-run", false, "print what would change without writing (implies --update)")
	force := fs.Bool("force", false, "overwrite hand-patched drift without prompting (only valid with --update)")
	failOnDrift := fs.Bool("fail-on-drift", false, "exit non-zero if drift is detected (only valid with --update; for CI)")
	regenerateToken := fs.Bool("regenerate-token", false, "deliberately rotate SharedToken; restart orch + agentmon after (only valid with --update)")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}

	// Route to the update handler when --update (or an update-only flag) is set.
	if *update || *dryRun || *force || *failOnDrift || *regenerateToken {
		resolvedName := *name
		if resolvedName == "" {
			resolvedName = positional
		}
		return cmdProjectRegisterUpdate(registerUpdateOpts{
			Name:             resolvedName,
			HomeDir:          *homeDir,
			DryRun:           *dryRun,
			Force:            *force,
			FailOnDrift:      *failOnDrift,
			RegenerateToken:  *regenerateToken,
			PlanReviewPolicy: strings.TrimSpace(*planReviewPolicy),
			TestReviewPolicy: strings.TrimSpace(*testReviewPolicy),
			TestCommand:      strings.TrimSpace(*testCommand),
			CompileCommand:   strings.TrimSpace(*compileCommand),
			InferenceModel:   strings.TrimSpace(*inferenceModel),
		}, stdout)
	}

	if *name == "" || *bare == "" || *language == "" || *orchURL == "" {
		return fmt.Errorf("--name, --bare, --language, and --orch-url are required")
	}

	registryPath := resolveRegistryPath(*homeDir)
	reg, err := projects.Load(registryPath)
	if err != nil {
		return err
	}
	warmTokenPath, err := projects.EnsureWarmAgentToken(*homeDir)
	if err != nil {
		return err
	}

	if _, err := model.ParseIntegrationPolicy(*integrationPolicy); err != nil {
		return err
	}
	if _, err := model.ParseVerificationPolicy(*verificationPolicy); err != nil {
		return err
	}
	for label, raw := range map[string]string{"plan review": *planReviewPolicy, "test review": *testReviewPolicy} {
		if strings.TrimSpace(raw) != "" {
			if _, err := model.ParseReviewGatePolicy(strings.TrimSpace(raw)); err != nil {
				return fmt.Errorf("%s policy: %w", label, err)
			}
		}
	}
	p := projects.Project{
		Name:               *name,
		BareRepoPath:       *bare,
		Language:           *language,
		OrchURL:            *orchURL,
		InferenceEndpoint:  strings.TrimSpace(*inferenceEndpoint),
		InferenceModel:     strings.TrimSpace(*inferenceModel),
		IntegrationPolicy:  strings.TrimSpace(*integrationPolicy),
		VerificationPolicy: strings.TrimSpace(*verificationPolicy),
		PlanReviewPolicy:   strings.TrimSpace(*planReviewPolicy),
		TestReviewPolicy:   strings.TrimSpace(*testReviewPolicy),
		TestCommand:        strings.TrimSpace(*testCommand),
		CompileCommand:     strings.TrimSpace(*compileCommand),
	}
	p.OrchHostPort = projects.OrchHostPortFromURL(p.OrchURL)
	if p.OrchHostPort == 0 {
		p.OrchHostPort = reg.AllocateOrchHostPort()
	}
	if err := reg.Add(p); err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return err
	}

	data := templateDataFor(p)
	data.WarmAgentTokenPath = warmTokenPath
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

	// Configure receive.denyCurrentBranch=ignore on the bare repo so
	// the worker watchdog's final `git push` (issued from inside a
	// container with the bare repo bind-mounted) succeeds against our
	// shared-workspace layout. See plans/bare-repo-denyCurrentBranch.md.
	if err := projects.ConfigureBareRepo(p.BareRepoPath); err != nil {
		return fmt.Errorf("configure bare repo: %w", err)
	}

	fmt.Fprintf(stdout, "registered %q (language=%s)\n", p.Name, p.Language)
	fmt.Fprintf(stdout, "drem.toml:    %s\n", configPath)
	fmt.Fprintf(stdout, "compose file: %s\n", composePath)
	return nil
}

// registerUpdateOpts bundles the --update flags so cmdProjectRegister's
// flag parsing and cmdProjectRegisterUpdate's consumption agree on
// field names without shipping a stringly-typed map.
type registerUpdateOpts struct {
	Name             string
	HomeDir          string
	DryRun           bool
	Force            bool
	FailOnDrift      bool
	RegenerateToken  bool
	PlanReviewPolicy string
	TestReviewPolicy string
	TestCommand      string
	CompileCommand   string
	InferenceModel   string
}

// cmdProjectRegisterUpdate regenerates the per-project compose.yml +
// drem.toml from current templates. Preserves SharedToken (extracted
// from the on-disk compose) and OrchHostPort (registry, with on-disk
// fallback when registry is zero). Refuses silently-new-token
// regeneration unless --regenerate-token is passed. Surfaces hand-
// patched drift via Diff; default is warn-then-stop, --force overrides,
// --fail-on-drift turns warnings into errors.
func cmdProjectRegisterUpdate(opts registerUpdateOpts, stdout io.Writer) error {
	if opts.Name == "" {
		return fmt.Errorf("project name is required (either as positional argument or --name)")
	}

	// 1. Load the registry entry.
	registryPath := resolveRegistryPath(opts.HomeDir)
	reg, err := projects.Load(registryPath)
	if err != nil {
		return err
	}
	p, ok := reg.Get(opts.Name)
	if !ok {
		return fmt.Errorf("project %q not found in registry (run `drem project register` to create it first)", opts.Name)
	}
	warmTokenPath, err := projects.EnsureWarmAgentToken(opts.HomeDir)
	if err != nil {
		return err
	}
	updatedProject := *p
	if opts.PlanReviewPolicy != "" {
		if _, err := model.ParseReviewGatePolicy(opts.PlanReviewPolicy); err != nil {
			return fmt.Errorf("plan review policy: %w", err)
		}
		updatedProject.PlanReviewPolicy = opts.PlanReviewPolicy
	}
	if opts.TestReviewPolicy != "" {
		if _, err := model.ParseReviewGatePolicy(opts.TestReviewPolicy); err != nil {
			return fmt.Errorf("test review policy: %w", err)
		}
		updatedProject.TestReviewPolicy = opts.TestReviewPolicy
	}
	if opts.TestCommand != "" {
		updatedProject.TestCommand = opts.TestCommand
	}
	if opts.CompileCommand != "" {
		updatedProject.CompileCommand = opts.CompileCommand
	}
	if opts.InferenceModel != "" {
		updatedProject.InferenceModel = opts.InferenceModel
	}

	// 2. Extract on-disk state (SharedToken + observed host port).
	snap, err := projects.ReadStateFromDisk(opts.HomeDir, opts.Name)
	if err != nil {
		return fmt.Errorf("read on-disk state: %w", err)
	}

	// 3. Decide the SharedToken source.
	//    a. --regenerate-token forces a fresh uuid regardless of state.
	//    b. On-disk SharedToken wins when present.
	//    c. Empty on-disk + no --regenerate-token is fail-closed: the
	//       operator either hand-deleted compose.yml or the env key,
	//       and silent-regen would break orch/agentmon auth.
	token := snap.SharedToken
	switch {
	case opts.RegenerateToken:
		token = uuid.NewString()
	case token == "":
		return fmt.Errorf(
			"on-disk compose.yml has no DREM_AGENTMON_TOKEN (or compose.yml is missing); " +
				"pass --regenerate-token to rotate intentionally (will require restarting orch + agentmon)")
	}

	// 4. Build TemplateData from (registry entry + state snapshot).
	data := templateDataFor(updatedProject)
	data.WarmAgentTokenPath = warmTokenPath
	data.SharedToken = token
	// Prefer the registry's OrchHostPort when set; fall back to what
	// the on-disk compose observed; fall back to zero (applyDefaults
	// substitutes DefaultOrchHostPort).
	switch {
	case p.OrchHostPort > 0:
		data.OrchHostPort = p.OrchHostPort
	case snap.ObservedOrchHostPort > 0:
		data.OrchHostPort = snap.ObservedOrchHostPort
	}
	// DevMode carried on registry Project struct.
	data.DevMode = p.DevMode

	// 5. Render expected compose.yml + drem.toml.
	// ConfigFilePath must match the on-disk drem.toml path so the
	// bind-mount lands correctly (WriteProjectConfigAt will write to
	// this path).
	projectRoot := projectDir(opts.HomeDir, opts.Name)
	data.ConfigFilePath = filepath.Join(projectRoot, "drem.toml")
	renderedCompose, err := projects.Render(data)
	if err != nil {
		return fmt.Errorf("render compose: %w", err)
	}
	renderedConfig, err := projects.RenderConfig(data)
	if err != nil {
		return fmt.Errorf("render drem.toml: %w", err)
	}

	// 6. Diff against on-disk.
	composePath := filepath.Join(projectRoot, "compose.yml")
	tomlPath := filepath.Join(projectRoot, "drem.toml")
	onDiskCompose, _ := os.ReadFile(composePath) // zero-bytes on missing, which Diff treats as all-added
	onDiskToml, _ := os.ReadFile(tomlPath)
	composeDrift := projects.Diff(renderedCompose, onDiskCompose, projects.FileKindCompose)
	tomlDrift := projects.Diff(renderedConfig, onDiskToml, projects.FileKindDremToml)
	totalDrift := len(composeDrift) + len(tomlDrift)

	// 7. Print summary.
	if totalDrift > 0 {
		fmt.Fprintf(stdout, "drift detected for %q:\n", opts.Name)
		printDriftGroup(stdout, composePath, composeDrift)
		printDriftGroup(stdout, tomlPath, tomlDrift)
	}

	// 8. Dry-run: report and exit without writing.
	if opts.DryRun {
		if totalDrift == 0 {
			fmt.Fprintf(stdout, "%q is up to date (no drift)\n", opts.Name)
		}
		fmt.Fprintln(stdout, "--dry-run: no files written")
		return nil
	}

	// 9. Drift gating.
	if totalDrift > 0 && opts.FailOnDrift {
		return fmt.Errorf("drift detected with --fail-on-drift set: %d entries", totalDrift)
	}
	if totalDrift > 0 && !opts.Force {
		fmt.Fprintln(stdout, "")
		fmt.Fprintln(stdout, "no changes written. pass --force to proceed, or --regenerate-token to rotate auth.")
		return nil
	}

	// 10. Write both files atomically.
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		return fmt.Errorf("create project dir %q: %w", projectRoot, err)
	}
	if _, err := projects.WriteProjectConfigAt(opts.HomeDir, opts.Name, data); err != nil {
		return fmt.Errorf("write drem.toml: %w", err)
	}
	if _, err := projects.WriteProjectComposeAt(opts.HomeDir, opts.Name, data); err != nil {
		return fmt.Errorf("write compose: %w", err)
	}
	*p = updatedProject
	if err := reg.Save(); err != nil {
		return fmt.Errorf("save project registry: %w", err)
	}

	// Reapply the bare-repo config on every --update so operators
	// migrating from old installs pick up the setting without a
	// separate back-fill step. Idempotent by construction.
	if err := projects.ConfigureBareRepo(p.BareRepoPath); err != nil {
		return fmt.Errorf("configure bare repo: %w", err)
	}

	// 11. Summary.
	if totalDrift == 0 {
		fmt.Fprintf(stdout, "%q is up to date (no drift; files rewritten byte-identical)\n", opts.Name)
	} else {
		verb := "regenerated"
		if opts.Force {
			verb = "force-regenerated"
		}
		fmt.Fprintf(stdout, "%s %q: %d drift entries overwritten\n", verb, opts.Name, totalDrift)
	}
	if opts.RegenerateToken {
		fmt.Fprintln(stdout, "SharedToken rotated; restart orch + agentmon + csuite-watcher to pick up the new token")
	} else {
		fmt.Fprintln(stdout, "SharedToken preserved; running services keep their auth")
	}
	fmt.Fprintf(stdout, "compose file: %s\n", composePath)
	fmt.Fprintf(stdout, "drem.toml:    %s\n", tomlPath)
	return nil
}

// printDriftGroup emits a human-readable subsection of drift entries
// under a file label. Stable ordering (Diff returns entries sorted by
// Path) so operators can diff the output across runs.
func printDriftGroup(w io.Writer, path string, entries []projects.DriftEntry) {
	if len(entries) == 0 {
		return
	}
	fmt.Fprintf(w, "  %s:\n", path)
	for _, e := range entries {
		switch e.Kind {
		case "added":
			fmt.Fprintf(w, "    + %s = %q\n", e.Path, e.NewValue)
		case "removed":
			fmt.Fprintf(w, "    - %s = %q\n", e.Path, e.WasValue)
		case "changed":
			fmt.Fprintf(w, "    ~ %s: %q -> %q\n", e.Path, e.WasValue, e.NewValue)
		}
	}
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
		"seth": "localhost:5000/drem-csuite-seth:latest",
		"kyle": "localhost:5000/drem-csuite-kyle:latest",
	}
	for persona := range csuiteImages {
		if ov, ok := p.ContainerImageOverrides["csuite-"+persona]; ok && ov != "" {
			csuiteImages[persona] = ov
		}
	}
	return projects.TemplateData{
		ProjectName:        p.Name,
		OrchURL:            p.OrchURL,
		Language:           p.Language,
		WorkerImage:        workerImage,
		MergerImage:        mergerImage,
		CsuiteImages:       csuiteImages,
		BareRepoPath:       p.BareRepoPath,
		InferenceEndpoint:  p.InferenceEndpoint,
		InferenceModel:     p.InferenceModel,
		IntegrationPolicy:  effectiveIntegrationPolicy(p.IntegrationPolicy),
		VerificationPolicy: effectiveVerificationPolicy(p.VerificationPolicy),
		PlanReviewPolicy:   effectiveReviewPolicy(p.PlanReviewPolicy),
		TestReviewPolicy:   effectiveReviewPolicy(p.TestReviewPolicy),
		TestCommand:        p.TestCommand,
		CompileCommand:     p.CompileCommand,
		OrchHostPort:       p.OrchHostPort,
		SharedToken:        uuid.NewString(),
		UseNamedDBVolume:   runtime.GOOS == "darwin",
	}
}

func effectiveReviewPolicy(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return string(model.ReviewGateManual)
	}
	return raw
}

func effectiveIntegrationPolicy(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return string(model.IntegrationAutoMerge)
	}
	return raw
}

func effectiveVerificationPolicy(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return string(model.VerificationLocalAutomated)
	}
	return raw
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
	inferenceEndpoint := p.InferenceEndpoint
	if inferenceEndpoint == "" {
		inferenceEndpoint = projects.DefaultInferenceEndpoint
	}
	fmt.Fprintf(&b, "Inference:     %s\n", inferenceEndpoint)
	inferenceModel := p.InferenceModel
	if inferenceModel == "" {
		inferenceModel = projects.DefaultInferenceModel
	}
	fmt.Fprintf(&b, "Model:         %s\n", inferenceModel)
	fmt.Fprintf(&b, "PlanReview:   %s\n", effectiveReviewPolicy(p.PlanReviewPolicy))
	fmt.Fprintf(&b, "TestReview:   %s\n", effectiveReviewPolicy(p.TestReviewPolicy))
	if p.TestCommand != "" {
		fmt.Fprintf(&b, "TestCommand:  %s\n", p.TestCommand)
	}
	if p.CompileCommand != "" {
		fmt.Fprintf(&b, "CompileCommand: %s\n", p.CompileCommand)
	}
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
