package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gr/internal/project"
	"gr/internal/rdeps"
	"gr/internal/rmanager"
	"gr/internal/runner"
)

type stringList []string

type scanReport struct {
	Script          string   `json:"script"`
	Packages        []string `json:"packages"`
	CRANPackages    []string `json:"cran_packages"`
	BiocPackages    []string `json:"bioc_packages"`
	InstallableOnly bool     `json:"installable_only"`
}

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func Run(args []string) error {
	if len(args) == 0 {
		return usageError()
	}

	switch args[0] {
	case "init":
		return initCommand(args[1:])
	case "add":
		return addCommand(args[1:])
	case "remove":
		return removeCommand(args[1:])
	case "lock":
		return lockCommand(args[1:])
	case "r":
		return rCommand(args[1:])
	case "list":
		return listCommand(args[1:])
	case "prune":
		return pruneCommand(args[1:])
	case "shell":
		return shellCommand(args[1:])
	case "exec":
		return execCommand(args[1:])
	case "cache":
		return cacheCommand(args[1:])
	case "run":
		return runCommand(args[1:])
	case "scan":
		return scanCommand(args[1:])
	case "sync":
		return syncCommand(args[1:])
	case "check":
		return checkCommand(args[1:])
	case "doctor":
		return doctorCommand(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usageText())
	}
}

func runCommand(args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install missing packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use for resolution and execution")
	noInstall := fs.Bool("no-install", false, "skip package installation and use current libraries only")
	locked := fs.Bool("locked", false, "require a valid lockfile but allow installing missing packages without updating it")
	frozen := fs.Bool("frozen", false, "require a valid lockfile and installed packages; do not modify dependencies")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before execution")
	fs.Var(&packages, "package", "extra R package to install before running the script (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to install before running the script (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before running the script (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved environment (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	rest := fs.Args()
	if len(rest) == 0 {
		return errors.New("missing R script path\n\nusage: rs run [flags] path/to/script.R [script args...]")
	}

	scriptPath, err := filepath.Abs(rest[0])
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	runArgs := rest[1:]
	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	opts := runner.RunOptions{
		ScriptPath:    scriptPath,
		ScriptArgs:    runArgs,
		ExtraDeps:     mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps: mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:   excludePackages,
		Repo:          *repo,
		CacheDir:      *cacheDir,
		RscriptPath:   *rscript,
		SkipInstall:   *noInstall,
		Locked:        *locked,
		Frozen:        *frozen,
		Verbose:       *verbose,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
	}

	return runner.Run(opts)
}

func initCommand(args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	repo := fs.String("repo", project.DefaultRepo, "default CRAN mirror written to rs.toml")
	cacheDir := fs.String("cache-dir", ".rs-cache", "managed library cache directory written to rs.toml")
	lockfile := fs.String("lockfile", "rs.lock.json", "lockfile path written to rs.toml")
	rscript := fs.String("rscript", "", "default Rscript binary or path written to rs.toml")
	rVersion := fs.String("r-version", "", "default R version selector written to rs.toml")
	var fromScripts stringList
	var fromDirs stringList
	var includePackages stringList
	var excludePackages stringList
	var biocPackages stringList
	writeScriptBlock := fs.Bool("write-script-block", false, "with one --from, write detected packages under a script-specific block")
	includeBundled := fs.Bool("include-bundled", false, "with --from or --from-dir, keep R bundled base/recommended packages in generated config")
	force := fs.Bool("force", false, "overwrite an existing rs.toml")
	fs.Var(&fromScripts, "from", "scan packages from an existing R script and seed rs.toml (repeatable)")
	fs.Var(&fromDirs, "from-dir", "scan all R scripts under a directory and seed rs.toml (repeatable)")
	fs.Var(&includePackages, "include", "add an extra project-level dependency to generated config (repeatable)")
	fs.Var(&excludePackages, "exclude", "exclude a detected dependency from generated config (repeatable)")
	fs.Var(&biocPackages, "bioc-package", "add an extra project-level Bioconductor dependency to generated config (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs init [flags] [dir]")
	}

	targetDir := "."
	if fs.NArg() == 1 {
		targetDir = fs.Arg(0)
	}
	targetDir, err := filepath.Abs(targetDir)
	if err != nil {
		return fmt.Errorf("resolve target dir: %w", err)
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return fmt.Errorf("create target dir: %w", err)
	}

	configPath := filepath.Join(targetDir, project.ConfigFileName)
	if _, err := os.Stat(configPath); err == nil && !*force {
		return fmt.Errorf("%s already exists\nrerun with --force to overwrite", configPath)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat config: %w", err)
	}

	initOpts := project.InitOptions{
		Repo:         *repo,
		CacheDir:     *cacheDir,
		Lockfile:     *lockfile,
		Rscript:      *rscript,
		RVersion:     *rVersion,
		BiocPackages: biocPackages,
	}
	includeCRAN, includeBioc := rdeps.SplitBiocPackages(includePackages)
	initOpts.Packages = mergeInitDeps(initOpts.Packages, includeCRAN)
	initOpts.BiocPackages = mergeInitDeps(initOpts.BiocPackages, includeBioc)

	scriptConfigs := map[string]project.ScriptConfig{}
	for _, rawDir := range fromDirs {
		dirPath, err := filepath.Abs(rawDir)
		if err != nil {
			return fmt.Errorf("resolve --from-dir path: %w", err)
		}
		scriptPaths, err := collectInitScriptPaths(dirPath)
		if err != nil {
			return err
		}
		for _, path := range scriptPaths {
			fromScripts = append(fromScripts, path)
		}
	}
	for _, rawPath := range fromScripts {
		fromPath, err := filepath.Abs(rawPath)
		if err != nil {
			return fmt.Errorf("resolve --from script path: %w", err)
		}
		deps, err := runner.ScanScript(fromPath)
		if err != nil {
			return err
		}
		if !*includeBundled {
			deps = rdeps.FilterInstallable(deps)
		}
		deps = filterInitExcludedDeps(deps, excludePackages)
		cranDeps, detectedBioc := rdeps.SplitBiocPackages(deps)
		scriptCfg := scriptConfigs[fromPath]
		scriptCfg.Packages = mergeInitDeps(scriptCfg.Packages, cranDeps)
		scriptCfg.BiocPackages = mergeInitDeps(scriptCfg.BiocPackages, detectedBioc)
		scriptConfigs[fromPath] = scriptCfg
	}

	writeScripts := *writeScriptBlock || len(scriptConfigs) > 1
	if len(scriptConfigs) == 1 && !writeScripts {
		for _, scriptCfg := range scriptConfigs {
			initOpts.Packages = mergeInitDeps(initOpts.Packages, scriptCfg.Packages)
			initOpts.BiocPackages = mergeInitDeps(initOpts.BiocPackages, scriptCfg.BiocPackages)
		}
	}

	cfg, err := project.NewConfigFromScripts(initOpts, targetDir, scriptConfigs, writeScripts)
	if err != nil {
		return err
	}
	if err := project.Save(configPath, cfg); err != nil {
		return err
	}

	fmt.Fprintf(os.Stdout, "wrote %s\n", configPath)
	if len(scriptConfigs) > 0 {
		scriptPaths := make([]string, 0, len(scriptConfigs))
		for path := range scriptConfigs {
			scriptPaths = append(scriptPaths, path)
		}
		slices.Sort(scriptPaths)
		for _, path := range scriptPaths {
			total := len(scriptConfigs[path].Packages) + len(scriptConfigs[path].BiocPackages)
			fmt.Fprintf(os.Stdout, "seeded %d package(s) from %s\n", total, path)
		}
	}
	return nil
}

func mergeInitDeps(left, right []string) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	out := make([]string, 0, len(left)+len(right))
	for _, dep := range left {
		if _, ok := seen[dep]; ok {
			continue
		}
		seen[dep] = struct{}{}
		out = append(out, dep)
	}
	for _, dep := range right {
		if _, ok := seen[dep]; ok {
			continue
		}
		seen[dep] = struct{}{}
		out = append(out, dep)
	}
	slices.Sort(out)
	return out
}

func splitIncludedPackages(includePackages []string) ([]string, []string) {
	return rdeps.SplitBiocPackages(includePackages)
}

func filterInitExcludedDeps(deps []string, excludes []string) []string {
	if len(deps) == 0 || len(excludes) == 0 {
		return deps
	}
	excluded := make(map[string]struct{}, len(excludes))
	for _, dep := range excludes {
		excluded[dep] = struct{}{}
	}
	out := make([]string, 0, len(deps))
	for _, dep := range deps {
		if _, ok := excluded[dep]; ok {
			continue
		}
		out = append(out, dep)
	}
	return out
}

func collectInitScriptPaths(root string) ([]string, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat --from-dir path: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", root)
	}

	var out []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".rs-cache":
				return filepath.SkipDir
			}
			return nil
		}
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".r", ".rscript":
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk --from-dir scripts: %w", err)
	}
	slices.Sort(out)
	return out, nil
}

func addCommand(args []string) error {
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	bioc := fs.Bool("bioc", false, "add packages to bioc_packages instead of packages")
	script := fs.String("script", "", "add packages only for the given script profile")
	projectDir := fs.String("project-dir", "", "start config discovery from this directory instead of the current working directory")
	sourceType := fs.String("source", "", "custom source type: github, git, or local")
	githubRepo := fs.String("github-repo", "", "GitHub repository for --source github (owner/name)")
	url := fs.String("url", "", "repository URL for --source git")
	path := fs.String("path", "", "local path for --source local")
	ref := fs.String("ref", "", "git or GitHub ref for custom source packages")
	subdir := fs.String("subdir", "", "package subdirectory inside the custom source")
	host := fs.String("host", "", "GitHub API host for GitHub Enterprise installs")
	tokenEnv := fs.String("token-env", "", "environment variable name holding a GitHub token")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rs add [flags] <package> [package...]")
	}

	packages := fs.Args()
	if *sourceType != "" {
		if len(packages) != 1 {
			return errors.New("custom source packages must be added one at a time")
		}
		if *bioc {
			return errors.New("--bioc cannot be combined with --source")
		}
	}

	discoveryDir := *projectDir
	if discoveryDir == "" {
		discoveryDir = "."
	}
	if *script != "" {
		scriptPath := *script
		if !filepath.IsAbs(scriptPath) && *projectDir != "" {
			scriptPath = filepath.Join(*projectDir, scriptPath)
		}
		scriptPath, err := filepath.Abs(scriptPath)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		*script = scriptPath
		if *projectDir == "" {
			discoveryDir = filepath.Dir(scriptPath)
		}
	}
	discoveryDir, err := filepath.Abs(discoveryDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	projectCfg, found, err := project.Discover(discoveryDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	if !found || projectCfg.Path == "" {
		return fmt.Errorf("no %s found from %s\nrun `rs init` first", project.ConfigFileName, discoveryDir)
	}

	cfg, err := project.LoadEditable(projectCfg.Path)
	if err != nil {
		return err
	}

	var sourceSpec *project.SourceSpec
	if *sourceType != "" {
		sourceSpec = &project.SourceSpec{
			Type:     *sourceType,
			Host:     *host,
			Repo:     *githubRepo,
			URL:      *url,
			Ref:      *ref,
			Path:     *path,
			Subdir:   *subdir,
			TokenEnv: *tokenEnv,
		}
	}

	for _, pkg := range packages {
		if err := project.AddPackage(&cfg, project.AddPackageOptions{
			ScriptPath: *script,
			Package:    pkg,
			Bioc:       *bioc,
			Source:     sourceSpec,
		}); err != nil {
			return err
		}
	}

	if err := project.Save(cfg.Path, cfg); err != nil {
		return err
	}

	scope := "project"
	if *script != "" {
		scope = *script
	}
	fmt.Fprintf(os.Stdout, "updated %s (%s)\n", cfg.Path, scope)
	return nil
}

func removeCommand(args []string) error {
	fs := flag.NewFlagSet("remove", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	bioc := fs.Bool("bioc", false, "remove packages from bioc_packages instead of packages")
	script := fs.String("script", "", "remove packages only from the given script profile")
	projectDir := fs.String("project-dir", "", "start config discovery from this directory instead of the current working directory")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: rs remove [flags] <package> [package...]")
	}

	discoveryDir := *projectDir
	if discoveryDir == "" {
		discoveryDir = "."
	}
	if *script != "" {
		scriptPath := *script
		if !filepath.IsAbs(scriptPath) && *projectDir != "" {
			scriptPath = filepath.Join(*projectDir, scriptPath)
		}
		scriptPath, err := filepath.Abs(scriptPath)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		*script = scriptPath
		if *projectDir == "" {
			discoveryDir = filepath.Dir(scriptPath)
		}
	}
	discoveryDir, err := filepath.Abs(discoveryDir)
	if err != nil {
		return fmt.Errorf("resolve project dir: %w", err)
	}

	projectCfg, found, err := project.Discover(discoveryDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	if !found || projectCfg.Path == "" {
		return fmt.Errorf("no %s found from %s\nrun `rs init` first", project.ConfigFileName, discoveryDir)
	}

	cfg, err := project.LoadEditable(projectCfg.Path)
	if err != nil {
		return err
	}

	for _, pkg := range fs.Args() {
		if err := project.RemovePackage(&cfg, project.RemovePackageOptions{
			ScriptPath: *script,
			Package:    pkg,
			Bioc:       *bioc,
		}); err != nil {
			return err
		}
	}

	if err := project.Save(cfg.Path, cfg); err != nil {
		return err
	}

	scope := "project"
	if *script != "" {
		scope = *script
	}
	fmt.Fprintf(os.Stdout, "updated %s (%s)\n", cfg.Path, scope)
	return nil
}

func lockCommand(args []string) error {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install and lock packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use while locking")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before locking")
	fs.Var(&packages, "package", "extra R package to include before writing the lockfile (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include before writing the lockfile (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before writing the lockfile (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved lock plan (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs lock [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Lock(runner.LockOptions{
		ScriptPath:    scriptPath,
		ExtraDeps:     mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps: mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:   excludePackages,
		Repo:          *repo,
		CacheDir:      *cacheDir,
		RscriptPath:   *rscript,
		Verbose:       *verbose,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
	})
}

func listCommand(args []string) error {
	fs := flag.NewFlagSet("list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to resolve the dependency plan")
	cacheDir := fs.String("cache-dir", "", "cache directory used to compute the managed library path")
	rscript := fs.String("rscript", "", "Rscript binary or path to use while resolving the plan")
	jsonOutput := fs.Bool("json", false, "print the resolved dependency plan as JSON")
	fs.Var(&packages, "package", "extra CRAN package to include in the resolved plan (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include in the resolved plan (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include in the resolved plan (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved plan (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs list [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.List(runner.ListOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:   mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:     excludePackages,
		IncludeDeps:     includeCRAN,
		IncludeBiocDeps: includeBioc,
		Repo:            *repo,
		CacheDir:        *cacheDir,
		RscriptPath:     *rscript,
		JSON:            *jsonOutput,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	})
}

func pruneCommand(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	projectDir := fs.String("project-dir", "", "project directory to prune when no script path is provided")
	dryRun := fs.Bool("dry-run", false, "show which managed libraries would be removed without deleting them")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs prune [flags] [path/to/script.R]")
	}

	scriptPath := ""
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.Prune(runner.PruneOptions{
		ScriptPath: scriptPath,
		ProjectDir: *projectDir,
		DryRun:     *dryRun,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func shellCommand(args []string) error {
	fs := flag.NewFlagSet("shell", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install missing packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use for the managed shell")
	noInstall := fs.Bool("no-install", false, "skip package installation and use current libraries only")
	locked := fs.Bool("locked", false, "require a valid lockfile but allow installing missing packages without updating it")
	frozen := fs.Bool("frozen", false, "require a valid lockfile and installed packages; do not modify dependencies")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before opening the shell")
	fs.Var(&packages, "package", "extra R package to install before opening the shell (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to install before opening the shell (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before opening the shell (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved shell environment (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs shell [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Shell(runner.ShellOptions{
		ScriptPath:    scriptPath,
		ExtraDeps:     mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps: mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:   excludePackages,
		Repo:          *repo,
		CacheDir:      *cacheDir,
		RscriptPath:   *rscript,
		SkipInstall:   *noInstall,
		Locked:        *locked,
		Frozen:        *frozen,
		Verbose:       *verbose,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
	})
}

func execCommand(args []string) error {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to install missing packages")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use for the managed expression")
	noInstall := fs.Bool("no-install", false, "skip package installation and use current libraries only")
	locked := fs.Bool("locked", false, "require a valid lockfile but allow installing missing packages without updating it")
	frozen := fs.Bool("frozen", false, "require a valid lockfile and installed packages; do not modify dependencies")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before executing the expression")
	expression := fs.String("e", "", "R expression to execute")
	fs.StringVar(expression, "expr", "", "R expression to execute")
	fs.Var(&packages, "package", "extra R package to install before executing the expression (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to install before executing the expression (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include before executing the expression (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from the resolved expression environment (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if strings.TrimSpace(*expression) == "" {
		return errors.New("usage: rs exec [flags] -e 'expression' path/to/script.R")
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs exec [flags] -e 'expression' path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Exec(runner.ExecOptions{
		ScriptPath:    scriptPath,
		Expression:    *expression,
		ExtraDeps:     mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps: mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:   excludePackages,
		Repo:          *repo,
		CacheDir:      *cacheDir,
		RscriptPath:   *rscript,
		SkipInstall:   *noInstall,
		Locked:        *locked,
		Frozen:        *frozen,
		Verbose:       *verbose,
		Stdout:        os.Stdout,
		Stderr:        os.Stderr,
	})
}

func cacheCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rs cache <dir|ls|rm> [args]")
	}

	switch args[0] {
	case "dir":
		return cacheDirCommand(args[1:])
	case "ls":
		return cacheListCommand(args[1:])
	case "rm":
		return cacheRemoveCommand(args[1:])
	default:
		return fmt.Errorf("unknown cache subcommand %q\n\n%s", args[0], usageText())
	}
}

func cacheDirCommand(args []string) error {
	fs := flag.NewFlagSet("cache dir", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs cache dir [path/to/script.R]")
	}

	scriptPath := ""
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.CacheDir(runner.CacheDirOptions{
		ScriptPath: scriptPath,
		Stdout:     os.Stdout,
	})
}

func cacheListCommand(args []string) error {
	fs := flag.NewFlagSet("cache ls", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	projectDir := fs.String("project-dir", "", "project directory used to mark active libraries")
	jsonOutput := fs.Bool("json", false, "print cache libraries as JSON")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs cache ls [flags] [path/to/script.R]")
	}

	scriptPath := ""
	if fs.NArg() == 1 {
		abs, err := filepath.Abs(fs.Arg(0))
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.CacheList(runner.CacheListOptions{
		ScriptPath: scriptPath,
		ProjectDir: *projectDir,
		JSON:       *jsonOutput,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func cacheRemoveCommand(args []string) error {
	fs := flag.NewFlagSet("cache rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	script := fs.String("script", "", "script path used to resolve the cache root when removing by hash")
	projectDir := fs.String("project-dir", "", "project directory used to resolve the cache root when removing by hash")
	cacheDir := fs.String("cache-dir", "", "explicit cache root used when removing by hash")
	dryRun := fs.Bool("dry-run", false, "show which managed library would be removed without deleting it")

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs cache rm [flags] <hash|path>")
	}

	scriptPath := ""
	if *script != "" {
		abs, err := filepath.Abs(*script)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
		scriptPath = abs
	}

	return runner.CacheRemove(runner.CacheRemoveOptions{
		Target:     fs.Arg(0),
		ScriptPath: scriptPath,
		ProjectDir: *projectDir,
		CacheDir:   *cacheDir,
		DryRun:     *dryRun,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	})
}

func syncCommand(args []string) error {
	return lockCommand(args)
}

func rCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rs r <list|install|use|which> [args]")
	}

	switch args[0] {
	case "list":
		return rListCommand(args[1:])
	case "install":
		return rInstallCommand(args[1:])
	case "use":
		return rUseCommand(args[1:])
	case "which":
		return rWhichCommand(args[1:])
	default:
		return fmt.Errorf("unknown r subcommand %q\n\n%s", args[0], usageText())
	}
}

func rListCommand(args []string) error {
	fs := flag.NewFlagSet("r list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("usage: rs r list")
	}
	return rmanager.List(os.Stdout, os.Stderr)
}

func rInstallCommand(args []string) error {
	fs := flag.NewFlagSet("r install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	method := fs.String("method", string(rmanager.InstallMethodAuto), "install method: auto, binary, or source")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs r install [--method auto|binary|source] <version>")
	}
	return rmanager.InstallWithOptions(rmanager.InstallOptions{
		Version: fs.Arg(0),
		Method:  rmanager.InstallMethod(strings.TrimSpace(*method)),
		Stdout:  os.Stdout,
		Stderr:  os.Stderr,
	})
}

func rUseCommand(args []string) error {
	fs := flag.NewFlagSet("r use", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	projectDir := fs.String("project-dir", ".", "directory or script path used to find rs.toml")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rs r use [--project-dir <dir>] <version|Rscript-path>")
	}

	startDir, _, err := resolveProjectTarget(*projectDir)
	if err != nil {
		return err
	}
	cfg, found, err := project.Discover(startDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	if !found {
		return fmt.Errorf("no %s found from %s\nrun `rs init` first", project.ConfigFileName, startDir)
	}

	editable, err := project.LoadEditable(cfg.Path)
	if err != nil {
		return err
	}
	spec := fs.Arg(0)
	if rmanager.LooksLikeVersionSpec(spec) && !strings.Contains(strings.ToLower(spec), "rscript") {
		if err := rmanager.ValidateVersionSelector(spec); err != nil {
			return err
		}
		editable.Defaults.RVersion = spec
		editable.Defaults.Rscript = ""
	} else {
		rscriptPath, err := rmanager.ResolveVersionOrPath(spec)
		if err != nil {
			return err
		}
		editable.Defaults.Rscript = rscriptPath
		editable.Defaults.RVersion = ""
	}
	if err := project.Save(editable.Path, editable); err != nil {
		return err
	}

	if editable.Defaults.RVersion != "" {
		fmt.Fprintf(os.Stdout, "updated %s (r_version = %s)\n", editable.Path, editable.Defaults.RVersion)
	} else {
		fmt.Fprintf(os.Stdout, "updated %s (rscript = %s)\n", editable.Path, editable.Defaults.Rscript)
	}
	return nil
}

func rWhichCommand(args []string) error {
	fs := flag.NewFlagSet("r which", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	script := fs.String("script", "", "script path used to resolve a script-specific rscript setting")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() > 1 {
		return errors.New("usage: rs r which [--script <path>] [dir|script]")
	}

	target := "."
	if fs.NArg() == 1 {
		target = fs.Arg(0)
	}
	startDir, inferredScript, err := resolveProjectTarget(target)
	if err != nil {
		return err
	}
	scriptPath := inferredScript
	if *script != "" {
		scriptPath, err = filepath.Abs(*script)
		if err != nil {
			return fmt.Errorf("resolve script path: %w", err)
		}
	}

	cfg, found, err := project.Discover(startDir)
	if err != nil {
		return fmt.Errorf("discover project config: %w", err)
	}
	configured := ""
	if found {
		if scriptPath != "" {
			resolved, err := cfg.ResolveForScript(scriptPath)
			if err != nil {
				return fmt.Errorf("resolve script config: %w", err)
			}
			if resolved.Rscript != "" {
				configured = resolved.Rscript
			} else {
				configured = resolved.RVersion
			}
		} else {
			if cfg.Defaults.Rscript != "" {
				configured = cfg.Defaults.Rscript
			} else {
				configured = cfg.Defaults.RVersion
			}
		}
	}

	if configured == "" {
		configured = "Rscript"
	}
	rscriptPath, err := rmanager.ResolveVersionOrPath(configured)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, rscriptPath)
	return nil
}

func checkCommand(args []string) error {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to resolve the lockfile environment")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use during validation")
	jsonOutput := fs.Bool("json", false, "print the validation report as JSON")
	verbose := fs.Bool("verbose", false, "print resolved dependency information before validation")
	fs.Var(&packages, "package", "extra CRAN package to include in validation (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include in validation (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include in validation (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from validation (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs check [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Check(runner.CheckOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:   mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:     excludePackages,
		IncludeDeps:     includeCRAN,
		IncludeBiocDeps: includeBioc,
		Repo:            *repo,
		CacheDir:        *cacheDir,
		RscriptPath:     *rscript,
		JSON:            *jsonOutput,
		Verbose:         *verbose,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	})
}

func doctorCommand(args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var packages stringList
	var biocPackages stringList
	var includePackages stringList
	var excludePackages stringList
	repo := fs.String("repo", "", "CRAN mirror used to resolve the environment")
	cacheDir := fs.String("cache-dir", "", "cache directory for managed R libraries")
	rscript := fs.String("rscript", "", "Rscript binary or path to use during diagnosis")
	jsonOutput := fs.Bool("json", false, "print the diagnostic report as JSON")
	strict := fs.Bool("strict", false, "exit non-zero unless doctor status is ok")
	quiet := fs.Bool("quiet", false, "hide informational lines and print only diagnostics plus summary")
	summaryOnly := fs.Bool("summary-only", false, "print only the final doctor summary line")
	verbose := fs.Bool("verbose", false, "print additional environment details")
	fs.Var(&packages, "package", "extra CRAN package to include in diagnosis (repeatable)")
	fs.Var(&packages, "p", "alias for --package")
	fs.Var(&biocPackages, "bioc-package", "extra Bioconductor package to include in diagnosis (repeatable)")
	fs.Var(&includePackages, "include", "extra dependency to include in diagnosis (repeatable)")
	fs.Var(&excludePackages, "exclude", "dependency to exclude from diagnosis (repeatable)")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs doctor [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	includeCRAN, includeBioc := splitIncludedPackages(includePackages)
	return runner.Doctor(runner.DoctorOptions{
		ScriptPath:      scriptPath,
		ExtraDeps:       mergeInitDeps(packages, includeCRAN),
		ExtraBiocDeps:   mergeInitDeps(biocPackages, includeBioc),
		ExcludeDeps:     excludePackages,
		IncludeDeps:     includeCRAN,
		IncludeBiocDeps: includeBioc,
		Repo:            *repo,
		CacheDir:        *cacheDir,
		RscriptPath:     *rscript,
		JSON:            *jsonOutput,
		Strict:          *strict,
		Quiet:           *quiet,
		SummaryOnly:     *summaryOnly,
		Verbose:         *verbose,
		Stdout:          os.Stdout,
		Stderr:          os.Stderr,
	})
}

func scanCommand(args []string) error {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jsonOutput := fs.Bool("json", false, "print detected package dependencies as JSON")
	installable := fs.Bool("installable", false, "filter out R bundled base/recommended packages")

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 1 {
		return errors.New("usage: rs scan [flags] path/to/script.R")
	}

	scriptPath, err := filepath.Abs(fs.Arg(0))
	if err != nil {
		return fmt.Errorf("resolve script path: %w", err)
	}

	deps, err := runner.ScanScript(scriptPath)
	if err != nil {
		return err
	}
	if *installable {
		deps = rdeps.FilterInstallable(deps)
	}

	if *jsonOutput {
		cranDeps, biocDeps := rdeps.SplitBiocPackages(deps)
		report := scanReport{
			Script:          scriptPath,
			Packages:        deps,
			CRANPackages:    cranDeps,
			BiocPackages:    biocDeps,
			InstallableOnly: *installable,
		}
		if report.Packages == nil {
			report.Packages = []string{}
		}
		if report.CRANPackages == nil {
			report.CRANPackages = []string{}
		}
		if report.BiocPackages == nil {
			report.BiocPackages = []string{}
		}
		data, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal scan report: %w", err)
		}
		fmt.Fprintln(os.Stdout, string(data))
		return nil
	}

	for _, dep := range deps {
		fmt.Println(dep)
	}

	return nil
}

func resolveProjectTarget(raw string) (string, string, error) {
	target := raw
	if strings.TrimSpace(target) == "" {
		target = "."
	}
	absTarget, err := filepath.Abs(target)
	if err != nil {
		return "", "", fmt.Errorf("resolve target path: %w", err)
	}
	info, err := os.Stat(absTarget)
	if err != nil {
		return "", "", fmt.Errorf("stat target path: %w", err)
	}
	if info.IsDir() {
		return absTarget, "", nil
	}
	scriptPath := ""
	ext := strings.ToLower(filepath.Ext(absTarget))
	if ext == ".r" || ext == ".rscript" {
		scriptPath = absTarget
	}
	return filepath.Dir(absTarget), scriptPath, nil
}

func printUsage(w *os.File) {
	fmt.Fprint(w, usageText())
}

func usageError() error {
	return errors.New(usageText())
}

func usageText() string {
	return `rs manages a lightweight per-script R library and runs R scripts with automatic dependency installation.

Usage:
  rs init [flags] [dir]
  rs add [flags] <package> [package...]
  rs remove [flags] <package> [package...]
  rs lock [flags] path/to/script.R
  rs r <list|install|use|which> [args]
  rs list [flags] path/to/script.R
  rs prune [flags] [path/to/script.R]
  rs shell [flags] path/to/script.R
  rs exec [flags] -e 'expression' path/to/script.R
  rs cache <dir|ls|rm> [args]
  rs run [flags] path/to/script.R [script args...]
  rs scan [flags] path/to/script.R
  rs sync [flags] path/to/script.R
  rs check [flags] path/to/script.R
  rs doctor [flags] path/to/script.R

Commands:
  init   create a starter rs.toml for an R project, optionally seeded from a script
  add    add CRAN, Bioconductor, or custom source packages to rs.toml
  remove remove packages from rs.toml
  lock   install resolved dependencies and write or refresh the lock file
  r      list, install, select, or inspect managed R interpreters
  list   print the resolved dependency plan for a script without installing
  prune  remove stale managed library directories from the cache
  shell  open an interactive R shell in the script's managed environment
  exec   execute an inline R expression in the script's managed environment
  cache  inspect cache locations and managed library directories
  run    scan dependencies, install missing packages into rs cache, then execute the script
  scan   print detected package dependencies for a script
  sync   alias for lock
  check  validate the current environment against the lock file
  doctor inspect local prerequisites, source configuration, and lockfile presence

Flags for "init":
  --repo <url>              default CRAN mirror to write into rs.toml
  --cache-dir <dir>         cache directory to write into rs.toml
  --lockfile <path>         lockfile path to write into rs.toml
  --rscript <path|cmd>      default Rscript binary or path to write into rs.toml
  --r-version <version>     default R version selector to write into rs.toml
  --from <path>             scan an existing R script and seed rs.toml (repeatable)
  --from-dir <dir>          scan all .R/.Rscript files under a directory (repeatable)
  --include <pkg>           add an extra project-level dependency (repeatable)
  --exclude <pkg>           exclude a detected dependency from generated config (repeatable)
  --bioc-package <pkg>      add an extra project-level Bioconductor dependency (repeatable)
  --write-script-block      with one --from, write detected packages under [scripts."..."]
  --include-bundled         keep R bundled base/recommended packages in generated config
  --force                   overwrite an existing rs.toml

Flags for "add":
  --bioc                    add packages to bioc_packages instead of packages
  --script <path>           add packages to one script profile instead of project defaults
  --project-dir <dir>       start config discovery from this directory
  --source <type>           set a custom source type: github, git, or local
  --github-repo <owner/repo> repository for --source github
  --url <git-url>           repository URL for --source git
  --path <local-path>       local path for --source local
  --ref <ref>               git or GitHub ref for custom source packages
  --subdir <path>           package subdirectory inside a custom source
  --host <host>             GitHub API host for GitHub Enterprise installs
  --token-env <env>         environment variable name holding a GitHub token

Flags for "remove":
  --bioc                    remove packages from bioc_packages instead of packages
  --script <path>           remove packages from one script profile instead of project defaults
  --project-dir <dir>       start config discovery from this directory

Flags for "lock":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved lock plan (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used while locking
  --verbose                 print dependency and cache details before locking

Flags for "r":
  list                      list managed and discovered external R installations
  install <version>         install an R version with the selected manager backend
  use <version|path>        write r_version or a resolved Rscript path to rs.toml
  which [dir|script]        print the currently selected Rscript path

Flags for "list":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved plan (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used while resolving
  --json                    print the resolved dependency plan as JSON

Flags for "prune":
  --project-dir <dir>       prune a project by scanning script files under this directory
  --dry-run                 show which managed libraries would be removed without deleting them

Flags for "shell":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved shell environment (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used for the shell
  --no-install              do not install missing packages
  --locked                  require a valid lockfile but allow installing missing packages without updating it
  --frozen                  require a valid lockfile and installed packages; do not modify dependencies
  --verbose                 print dependency and cache details before opening the shell

Flags for "exec":
  -e, --expr <code>         R expression to execute
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved expression environment (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used for the expression
  --no-install              do not install missing packages
  --locked                  require a valid lockfile but allow installing missing packages without updating it
  --frozen                  require a valid lockfile and installed packages; do not modify dependencies
  --verbose                 print dependency and cache details before executing the expression

Flags for "cache dir":
  accepts an optional path/to/script.R to print the cache root for that script

Flags for "cache ls":
  --project-dir <dir>       project directory used to mark active libraries
  --json                    print cache libraries as JSON
  accepts an optional path/to/script.R to mark active libraries for one script

Flags for "cache rm":
  --script <path>           script path used to resolve the cache root when removing by hash
  --project-dir <dir>       project directory used to resolve the cache root when removing by hash
  --cache-dir <dir>         explicit cache root used when removing by hash
  --dry-run                 show which managed library would be removed without deleting it
  accepts one managed library hash or path under <cache>/lib/

Flags for "scan":
  --json                    print detected package dependencies as JSON
  --installable             filter out R bundled base/recommended packages

Flags for "run":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from the resolved environment (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used for execution
  --no-install              do not install missing packages
  --locked                  require a valid lockfile but allow installing missing packages without updating it
  --frozen                  require a valid lockfile and installed packages; do not modify dependencies
  --verbose                 print dependency and cache details before execution

Flags for "sync":
  same as "lock" (kept as a compatibility alias)

Flags for "check":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN dependency manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor dependency manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from validation (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used during validation
  --json                    print the validation report as JSON
  --verbose                 print dependency and cache details before validation

Flags for "doctor":
  --repo <url>              CRAN mirror to use (defaults to rs.toml or cloud.r-project.org)
  --package, -p <pkg>       add an extra CRAN package manually (repeatable)
  --bioc-package <pkg>      add an extra Bioconductor package manually (repeatable)
  --include <pkg>           add an extra dependency with automatic CRAN/Bioc split (repeatable)
  --exclude <pkg>           exclude a dependency from diagnosis (repeatable)
  --cache-dir <dir>         override cache location (default OS user cache dir)
  --rscript <path|cmd>      override the Rscript interpreter used during diagnosis
  --json                    print the diagnostic report as JSON
  --verbose                 print additional environment details
`
}
