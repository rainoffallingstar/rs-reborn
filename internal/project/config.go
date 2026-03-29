package project

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const ConfigFileName = "rs.toml"

var configKeys = []string{"repo", "cache_dir", "lockfile", "rscript", "packages", "bioc_packages"}
var sourceKeys = []string{"type", "host", "repo", "url", "ref", "path", "subdir", "token_env"}
var sourceTypes = []string{"github", "git", "local"}
var sectionPrefixes = []string{"scripts", "sources"}
var scriptSubsections = []string{"sources"}

type ScriptConfig struct {
	Repo         string
	CacheDir     string
	Lockfile     string
	Rscript      string
	Packages     []string
	BiocPackages []string
	Sources      map[string]SourceSpec
}

type SourceSpec struct {
	Package  string
	Type     string
	Host     string
	Repo     string
	URL      string
	Ref      string
	Path     string
	Subdir   string
	TokenEnv string
}

type Config struct {
	Path    string
	RootDir string

	Defaults ScriptConfig
	Scripts  map[string]ScriptConfig
	Sources  map[string]SourceSpec
	EditMeta EditMetadata
}

type EditMetadata struct {
	Preamble                  []string
	Epilogue                  []string
	TopLevelOrder             []string
	RootKeyOrder              []string
	RootSourceOrder           []string
	ScriptOrder               []string
	ScriptSourceMap           map[string][]string
	ScriptKeyOrder            map[string][]string
	SourceKeyOrder            map[string][]string
	ScriptSourceKey           map[string]map[string][]string
	RootKeyLeadingComments    map[string][]string
	RootKeyTrailingComments   map[string]string
	RootSourceLeadingComments map[string][]string
	RootSourceTrailingComment map[string]string
	ScriptLeadingComments     map[string][]string
	ScriptTrailingComments    map[string]string
	ScriptKeyLeadingComments  map[string][]string
	ScriptKeyTrailingComments map[string]string
	SourceKeyLeadingComments  map[string][]string
	SourceKeyTrailingComments map[string]string
	ScriptSourceLeadingNotes  map[string][]string
	ScriptSourceTrailingNotes map[string]string
	ScriptSourceKeyLeading    map[string][]string
	ScriptSourceKeyTrailing   map[string]string
}

type ResolvedScriptConfig struct {
	Repo         string
	CacheDir     string
	Lockfile     string
	Rscript      string
	Packages     []string
	BiocPackages []string
	Sources      map[string]SourceSpec
	ScriptKey    string
}

func Discover(start string) (Config, bool, error) {
	dir := start
	for {
		candidate := filepath.Join(dir, ConfigFileName)
		info, err := os.Stat(candidate)
		switch {
		case err == nil && !info.IsDir():
			cfg, err := Load(candidate)
			return cfg, true, err
		case err == nil:
			return Config{}, false, fmt.Errorf("%s is a directory, expected a file", candidate)
		case errors.Is(err, os.ErrNotExist):
		default:
			return Config{}, false, err
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return Config{}, false, nil
		}
		dir = parent
	}
}

func Load(path string) (Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}

	cfg, err := Parse(string(content))
	if err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}

	cfg.Path = path
	cfg.RootDir = filepath.Dir(path)
	cfg.Defaults = resolvePaths(cfg.RootDir, cfg.Defaults)
	if len(cfg.Scripts) > 0 {
		resolved := make(map[string]ScriptConfig, len(cfg.Scripts))
		for key, scriptCfg := range cfg.Scripts {
			resolved[key] = resolveScriptPaths(cfg.RootDir, scriptCfg)
		}
		cfg.Scripts = resolved
	}
	if len(cfg.Sources) > 0 {
		resolved := make(map[string]SourceSpec, len(cfg.Sources))
		for key, sourceCfg := range cfg.Sources {
			resolved[key] = resolveSourcePaths(cfg.RootDir, sourceCfg)
		}
		cfg.Sources = resolved
	}
	return cfg, nil
}

func Parse(src string) (Config, error) {
	cfg := Config{
		Scripts: map[string]ScriptConfig{},
		Sources: map[string]SourceSpec{},
		EditMeta: EditMetadata{
			Preamble:                  []string{},
			Epilogue:                  []string{},
			TopLevelOrder:             []string{},
			RootKeyOrder:              []string{},
			RootSourceOrder:           []string{},
			ScriptOrder:               []string{},
			ScriptSourceMap:           map[string][]string{},
			ScriptKeyOrder:            map[string][]string{},
			SourceKeyOrder:            map[string][]string{},
			ScriptSourceKey:           map[string]map[string][]string{},
			RootKeyLeadingComments:    map[string][]string{},
			RootKeyTrailingComments:   map[string]string{},
			RootSourceLeadingComments: map[string][]string{},
			RootSourceTrailingComment: map[string]string{},
			ScriptLeadingComments:     map[string][]string{},
			ScriptTrailingComments:    map[string]string{},
			ScriptKeyLeadingComments:  map[string][]string{},
			ScriptKeyTrailingComments: map[string]string{},
			SourceKeyLeadingComments:  map[string][]string{},
			SourceKeyTrailingComments: map[string]string{},
			ScriptSourceLeadingNotes:  map[string][]string{},
			ScriptSourceTrailingNotes: map[string]string{},
			ScriptSourceKeyLeading:    map[string][]string{},
			ScriptSourceKeyTrailing:   map[string]string{},
		},
	}

	currentScriptKey := ""
	currentSourceKey := ""
	currentScriptSourceKey := ""
	rootSourceLines := map[string]int{}
	scriptSourceLines := map[string]map[string]int{}
	seenRootKeys := map[string]struct{}{}
	seenRootSources := map[string]struct{}{}
	seenScripts := map[string]struct{}{}
	seenScriptSources := map[string]map[string]struct{}{}
	seenScriptKeys := map[string]map[string]struct{}{}
	seenSourceKeys := map[string]map[string]struct{}{}
	seenScriptSourceKeys := map[string]map[string]map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(src))
	beforeContent := true
	pendingComments := []string{}
	for lineNo := 1; scanner.Scan(); lineNo++ {
		rawLine := scanner.Text()
		line, trailingComment := splitLineComment(rawLine)
		line = strings.TrimSpace(line)
		if beforeContent {
			if line == "" {
				cfg.EditMeta.Preamble = append(cfg.EditMeta.Preamble, rawLine)
				continue
			}
			beforeContent = false
		}
		if line == "" {
			pendingComments = append(pendingComments, rawLine)
			continue
		}

		if strings.HasPrefix(line, "[") {
			scriptKey, sourceKey, scriptSourceKey, err := parseSection(line)
			if err != nil {
				return Config{}, fmt.Errorf("line %d: %w", lineNo, err)
			}
			currentScriptKey = scriptKey
			currentSourceKey = sourceKey
			currentScriptSourceKey = scriptSourceKey
			switch {
			case sourceKey != "":
				cfg.EditMeta.TopLevelOrder = appendUnique(cfg.EditMeta.TopLevelOrder, topLevelEntrySource(sourceKey))
				if _, exists := seenRootSources[sourceKey]; exists {
					return Config{}, fmt.Errorf("line %d: %s is declared more than once", lineNo, rootSourceSectionName(sourceKey))
				}
				seenRootSources[sourceKey] = struct{}{}
				rootSourceLines[sourceKey] = lineNo
				cfg.EditMeta.RootSourceOrder = appendUnique(cfg.EditMeta.RootSourceOrder, sourceKey)
				cfg.EditMeta.RootSourceLeadingComments[sourceKey] = append([]string(nil), pendingComments...)
				cfg.EditMeta.RootSourceTrailingComment[sourceKey] = trailingComment
			case scriptKey != "" && scriptSourceKey == "":
				cfg.EditMeta.TopLevelOrder = appendUnique(cfg.EditMeta.TopLevelOrder, topLevelEntryScript(scriptKey))
				if _, exists := seenScripts[scriptKey]; exists {
					return Config{}, fmt.Errorf("line %d: %s is declared more than once", lineNo, scriptSectionName(scriptKey))
				}
				seenScripts[scriptKey] = struct{}{}
				cfg.EditMeta.ScriptOrder = appendUnique(cfg.EditMeta.ScriptOrder, scriptKey)
				cfg.EditMeta.ScriptLeadingComments[scriptKey] = append([]string(nil), pendingComments...)
				cfg.EditMeta.ScriptTrailingComments[scriptKey] = trailingComment
			case scriptKey != "" && scriptSourceKey != "":
				cfg.EditMeta.TopLevelOrder = appendUnique(cfg.EditMeta.TopLevelOrder, topLevelEntryScript(scriptKey))
				if seenScriptSources[scriptKey] == nil {
					seenScriptSources[scriptKey] = map[string]struct{}{}
				}
				if _, exists := seenScriptSources[scriptKey][scriptSourceKey]; exists {
					return Config{}, fmt.Errorf("line %d: %s is declared more than once", lineNo, scriptSourceSectionName(scriptKey, scriptSourceKey))
				}
				seenScriptSources[scriptKey][scriptSourceKey] = struct{}{}
				if scriptSourceLines[scriptKey] == nil {
					scriptSourceLines[scriptKey] = map[string]int{}
				}
				scriptSourceLines[scriptKey][scriptSourceKey] = lineNo
				cfg.EditMeta.ScriptOrder = appendUnique(cfg.EditMeta.ScriptOrder, scriptKey)
				cfg.EditMeta.ScriptSourceMap[scriptKey] = appendUnique(cfg.EditMeta.ScriptSourceMap[scriptKey], scriptSourceKey)
				cfg.EditMeta.ScriptSourceLeadingNotes[commentPathKey(scriptKey, scriptSourceKey)] = append([]string(nil), pendingComments...)
				cfg.EditMeta.ScriptSourceTrailingNotes[commentPathKey(scriptKey, scriptSourceKey)] = trailingComment
			}
			pendingComments = nil
			if scriptKey != "" {
				if _, ok := cfg.Scripts[scriptKey]; !ok {
					cfg.Scripts[scriptKey] = ScriptConfig{}
				}
			}
			if scriptKey != "" && scriptSourceKey != "" {
				scriptCfg := cfg.Scripts[scriptKey]
				if scriptCfg.Sources == nil {
					scriptCfg.Sources = map[string]SourceSpec{}
				}
				if _, ok := scriptCfg.Sources[scriptSourceKey]; !ok {
					scriptCfg.Sources[scriptSourceKey] = SourceSpec{Package: scriptSourceKey}
				}
				cfg.Scripts[scriptKey] = scriptCfg
			}
			if sourceKey != "" {
				if _, ok := cfg.Sources[sourceKey]; !ok {
					cfg.Sources[sourceKey] = SourceSpec{Package: sourceKey}
				}
			}
			continue
		}

		key, rawValue, ok := strings.Cut(line, "=")
		if !ok {
			return Config{}, fmt.Errorf("line %d: %s: expected key = value", lineNo, currentSectionLabel(currentScriptKey, currentSourceKey, currentScriptSourceKey))
		}
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(rawValue)

		if currentSourceKey != "" {
			if seenSourceKeys[currentSourceKey] == nil {
				seenSourceKeys[currentSourceKey] = map[string]struct{}{}
			}
			if _, exists := seenSourceKeys[currentSourceKey][trimmedKey]; exists {
				return Config{}, fmt.Errorf("line %d: duplicate key %q in %s", lineNo, trimmedKey, rootSourceSectionName(currentSourceKey))
			}
			seenSourceKeys[currentSourceKey][trimmedKey] = struct{}{}
			cfg.EditMeta.SourceKeyOrder[currentSourceKey] = appendUnique(cfg.EditMeta.SourceKeyOrder[currentSourceKey], trimmedKey)
			cfg.EditMeta.SourceKeyLeadingComments[commentPathKey(currentSourceKey, trimmedKey)] = append([]string(nil), pendingComments...)
			cfg.EditMeta.SourceKeyTrailingComments[commentPathKey(currentSourceKey, trimmedKey)] = trailingComment
			pendingComments = nil
			if err := assignSourceValue(&cfg, currentSourceKey, trimmedKey, trimmedValue); err != nil {
				return Config{}, fmt.Errorf("line %d: %s: %w", lineNo, rootSourceSectionName(currentSourceKey), err)
			}
			continue
		}
		if currentScriptSourceKey != "" {
			if seenScriptSourceKeys[currentScriptKey] == nil {
				seenScriptSourceKeys[currentScriptKey] = map[string]map[string]struct{}{}
			}
			if seenScriptSourceKeys[currentScriptKey][currentScriptSourceKey] == nil {
				seenScriptSourceKeys[currentScriptKey][currentScriptSourceKey] = map[string]struct{}{}
			}
			if _, exists := seenScriptSourceKeys[currentScriptKey][currentScriptSourceKey][trimmedKey]; exists {
				return Config{}, fmt.Errorf("line %d: duplicate key %q in %s", lineNo, trimmedKey, scriptSourceSectionName(currentScriptKey, currentScriptSourceKey))
			}
			seenScriptSourceKeys[currentScriptKey][currentScriptSourceKey][trimmedKey] = struct{}{}
			if cfg.EditMeta.ScriptSourceKey[currentScriptKey] == nil {
				cfg.EditMeta.ScriptSourceKey[currentScriptKey] = map[string][]string{}
			}
			cfg.EditMeta.ScriptSourceKey[currentScriptKey][currentScriptSourceKey] = appendUnique(cfg.EditMeta.ScriptSourceKey[currentScriptKey][currentScriptSourceKey], trimmedKey)
			cfg.EditMeta.ScriptSourceKeyLeading[commentPathKey(currentScriptKey, currentScriptSourceKey, trimmedKey)] = append([]string(nil), pendingComments...)
			cfg.EditMeta.ScriptSourceKeyTrailing[commentPathKey(currentScriptKey, currentScriptSourceKey, trimmedKey)] = trailingComment
			pendingComments = nil
			if err := assignScriptSourceValue(&cfg, currentScriptKey, currentScriptSourceKey, trimmedKey, trimmedValue); err != nil {
				return Config{}, fmt.Errorf("line %d: %s: %w", lineNo, scriptSourceSectionName(currentScriptKey, currentScriptSourceKey), err)
			}
			continue
		}

		if currentScriptKey == "" {
			if _, exists := seenRootKeys[trimmedKey]; exists {
				return Config{}, fmt.Errorf("line %d: duplicate key %q in %s", lineNo, trimmedKey, rootConfigSectionName())
			}
			seenRootKeys[trimmedKey] = struct{}{}
			cfg.EditMeta.RootKeyOrder = appendUnique(cfg.EditMeta.RootKeyOrder, trimmedKey)
			cfg.EditMeta.RootKeyLeadingComments[trimmedKey] = append([]string(nil), pendingComments...)
			cfg.EditMeta.RootKeyTrailingComments[trimmedKey] = trailingComment
		} else {
			if seenScriptKeys[currentScriptKey] == nil {
				seenScriptKeys[currentScriptKey] = map[string]struct{}{}
			}
			if _, exists := seenScriptKeys[currentScriptKey][trimmedKey]; exists {
				return Config{}, fmt.Errorf("line %d: duplicate key %q in %s", lineNo, trimmedKey, scriptSectionName(currentScriptKey))
			}
			seenScriptKeys[currentScriptKey][trimmedKey] = struct{}{}
			cfg.EditMeta.ScriptKeyOrder[currentScriptKey] = appendUnique(cfg.EditMeta.ScriptKeyOrder[currentScriptKey], trimmedKey)
			cfg.EditMeta.ScriptKeyLeadingComments[commentPathKey(currentScriptKey, trimmedKey)] = append([]string(nil), pendingComments...)
			cfg.EditMeta.ScriptKeyTrailingComments[commentPathKey(currentScriptKey, trimmedKey)] = trailingComment
		}
		pendingComments = nil
		if err := assignLineValue(&cfg, currentScriptKey, trimmedKey, trimmedValue); err != nil {
			if currentScriptKey == "" {
				return Config{}, fmt.Errorf("line %d: %s: %w", lineNo, rootConfigSectionName(), err)
			}
			return Config{}, fmt.Errorf("line %d: %s: %w", lineNo, scriptSectionName(currentScriptKey), err)
		}
	}

	if err := scanner.Err(); err != nil {
		return Config{}, err
	}
	if len(pendingComments) > 0 {
		cfg.EditMeta.Epilogue = append([]string(nil), pendingComments...)
	}
	if err := validateParsedSources(cfg, rootSourceLines, scriptSourceLines); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func commentPathKey(parts ...string) string {
	return strings.Join(parts, "\x00")
}

func validateParsedSources(cfg Config, rootSourceLines map[string]int, scriptSourceLines map[string]map[string]int) error {
	for sourceName, spec := range cfg.Sources {
		if err := validateSourceSpecInSection(rootSourceSectionName(sourceName), spec); err != nil {
			return wrapSectionValidationError(rootSourceLines[sourceName], err)
		}
	}
	for scriptName, scriptCfg := range cfg.Scripts {
		for sourceName, spec := range scriptCfg.Sources {
			if err := validateSourceSpecInSection(scriptSourceSectionName(scriptName, sourceName), spec); err != nil {
				lineNo := 0
				if scriptSourceLines[scriptName] != nil {
					lineNo = scriptSourceLines[scriptName][sourceName]
				}
				return wrapSectionValidationError(lineNo, err)
			}
		}
	}
	return nil
}

func wrapSectionValidationError(lineNo int, err error) error {
	if err == nil {
		return nil
	}
	if lineNo <= 0 {
		return err
	}
	return fmt.Errorf("line %d: %w", lineNo, err)
}

func rootSourceSectionName(sourceName string) string {
	return "[sources." + quoteString(sourceName) + "]"
}

func rootConfigSectionName() string {
	return "root config"
}

func scriptSectionName(scriptName string) string {
	return "[scripts." + quoteString(scriptName) + "]"
}

func scriptSourceSectionName(scriptName, sourceName string) string {
	return "[scripts." + quoteString(scriptName) + ".sources." + quoteString(sourceName) + "]"
}

func topLevelEntrySource(sourceName string) string {
	return "source\x00" + sourceName
}

func topLevelEntryScript(scriptName string) string {
	return "script\x00" + scriptName
}

func currentSectionLabel(scriptName, sourceName, scriptSourceName string) string {
	switch {
	case sourceName != "":
		return rootSourceSectionName(sourceName)
	case scriptName != "" && scriptSourceName != "":
		return scriptSourceSectionName(scriptName, scriptSourceName)
	case scriptName != "":
		return scriptSectionName(scriptName)
	default:
		return rootConfigSectionName()
	}
}

func (c Config) ResolveForScript(scriptPath string) (ResolvedScriptConfig, error) {
	resolved := ResolvedScriptConfig{
		Repo:         c.Defaults.Repo,
		CacheDir:     c.Defaults.CacheDir,
		Lockfile:     c.Defaults.Lockfile,
		Rscript:      c.Defaults.Rscript,
		Packages:     append([]string(nil), c.Defaults.Packages...),
		BiocPackages: append([]string(nil), c.Defaults.BiocPackages...),
		Sources:      cloneSourceMap(c.Sources),
	}

	if c.RootDir == "" || len(c.Scripts) == 0 {
		return resolved, nil
	}

	rel, err := filepath.Rel(c.RootDir, scriptPath)
	if err != nil {
		return ResolvedScriptConfig{}, fmt.Errorf("resolve script path relative to project: %w", err)
	}
	rel = filepath.ToSlash(rel)

	if scriptCfg, ok := c.Scripts[rel]; ok {
		mergeScriptConfig(&resolved, rel, scriptCfg)
		return resolved, nil
	}
	return resolved, nil
}

func mergeScriptConfig(dst *ResolvedScriptConfig, key string, src ScriptConfig) {
	if src.Repo != "" {
		dst.Repo = src.Repo
	}
	if src.CacheDir != "" {
		dst.CacheDir = src.CacheDir
	}
	if src.Lockfile != "" {
		dst.Lockfile = src.Lockfile
	}
	if src.Rscript != "" {
		dst.Rscript = src.Rscript
	}
	dst.Packages = mergeStrings(dst.Packages, src.Packages)
	dst.BiocPackages = mergeStrings(dst.BiocPackages, src.BiocPackages)
	dst.Sources = mergeSourceMaps(dst.Sources, src.Sources)
	dst.ScriptKey = key
}

func resolvePaths(root string, cfg ScriptConfig) ScriptConfig {
	if cfg.CacheDir != "" && !filepath.IsAbs(cfg.CacheDir) {
		cfg.CacheDir = filepath.Join(root, cfg.CacheDir)
	}
	if cfg.Lockfile != "" && !filepath.IsAbs(cfg.Lockfile) {
		cfg.Lockfile = filepath.Join(root, cfg.Lockfile)
	}
	cfg.Rscript = resolveCommandPath(root, cfg.Rscript)
	return cfg
}

func resolveCommandPath(root, value string) string {
	if value == "" || filepath.IsAbs(value) || !looksLikePath(value) {
		return value
	}
	return filepath.Join(root, value)
}

func looksLikePath(value string) bool {
	return strings.ContainsAny(value, `/\`)
}

func resolveScriptPaths(root string, cfg ScriptConfig) ScriptConfig {
	cfg = resolvePaths(root, cfg)
	if len(cfg.Sources) > 0 {
		resolved := make(map[string]SourceSpec, len(cfg.Sources))
		for key, sourceCfg := range cfg.Sources {
			resolved[key] = resolveSourcePaths(root, sourceCfg)
		}
		cfg.Sources = resolved
	}
	return cfg
}

func resolveSourcePaths(root string, spec SourceSpec) SourceSpec {
	if spec.Path != "" && !filepath.IsAbs(spec.Path) {
		spec.Path = filepath.Join(root, spec.Path)
	}
	return spec
}

func parseSection(line string) (string, string, string, error) {
	if strings.HasPrefix(line, "[[") {
		return "", "", "", arrayStyleSectionHeaderError(line)
	}
	if !strings.HasSuffix(line, "]") {
		return "", "", "", fmt.Errorf("invalid section header %q: missing closing ]", line)
	}
	name := strings.TrimSpace(line[1 : len(line)-1])
	switch {
	case strings.HasPrefix(name, "scripts."):
		scriptKey, sourceKey, err := parseScriptSection(strings.TrimPrefix(name, "scripts."))
		if err != nil {
			return "", "", "", fmt.Errorf("invalid script section %q: %w", line, err)
		}
		if scriptKey == "" {
			return "", "", "", fmt.Errorf("empty script section %q", line)
		}
		return filepath.ToSlash(scriptKey), "", sourceKey, nil
	case strings.HasPrefix(name, "sources."):
		sourceKey, err := parseString(strings.TrimPrefix(name, "sources."))
		if err != nil {
			return "", "", "", fmt.Errorf("invalid source section %q: %w", line, err)
		}
		if sourceKey == "" {
			return "", "", "", fmt.Errorf("empty source section %q", line)
		}
		return "", sourceKey, "", nil
	default:
		return "", "", "", unsupportedSectionError(line, name)
	}
}

func arrayStyleSectionHeaderError(line string) error {
	msg := fmt.Sprintf("array-style section headers are not supported %q", line)
	if strings.HasSuffix(line, "]]") {
		msg += fmt.Sprintf("; use %q instead", line[1:len(line)-1])
	}
	return fmt.Errorf("%s", msg)
}

func parseScriptSection(raw string) (string, string, error) {
	scriptKey, rest, err := parseQuotedPathPrefix(raw)
	if err != nil {
		return "", "", err
	}
	if rest == "" {
		return scriptKey, "", nil
	}
	if !strings.HasPrefix(rest, ".sources.") {
		return "", "", unsupportedScriptSubsectionError(rest)
	}
	sourceKey, err := parseString(strings.TrimPrefix(rest, ".sources."))
	if err != nil {
		return "", "", err
	}
	return scriptKey, sourceKey, nil
}

func parseQuotedPathPrefix(raw string) (string, string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", fmt.Errorf("empty path")
	}

	first := raw[0]
	if first != '"' && first != '\'' {
		return "", "", fmt.Errorf("expected quoted path in %q", raw)
	}

	i := 1
	escape := false
	for i < len(raw) {
		ch := raw[i]
		switch {
		case escape:
			escape = false
		case ch == '\\':
			escape = true
		case ch == first:
			quoted := raw[:i+1]
			value, err := parseString(quoted)
			if err != nil {
				return "", "", err
			}
			return value, raw[i+1:], nil
		}
		i++
	}
	return "", "", fmt.Errorf("unterminated quoted path in %q", raw)
}

func assignSourceValue(cfg *Config, sourceKey, key, rawValue string) error {
	spec := cfg.Sources[sourceKey]
	spec.Package = sourceKey
	if err := assignSourceSpecValue(&spec, key, rawValue); err != nil {
		return err
	}

	cfg.Sources[sourceKey] = spec
	return nil
}

func assignScriptSourceValue(cfg *Config, scriptKey, sourceKey, key, rawValue string) error {
	scriptCfg := cfg.Scripts[scriptKey]
	if scriptCfg.Sources == nil {
		scriptCfg.Sources = map[string]SourceSpec{}
	}
	spec := scriptCfg.Sources[sourceKey]
	spec.Package = sourceKey

	if err := assignSourceSpecValue(&spec, key, rawValue); err != nil {
		return err
	}

	scriptCfg.Sources[sourceKey] = spec
	cfg.Scripts[scriptKey] = scriptCfg
	return nil
}

func cloneSourceMap(in map[string]SourceSpec) map[string]SourceSpec {
	if len(in) == 0 {
		return nil
	}

	out := make(map[string]SourceSpec, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func mergeSourceMaps(base, overlay map[string]SourceSpec) map[string]SourceSpec {
	switch {
	case len(base) == 0 && len(overlay) == 0:
		return nil
	case len(base) == 0:
		return cloneSourceMap(overlay)
	case len(overlay) == 0:
		return cloneSourceMap(base)
	}

	merged := cloneSourceMap(base)
	for key, value := range overlay {
		merged[key] = value
	}
	return merged
}

func assignLineValue(cfg *Config, scriptKey, key, rawValue string) error {
	target := &cfg.Defaults
	if scriptKey != "" {
		scriptCfg := cfg.Scripts[scriptKey]
		target = &scriptCfg
		defer func() {
			cfg.Scripts[scriptKey] = *target
		}()
	}

	switch key {
	case "repo":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		target.Repo = value
	case "cache_dir":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		target.CacheDir = value
	case "lockfile":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		target.Lockfile = value
	case "rscript":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		target.Rscript = value
	case "packages":
		value, err := parseStringArrayValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		target.Packages = value
	case "bioc_packages":
		value, err := parseStringArrayValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		target.BiocPackages = value
	default:
		return unsupportedKeyError("key", key, configKeys)
	}

	return nil
}

func assignSourceSpecValue(spec *SourceSpec, key, rawValue string) error {
	switch key {
	case "type":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.Type = value
	case "repo":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.Repo = value
	case "url":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.URL = value
	case "host":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.Host = value
	case "ref":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.Ref = value
	case "path":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.Path = value
	case "subdir":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.Subdir = value
	case "token_env":
		value, err := parseStringValueForKey(key, rawValue)
		if err != nil {
			return err
		}
		spec.TokenEnv = value
	default:
		return unsupportedKeyError("source key", key, sourceKeys)
	}
	return nil
}

func unsupportedKeyError(kind, key string, allowed []string) error {
	return fmt.Errorf("%s", unsupportedNameMessage("unsupported "+kind, key, "supported keys", allowed))
}

func unsupportedValueError(kind, value string, allowed []string) error {
	return fmt.Errorf("%s", unsupportedNameMessage("unsupported "+kind, value, "supported values", allowed))
}

func unsupportedSectionError(line, name string) error {
	prefix := name
	if idx := strings.Index(prefix, "."); idx >= 0 {
		prefix = prefix[:idx]
	}
	msg := unsupportedNameMessage("unsupported section", line, "supported section prefixes", sectionPrefixes)
	if suggestion := nearestSupportedValue(prefix, sectionPrefixes); suggestion != "" {
		msg += fmt.Sprintf("; did you mean %q?", "["+suggestion+name[len(prefix):]+"]")
	}
	return fmt.Errorf("%s", msg)
}

func unsupportedScriptSubsectionError(rest string) error {
	subsection := strings.TrimPrefix(rest, ".")
	if idx := strings.Index(subsection, "."); idx >= 0 {
		subsection = subsection[:idx]
	}
	msg := unsupportedNameMessage("unsupported script subsection", rest, "supported subsections", []string{".sources."})
	if suggestion := nearestSupportedValue(subsection, scriptSubsections); suggestion != "" {
		msg += fmt.Sprintf("; did you mean %q?", "."+suggestion+".")
	}
	return fmt.Errorf("%s", msg)
}

func unsupportedNameMessage(kind, value, allowedLabel string, allowed []string) string {
	msg := fmt.Sprintf("%s %q; %s: %s", kind, value, allowedLabel, strings.Join(allowed, ", "))
	if suggestion := nearestSupportedValue(value, allowed); suggestion != "" {
		msg += fmt.Sprintf("; did you mean %q?", suggestion)
	}
	return msg
}

func nearestSupportedValue(value string, allowed []string) string {
	best := ""
	bestDistance := 0
	for _, candidate := range allowed {
		distance := editDistance(value, candidate)
		if best == "" || distance < bestDistance {
			best = candidate
			bestDistance = distance
		}
	}
	if best == "" {
		return ""
	}
	if bestDistance > 2 {
		return ""
	}
	return best
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	curr := make([]int, len(b)+1)
	for j := 0; j <= len(b); j++ {
		prev[j] = j
	}

	for i := 1; i <= len(a); i++ {
		curr[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 0
			if a[i-1] != b[j-1] {
				cost = 1
			}
			curr[j] = min3(
				prev[j]+1,
				curr[j-1]+1,
				prev[j-1]+cost,
			)
		}
		prev, curr = curr, prev
	}
	return prev[len(b)]
}

func min3(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}

func parseStringValueForKey(key, rawValue string) (string, error) {
	value, err := parseString(rawValue)
	if err != nil {
		return "", fmt.Errorf("invalid value for %q: %w", key, err)
	}
	return value, nil
}

func parseStringArrayValueForKey(key, rawValue string) ([]string, error) {
	value, err := parseStringArray(rawValue)
	if err != nil {
		return nil, fmt.Errorf("invalid value for %q: %w", key, err)
	}
	return value, nil
}

func stripComment(line string) string {
	content, _ := splitLineComment(line)
	return content
}

func splitLineComment(line string) (string, string) {
	inString := false
	quote := rune(0)
	escape := false
	runes := []rune(line)

	for i, r := range runes {
		switch {
		case escape:
			escape = false
		case inString && r == '\\':
			escape = true
		case inString && r == quote:
			inString = false
			quote = 0
		case !inString && (r == '"' || r == '\''):
			inString = true
			quote = r
		case !inString && r == '#':
			start := i
			for start > 0 && (runes[start-1] == ' ' || runes[start-1] == '\t') {
				start--
			}
			return string(runes[:start]), string(runes[start:])
		}
	}

	return line, ""
}

func parseString(raw string) (string, error) {
	value, err := strconv.Unquote(raw)
	if err == nil {
		return value, nil
	}

	if strings.HasPrefix(raw, "\"") || strings.HasPrefix(raw, "'") {
		return "", fmt.Errorf("invalid quoted string %q", raw)
	}

	return raw, nil
}

func parseStringArray(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[") || !strings.HasSuffix(raw, "]") {
		return nil, fmt.Errorf("expected [\"pkg\"] array, got %q", raw)
	}

	body := strings.TrimSpace(raw[1 : len(raw)-1])
	if body == "" {
		return nil, nil
	}

	parts := splitArrayValues(body)
	values := make([]string, 0, len(parts))
	for _, part := range parts {
		value, err := parseString(strings.TrimSpace(part))
		if err != nil {
			return nil, err
		}
		if value != "" {
			values = append(values, value)
		}
	}
	return values, nil
}

func splitArrayValues(body string) []string {
	var parts []string
	start := 0
	inString := false
	quote := byte(0)
	escape := false

	for i := 0; i < len(body); i++ {
		ch := body[i]
		switch {
		case escape:
			escape = false
		case inString && ch == '\\':
			escape = true
		case inString && ch == quote:
			inString = false
			quote = 0
		case !inString && (ch == '"' || ch == '\''):
			inString = true
			quote = ch
		case !inString && ch == ',':
			parts = append(parts, body[start:i])
			start = i + 1
		}
	}
	parts = append(parts, body[start:])
	return parts
}

func mergeStrings(groups ...[]string) []string {
	seen := map[string]struct{}{}
	merged := make([]string, 0)
	for _, group := range groups {
		for _, value := range group {
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			merged = append(merged, value)
		}
	}
	return merged
}
