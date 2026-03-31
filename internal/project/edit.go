package project

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

const DefaultRepo = "https://cloud.r-project.org"

type InitOptions struct {
	Repo              string
	CacheDir          string
	Lockfile          string
	Rscript           string
	RVersion          string
	ToolchainPrefixes []string
	PkgConfigPath     []string
	Packages          []string
	BiocPackages      []string
}

type AddPackageOptions struct {
	ScriptPath string
	Package    string
	Bioc       bool
	Source     *SourceSpec
}

type RemovePackageOptions struct {
	ScriptPath string
	Package    string
	Bioc       bool
}

func NewDefaultConfig(opts InitOptions) Config {
	return Config{
		Defaults: ScriptConfig{
			Repo:              firstNonEmpty(opts.Repo, DefaultRepo),
			CacheDir:          firstNonEmpty(opts.CacheDir, ".rs-cache"),
			Lockfile:          firstNonEmpty(opts.Lockfile, "rs.lock.json"),
			Rscript:           opts.Rscript,
			RVersion:          opts.RVersion,
			ToolchainPrefixes: mergeStrings(nil, opts.ToolchainPrefixes),
			PkgConfigPath:     mergeStrings(nil, opts.PkgConfigPath),
			Packages:          mergeStrings(nil, opts.Packages),
			BiocPackages:      mergeStrings(nil, opts.BiocPackages),
		},
		Scripts: map[string]ScriptConfig{},
		Sources: map[string]SourceSpec{},
	}
}

func NewConfigFromScript(opts InitOptions, rootDir string, scriptPath string, writeScriptBlock bool) (Config, error) {
	if scriptPath == "" {
		return NewDefaultConfig(opts), nil
	}
	return NewConfigFromScripts(InitOptions{
		Repo:              opts.Repo,
		CacheDir:          opts.CacheDir,
		Lockfile:          opts.Lockfile,
		Rscript:           opts.Rscript,
		RVersion:          opts.RVersion,
		ToolchainPrefixes: opts.ToolchainPrefixes,
		PkgConfigPath:     opts.PkgConfigPath,
	}, rootDir, map[string]ScriptConfig{
		scriptPath: {
			Packages:     mergeStrings(nil, opts.Packages),
			BiocPackages: mergeStrings(nil, opts.BiocPackages),
		},
	}, writeScriptBlock)
}

func NewConfigFromScripts(opts InitOptions, rootDir string, scriptConfigs map[string]ScriptConfig, writeScriptBlock bool) (Config, error) {
	cfg := NewDefaultConfig(InitOptions{
		Repo:              opts.Repo,
		CacheDir:          opts.CacheDir,
		Lockfile:          opts.Lockfile,
		Rscript:           opts.Rscript,
		RVersion:          opts.RVersion,
		ToolchainPrefixes: opts.ToolchainPrefixes,
		PkgConfigPath:     opts.PkgConfigPath,
		Packages:          opts.Packages,
		BiocPackages:      opts.BiocPackages,
	})

	if len(scriptConfigs) == 0 {
		return cfg, nil
	}

	if !writeScriptBlock && len(scriptConfigs) == 1 {
		for _, scriptCfg := range scriptConfigs {
			cfg.Defaults.Packages = mergeStrings(cfg.Defaults.Packages, scriptCfg.Packages)
			cfg.Defaults.BiocPackages = mergeStrings(cfg.Defaults.BiocPackages, scriptCfg.BiocPackages)
		}
		return cfg, nil
	}
	if rootDir == "" {
		return Config{}, fmt.Errorf("root dir is required when writeScriptBlock is enabled")
	}

	for scriptPath, scriptCfg := range scriptConfigs {
		if scriptPath == "" {
			return Config{}, fmt.Errorf("script path is required when writeScriptBlock is enabled")
		}
		rel, err := filepath.Rel(rootDir, scriptPath)
		if err != nil {
			return Config{}, fmt.Errorf("resolve script path relative to project: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return Config{}, fmt.Errorf("script %s is outside project root %s", scriptPath, rootDir)
		}
		cfg.Scripts[rel] = ScriptConfig{
			Packages:     mergeStrings(nil, scriptCfg.Packages),
			BiocPackages: mergeStrings(nil, scriptCfg.BiocPackages),
		}
	}
	return cfg, nil
}

func LoadEditable(path string) (Config, error) {
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
	return cfg, nil
}

func Save(path string, cfg Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(Render(cfg)), 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

func Render(cfg Config) string {
	body := renderConfigBody(cfg)
	lines := append([]string(nil), cfg.EditMeta.Preamble...)
	if shouldInsertPreambleSeparator(lines, body) {
		lines = append(lines, "")
	}
	lines = append(lines, body...)
	appendRawLines(&lines, cfg.EditMeta.Epilogue)

	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func renderConfigBody(cfg Config) []string {
	var lines []string

	appendScriptConfigLinesWithOrder(&lines, cfg.Defaults, cfg.EditMeta.RootKeyOrder,
		func(key string) []string { return cfg.EditMeta.RootKeyLeadingComments[key] },
		func(key string) string { return cfg.EditMeta.RootKeyTrailingComments[key] },
	)

	topLevelEntries := orderedTopLevelEntries(cfg)
	for i, entry := range topLevelEntries {
		if len(lines) > 0 && i == 0 {
			lines = append(lines, "")
		}
		if i > 0 {
			lines = append(lines, "")
		}
		switch {
		case strings.HasPrefix(entry, "source\x00"):
			renderRootSourceBlock(&lines, cfg, strings.TrimPrefix(entry, "source\x00"))
		case strings.HasPrefix(entry, "script\x00"):
			renderScriptBlock(&lines, cfg, strings.TrimPrefix(entry, "script\x00"))
		}
	}

	return lines
}

func orderedTopLevelEntries(cfg Config) []string {
	rootSources := orderedNames(sortedSourceNames(cfg.Sources), cfg.EditMeta.RootSourceOrder)
	scriptNames := orderedNames(sortedScriptNames(cfg.Scripts), cfg.EditMeta.ScriptOrder)

	alive := make(map[string]struct{}, len(rootSources)+len(scriptNames))
	var defaults []string
	for _, name := range rootSources {
		entry := topLevelEntrySource(name)
		alive[entry] = struct{}{}
		defaults = append(defaults, entry)
	}
	for _, name := range scriptNames {
		if scriptConfigEmpty(cfg.Scripts[name]) {
			continue
		}
		entry := topLevelEntryScript(name)
		alive[entry] = struct{}{}
		defaults = append(defaults, entry)
	}

	if len(alive) == 0 {
		return nil
	}

	var entries []string
	for _, entry := range cfg.EditMeta.TopLevelOrder {
		if _, ok := alive[entry]; !ok {
			continue
		}
		entries = append(entries, entry)
		delete(alive, entry)
	}
	for _, entry := range defaults {
		if _, ok := alive[entry]; !ok {
			continue
		}
		entries = append(entries, entry)
	}
	return entries
}

func renderRootSourceBlock(lines *[]string, cfg Config, name string) {
	appendRawLines(lines, cfg.EditMeta.RootSourceLeadingComments[name])
	*lines = append(*lines, fmt.Sprintf("[sources.%s]%s", quoteString(name), cfg.EditMeta.RootSourceTrailingComment[name]))
	appendSourceSpecLinesWithOrder(lines, cfg.Sources[name], cfg.EditMeta.SourceKeyOrder[name],
		func(key string) []string { return cfg.EditMeta.SourceKeyLeadingComments[commentPathKey(name, key)] },
		func(key string) string { return cfg.EditMeta.SourceKeyTrailingComments[commentPathKey(name, key)] },
	)
}

func renderScriptBlock(lines *[]string, cfg Config, name string) {
	scriptCfg := cfg.Scripts[name]
	appendRawLines(lines, cfg.EditMeta.ScriptLeadingComments[name])
	*lines = append(*lines, fmt.Sprintf("[scripts.%s]%s", quoteString(name), cfg.EditMeta.ScriptTrailingComments[name]))
	appendScriptConfigLinesWithOrder(lines, scriptCfg, cfg.EditMeta.ScriptKeyOrder[name],
		func(key string) []string { return cfg.EditMeta.ScriptKeyLeadingComments[commentPathKey(name, key)] },
		func(key string) string { return cfg.EditMeta.ScriptKeyTrailingComments[commentPathKey(name, key)] },
	)

	scriptSources := orderedNames(sortedSourceNames(scriptCfg.Sources), cfg.EditMeta.ScriptSourceMap[name])
	for _, sourceName := range scriptSources {
		*lines = append(*lines, "")
		appendRawLines(lines, cfg.EditMeta.ScriptSourceLeadingNotes[commentPathKey(name, sourceName)])
		*lines = append(*lines, fmt.Sprintf("[scripts.%s.sources.%s]%s", quoteString(name), quoteString(sourceName), cfg.EditMeta.ScriptSourceTrailingNotes[commentPathKey(name, sourceName)]))
		appendSourceSpecLinesWithOrder(lines, scriptCfg.Sources[sourceName], cfg.EditMeta.ScriptSourceKey[name][sourceName],
			func(key string) []string {
				return cfg.EditMeta.ScriptSourceKeyLeading[commentPathKey(name, sourceName, key)]
			},
			func(key string) string {
				return cfg.EditMeta.ScriptSourceKeyTrailing[commentPathKey(name, sourceName, key)]
			},
		)
	}
}

func AddPackage(cfg *Config, opts AddPackageOptions) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if opts.Package == "" {
		return fmt.Errorf("package name is required")
	}
	if opts.Bioc && opts.Source != nil {
		return fmt.Errorf("a package cannot be both Bioconductor and a custom source")
	}
	if opts.Source != nil {
		spec := *opts.Source
		spec.Package = opts.Package
		if err := validateSourceSpec(spec); err != nil {
			return err
		}
	}

	target := &cfg.Defaults
	targetSources := &cfg.Sources
	if opts.ScriptPath != "" {
		if cfg.RootDir == "" {
			return fmt.Errorf("config root is unknown")
		}
		rel, err := filepath.Rel(cfg.RootDir, opts.ScriptPath)
		if err != nil {
			return fmt.Errorf("resolve script path relative to project: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("script %s is outside project root %s", opts.ScriptPath, cfg.RootDir)
		}
		scriptCfg := cfg.Scripts[rel]
		target = &scriptCfg
		targetSources = &scriptCfg.Sources
		defer func() {
			if cfg.Scripts == nil {
				cfg.Scripts = map[string]ScriptConfig{}
			}
			cfg.Scripts[rel] = *target
		}()
	}

	if opts.Bioc {
		target.BiocPackages = mergeStrings(target.BiocPackages, []string{opts.Package})
		return nil
	}

	target.Packages = mergeStrings(target.Packages, []string{opts.Package})
	if opts.Source != nil {
		if *targetSources == nil {
			*targetSources = map[string]SourceSpec{}
		}
		spec := *opts.Source
		spec.Package = opts.Package
		(*targetSources)[opts.Package] = spec
	}
	return nil
}

func RemovePackage(cfg *Config, opts RemovePackageOptions) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if opts.Package == "" {
		return fmt.Errorf("package name is required")
	}

	target := &cfg.Defaults
	targetSources := &cfg.Sources
	removeScriptKey := ""
	if opts.ScriptPath != "" {
		if cfg.RootDir == "" {
			return fmt.Errorf("config root is unknown")
		}
		rel, err := filepath.Rel(cfg.RootDir, opts.ScriptPath)
		if err != nil {
			return fmt.Errorf("resolve script path relative to project: %w", err)
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			return fmt.Errorf("script %s is outside project root %s", opts.ScriptPath, cfg.RootDir)
		}
		scriptCfg, ok := cfg.Scripts[rel]
		if !ok {
			return fmt.Errorf("script profile %q not found in %s", rel, cfg.Path)
		}
		target = &scriptCfg
		targetSources = &scriptCfg.Sources
		removeScriptKey = rel
		defer func() {
			if scriptConfigEmpty(*target) {
				delete(cfg.Scripts, removeScriptKey)
				return
			}
			if cfg.Scripts == nil {
				cfg.Scripts = map[string]ScriptConfig{}
			}
			cfg.Scripts[removeScriptKey] = *target
		}()
	}

	removed := false
	if opts.Bioc {
		var changed bool
		target.BiocPackages, changed = removeString(target.BiocPackages, opts.Package)
		removed = changed
	} else {
		var changed bool
		target.Packages, changed = removeString(target.Packages, opts.Package)
		removed = changed
		if len(*targetSources) > 0 {
			if _, ok := (*targetSources)[opts.Package]; ok {
				if removeScriptKey != "" {
					transferDeletedScriptSourceComments(cfg, removeScriptKey, opts.Package)
				} else {
					transferDeletedRootSourceComments(cfg, opts.Package)
				}
				delete(*targetSources, opts.Package)
				removed = true
			}
		}
	}

	if !removed {
		scope := "project"
		if opts.ScriptPath != "" {
			scope = opts.ScriptPath
		}
		return fmt.Errorf("package %q not found in %s config", opts.Package, scope)
	}
	if removeScriptKey != "" && scriptConfigEmpty(*target) {
		transferDeletedScriptComments(cfg, removeScriptKey)
	}
	return nil
}

func transferDeletedRootSourceComments(cfg *Config, sourceName string) {
	if cfg == nil {
		return
	}
	block := collectRootSourceCommentBlock(cfg.EditMeta, sourceName)
	if len(block) == 0 {
		deleteRootSourceMetadata(&cfg.EditMeta, sourceName)
		return
	}

	nextEntry := nextOrderedTopLevelEntryAfter(*cfg, topLevelEntrySource(sourceName))
	if prependTopLevelCommentBlock(&cfg.EditMeta, nextEntry, block) {
		deleteRootSourceMetadata(&cfg.EditMeta, sourceName)
		return
	}

	cfg.EditMeta.Epilogue = appendRawBlock(cfg.EditMeta.Epilogue, trimLeadingBlankLines(block))
	deleteRootSourceMetadata(&cfg.EditMeta, sourceName)
}

func transferDeletedScriptSourceComments(cfg *Config, scriptName, sourceName string) {
	if cfg == nil {
		return
	}
	block := collectScriptSourceCommentBlock(cfg.EditMeta, scriptName, sourceName)
	if len(block) == 0 {
		deleteScriptSourceMetadata(&cfg.EditMeta, scriptName, sourceName)
		return
	}

	var aliveSources []string
	if scriptCfg, ok := cfg.Scripts[scriptName]; ok {
		aliveSources = sortedSourceNames(scriptCfg.Sources)
	}
	nextSource := nextOrderedNameAfter(sourceName, cfg.EditMeta.ScriptSourceMap[scriptName], aliveSources)
	if nextSource != "" {
		key := commentPathKey(scriptName, nextSource)
		if cfg.EditMeta.ScriptSourceLeadingNotes == nil {
			cfg.EditMeta.ScriptSourceLeadingNotes = map[string][]string{}
		}
		cfg.EditMeta.ScriptSourceLeadingNotes[key] = prependRawLines(block, cfg.EditMeta.ScriptSourceLeadingNotes[key])
		deleteScriptSourceMetadata(&cfg.EditMeta, scriptName, sourceName)
		return
	}

	if cfg.EditMeta.ScriptLeadingComments == nil {
		cfg.EditMeta.ScriptLeadingComments = map[string][]string{}
	}
	cfg.EditMeta.ScriptLeadingComments[scriptName] = appendRawBlock(cfg.EditMeta.ScriptLeadingComments[scriptName], block)
	deleteScriptSourceMetadata(&cfg.EditMeta, scriptName, sourceName)
}

func transferDeletedScriptComments(cfg *Config, scriptName string) {
	if cfg == nil {
		return
	}
	block := collectScriptCommentBlock(cfg.EditMeta, scriptName)
	if len(block) == 0 {
		deleteScriptMetadata(&cfg.EditMeta, scriptName)
		return
	}

	nextEntry := nextOrderedTopLevelEntryAfter(*cfg, topLevelEntryScript(scriptName))
	if prependTopLevelCommentBlock(&cfg.EditMeta, nextEntry, block) {
		deleteScriptMetadata(&cfg.EditMeta, scriptName)
		return
	}

	cfg.EditMeta.Epilogue = appendRawBlock(cfg.EditMeta.Epilogue, trimLeadingBlankLines(block))
	deleteScriptMetadata(&cfg.EditMeta, scriptName)
}

func collectRootSourceCommentBlock(meta EditMetadata, sourceName string) []string {
	return collectSectionCommentBlock(
		meta.RootSourceLeadingComments[sourceName],
		meta.RootSourceTrailingComment[sourceName],
		meta.SourceKeyOrder[sourceName],
		func(key string) []string { return meta.SourceKeyLeadingComments[commentPathKey(sourceName, key)] },
		func(key string) string { return meta.SourceKeyTrailingComments[commentPathKey(sourceName, key)] },
	)
}

func collectScriptSourceCommentBlock(meta EditMetadata, scriptName, sourceName string) []string {
	return collectSectionCommentBlock(
		meta.ScriptSourceLeadingNotes[commentPathKey(scriptName, sourceName)],
		meta.ScriptSourceTrailingNotes[commentPathKey(scriptName, sourceName)],
		meta.ScriptSourceKey[scriptName][sourceName],
		func(key string) []string {
			return meta.ScriptSourceKeyLeading[commentPathKey(scriptName, sourceName, key)]
		},
		func(key string) string {
			return meta.ScriptSourceKeyTrailing[commentPathKey(scriptName, sourceName, key)]
		},
	)
}

func collectScriptCommentBlock(meta EditMetadata, scriptName string) []string {
	block := collectSectionCommentBlock(
		meta.ScriptLeadingComments[scriptName],
		meta.ScriptTrailingComments[scriptName],
		meta.ScriptKeyOrder[scriptName],
		func(key string) []string { return meta.ScriptKeyLeadingComments[commentPathKey(scriptName, key)] },
		func(key string) string { return meta.ScriptKeyTrailingComments[commentPathKey(scriptName, key)] },
	)
	for _, sourceName := range meta.ScriptSourceMap[scriptName] {
		block = appendRawBlock(block, collectScriptSourceCommentBlock(meta, scriptName, sourceName))
	}
	return block
}

func collectSectionCommentBlock(sectionLeading []string, sectionTrailing string, fieldOrder []string, fieldLeading func(string) []string, fieldTrailing func(string) string) []string {
	block := append([]string(nil), sectionLeading...)
	block = appendRawBlock(block, trailingCommentAsBlock(sectionTrailing))
	for _, key := range fieldOrder {
		block = appendRawBlock(block, fieldLeading(key))
		block = appendRawBlock(block, trailingCommentAsBlock(fieldTrailing(key)))
	}
	return block
}

func trailingCommentAsBlock(comment string) []string {
	trimmed := strings.TrimSpace(comment)
	if trimmed == "" {
		return nil
	}
	return []string{trimmed}
}

func prependRawLines(block, existing []string) []string {
	if len(block) == 0 {
		return existing
	}
	existing = trimLeadingBlankLines(existing)
	out := append([]string(nil), block...)
	return append(out, existing...)
}

func appendRawBlock(dst, block []string) []string {
	if len(block) == 0 {
		return dst
	}
	out := append([]string(nil), dst...)
	return append(out, block...)
}

func trimLeadingBlankLines(lines []string) []string {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return lines
}

func shouldInsertPreambleSeparator(preamble, body []string) bool {
	if len(preamble) == 0 || len(body) == 0 {
		return false
	}

	last := strings.TrimSpace(preamble[len(preamble)-1])
	if last == "" {
		return false
	}

	first := ""
	for _, line := range body {
		first = strings.TrimSpace(line)
		if first != "" {
			break
		}
	}
	if first == "" {
		return false
	}
	if strings.HasPrefix(last, "#") && strings.HasPrefix(first, "#") {
		return false
	}
	if strings.HasPrefix(last, "#") && strings.HasPrefix(first, "[") {
		return false
	}
	return true
}

func nextOrderedNameAfter(current string, preferredOrder, alive []string) string {
	if len(alive) == 0 {
		return ""
	}
	aliveSet := make(map[string]struct{}, len(alive))
	for _, name := range alive {
		aliveSet[name] = struct{}{}
	}
	foundCurrent := current == ""
	for _, name := range preferredOrder {
		if !foundCurrent {
			if name == current {
				foundCurrent = true
			}
			continue
		}
		if name == current {
			continue
		}
		if _, ok := aliveSet[name]; ok {
			return name
		}
	}
	if current == "" {
		return alive[0]
	}
	return ""
}

func nextOrderedTopLevelEntryAfter(cfg Config, current string) string {
	entries := orderedTopLevelEntries(cfg)
	if len(entries) == 0 {
		return ""
	}
	foundCurrent := false
	for _, entry := range entries {
		if !foundCurrent {
			if entry == current {
				foundCurrent = true
			}
			continue
		}
		if entry != current {
			return entry
		}
	}
	return ""
}

func prependTopLevelCommentBlock(meta *EditMetadata, entry string, block []string) bool {
	if meta == nil || entry == "" || len(block) == 0 {
		return false
	}
	switch {
	case strings.HasPrefix(entry, "source\x00"):
		name := strings.TrimPrefix(entry, "source\x00")
		if meta.RootSourceLeadingComments == nil {
			meta.RootSourceLeadingComments = map[string][]string{}
		}
		meta.RootSourceLeadingComments[name] = prependRawLines(block, meta.RootSourceLeadingComments[name])
		return true
	case strings.HasPrefix(entry, "script\x00"):
		name := strings.TrimPrefix(entry, "script\x00")
		if meta.ScriptLeadingComments == nil {
			meta.ScriptLeadingComments = map[string][]string{}
		}
		meta.ScriptLeadingComments[name] = prependRawLines(block, meta.ScriptLeadingComments[name])
		return true
	default:
		return false
	}
}

func deleteRootSourceMetadata(meta *EditMetadata, sourceName string) {
	if meta == nil {
		return
	}
	if meta.RootSourceLeadingComments != nil {
		delete(meta.RootSourceLeadingComments, sourceName)
	}
	if meta.RootSourceTrailingComment != nil {
		delete(meta.RootSourceTrailingComment, sourceName)
	}
	if meta.SourceKeyOrder != nil {
		delete(meta.SourceKeyOrder, sourceName)
	}
	prefix := commentPathKey(sourceName) + "\x00"
	if meta.SourceKeyLeadingComments != nil {
		for key := range meta.SourceKeyLeadingComments {
			if strings.HasPrefix(key, prefix) {
				delete(meta.SourceKeyLeadingComments, key)
			}
		}
	}
	if meta.SourceKeyTrailingComments != nil {
		for key := range meta.SourceKeyTrailingComments {
			if strings.HasPrefix(key, prefix) {
				delete(meta.SourceKeyTrailingComments, key)
			}
		}
	}
	meta.RootSourceOrder = removeFromOrder(meta.RootSourceOrder, sourceName)
	meta.TopLevelOrder = removeFromOrder(meta.TopLevelOrder, topLevelEntrySource(sourceName))
}

func deleteScriptSourceMetadata(meta *EditMetadata, scriptName, sourceName string) {
	if meta == nil {
		return
	}
	if meta.ScriptSourceLeadingNotes != nil {
		delete(meta.ScriptSourceLeadingNotes, commentPathKey(scriptName, sourceName))
	}
	if meta.ScriptSourceTrailingNotes != nil {
		delete(meta.ScriptSourceTrailingNotes, commentPathKey(scriptName, sourceName))
	}
	if meta.ScriptSourceKey != nil && meta.ScriptSourceKey[scriptName] != nil {
		delete(meta.ScriptSourceKey[scriptName], sourceName)
		if len(meta.ScriptSourceKey[scriptName]) == 0 {
			delete(meta.ScriptSourceKey, scriptName)
		}
	}
	prefix := commentPathKey(scriptName, sourceName) + "\x00"
	if meta.ScriptSourceKeyLeading != nil {
		for key := range meta.ScriptSourceKeyLeading {
			if strings.HasPrefix(key, prefix) {
				delete(meta.ScriptSourceKeyLeading, key)
			}
		}
	}
	if meta.ScriptSourceKeyTrailing != nil {
		for key := range meta.ScriptSourceKeyTrailing {
			if strings.HasPrefix(key, prefix) {
				delete(meta.ScriptSourceKeyTrailing, key)
			}
		}
	}
	if meta.ScriptSourceMap != nil {
		meta.ScriptSourceMap[scriptName] = removeFromOrder(meta.ScriptSourceMap[scriptName], sourceName)
		if len(meta.ScriptSourceMap[scriptName]) == 0 {
			delete(meta.ScriptSourceMap, scriptName)
		}
	}
}

func deleteScriptMetadata(meta *EditMetadata, scriptName string) {
	if meta == nil {
		return
	}
	if meta.ScriptLeadingComments != nil {
		delete(meta.ScriptLeadingComments, scriptName)
	}
	if meta.ScriptTrailingComments != nil {
		delete(meta.ScriptTrailingComments, scriptName)
	}
	prefix := commentPathKey(scriptName) + "\x00"
	if meta.ScriptKeyLeadingComments != nil {
		for key := range meta.ScriptKeyLeadingComments {
			if strings.HasPrefix(key, prefix) {
				delete(meta.ScriptKeyLeadingComments, key)
			}
		}
	}
	if meta.ScriptKeyTrailingComments != nil {
		for key := range meta.ScriptKeyTrailingComments {
			if strings.HasPrefix(key, prefix) {
				delete(meta.ScriptKeyTrailingComments, key)
			}
		}
	}
	if meta.ScriptKeyOrder != nil {
		delete(meta.ScriptKeyOrder, scriptName)
	}
	if meta.ScriptSourceMap != nil {
		if sources := append([]string(nil), meta.ScriptSourceMap[scriptName]...); len(sources) > 0 {
			for _, sourceName := range sources {
				deleteScriptSourceMetadata(meta, scriptName, sourceName)
			}
		}
		delete(meta.ScriptSourceMap, scriptName)
	}
	if meta.ScriptSourceKey != nil {
		delete(meta.ScriptSourceKey, scriptName)
	}
	meta.ScriptOrder = removeFromOrder(meta.ScriptOrder, scriptName)
	meta.TopLevelOrder = removeFromOrder(meta.TopLevelOrder, topLevelEntryScript(scriptName))
}

func removeFromOrder(values []string, target string) []string {
	if len(values) == 0 {
		return values
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value == target {
			continue
		}
		out = append(out, value)
	}
	return out
}

func validateSourceSpec(spec SourceSpec) error {
	return validateSourceSpecWithLabel(spec, fmt.Sprintf("source for %q", spec.Package))
}

func validateSourceSpecInSection(section string, spec SourceSpec) error {
	return validateSourceSpecWithLabel(spec, section)
}

func validateSourceSpecWithLabel(spec SourceSpec, label string) error {
	switch spec.Type {
	case "github":
		if spec.Repo == "" {
			return fmt.Errorf("%s requires repo when type = %q", label, spec.Type)
		}
		if spec.URL != "" {
			return fmt.Errorf("%s cannot set url when type = %q", label, spec.Type)
		}
		if spec.Path != "" {
			return fmt.Errorf("%s cannot set path when type = %q", label, spec.Type)
		}
	case "git":
		if spec.URL == "" {
			return fmt.Errorf("%s requires url when type = %q", label, spec.Type)
		}
		if spec.Repo != "" {
			return fmt.Errorf("%s cannot set repo when type = %q", label, spec.Type)
		}
		if spec.Host != "" {
			return fmt.Errorf("%s cannot set host when type = %q", label, spec.Type)
		}
		if spec.TokenEnv != "" {
			return fmt.Errorf("%s cannot set token_env when type = %q", label, spec.Type)
		}
		if spec.Path != "" {
			return fmt.Errorf("%s cannot set path when type = %q", label, spec.Type)
		}
	case "local":
		if spec.Path == "" {
			return fmt.Errorf("%s requires path when type = %q", label, spec.Type)
		}
		if spec.Repo != "" {
			return fmt.Errorf("%s cannot set repo when type = %q", label, spec.Type)
		}
		if spec.URL != "" {
			return fmt.Errorf("%s cannot set url when type = %q", label, spec.Type)
		}
		if spec.Host != "" {
			return fmt.Errorf("%s cannot set host when type = %q", label, spec.Type)
		}
		if spec.Ref != "" {
			return fmt.Errorf("%s cannot set ref when type = %q", label, spec.Type)
		}
		if spec.Subdir != "" {
			return fmt.Errorf("%s cannot set subdir when type = %q", label, spec.Type)
		}
		if spec.TokenEnv != "" {
			return fmt.Errorf("%s cannot set token_env when type = %q", label, spec.Type)
		}
	case "":
		return fmt.Errorf("%s requires type", label)
	default:
		return fmt.Errorf("%s uses %w", label, unsupportedValueError("source type", spec.Type, sourceTypes))
	}
	return nil
}

func scriptConfigEmpty(cfg ScriptConfig) bool {
	return cfg.Repo == "" &&
		cfg.CacheDir == "" &&
		cfg.Lockfile == "" &&
		cfg.Rscript == "" &&
		cfg.RVersion == "" &&
		len(cfg.ToolchainPrefixes) == 0 &&
		len(cfg.PkgConfigPath) == 0 &&
		len(cfg.Packages) == 0 &&
		len(cfg.BiocPackages) == 0 &&
		len(cfg.Sources) == 0
}

func appendScriptConfigLines(lines *[]string, cfg ScriptConfig) {
	appendScriptConfigLinesWithOrder(lines, cfg, nil, nil, nil)
}

func appendScriptConfigLinesWithOrder(lines *[]string, cfg ScriptConfig, order []string, leading func(string) []string, trailing func(string) string) {
	seen := map[string]struct{}{}
	appendKnown := func(key string) {
		appendRawLines(lines, commentLines(leading, key))
		trailingComment := commentText(trailing, key)
		switch key {
		case "repo":
			if cfg.Repo != "" {
				*lines = append(*lines, "repo = "+quoteString(cfg.Repo)+trailingComment)
				seen[key] = struct{}{}
			}
		case "cache_dir":
			if cfg.CacheDir != "" {
				*lines = append(*lines, "cache_dir = "+quoteString(cfg.CacheDir)+trailingComment)
				seen[key] = struct{}{}
			}
		case "lockfile":
			if cfg.Lockfile != "" {
				*lines = append(*lines, "lockfile = "+quoteString(cfg.Lockfile)+trailingComment)
				seen[key] = struct{}{}
			}
		case "rscript":
			if cfg.Rscript != "" {
				*lines = append(*lines, "rscript = "+quoteString(cfg.Rscript)+trailingComment)
				seen[key] = struct{}{}
			}
		case "r_version":
			if cfg.RVersion != "" {
				*lines = append(*lines, "r_version = "+quoteString(cfg.RVersion)+trailingComment)
				seen[key] = struct{}{}
			}
		case "toolchain_prefixes":
			if len(cfg.ToolchainPrefixes) > 0 {
				*lines = append(*lines, "toolchain_prefixes = "+renderStringArray(cfg.ToolchainPrefixes)+trailingComment)
				seen[key] = struct{}{}
			}
		case "pkg_config_path":
			if len(cfg.PkgConfigPath) > 0 {
				*lines = append(*lines, "pkg_config_path = "+renderStringArray(cfg.PkgConfigPath)+trailingComment)
				seen[key] = struct{}{}
			}
		case "packages":
			if len(cfg.Packages) > 0 {
				*lines = append(*lines, "packages = "+renderStringArray(cfg.Packages)+trailingComment)
				seen[key] = struct{}{}
			}
		case "bioc_packages":
			if len(cfg.BiocPackages) > 0 {
				*lines = append(*lines, "bioc_packages = "+renderStringArray(cfg.BiocPackages)+trailingComment)
				seen[key] = struct{}{}
			}
		}
	}

	for _, key := range order {
		appendKnown(key)
	}
	for _, key := range []string{"repo", "cache_dir", "lockfile", "rscript", "r_version", "toolchain_prefixes", "pkg_config_path", "packages", "bioc_packages"} {
		if _, ok := seen[key]; ok {
			continue
		}
		appendKnown(key)
	}
}

func orderedNames(names, preferred []string) []string {
	if len(names) == 0 {
		return nil
	}
	index := make(map[string]struct{}, len(names))
	for _, name := range names {
		index[name] = struct{}{}
	}

	out := make([]string, 0, len(names))
	for _, name := range preferred {
		if _, ok := index[name]; !ok {
			continue
		}
		out = append(out, name)
		delete(index, name)
	}
	for _, name := range names {
		if _, ok := index[name]; !ok {
			continue
		}
		out = append(out, name)
	}
	return out
}

func commentLines(lookup func(string) []string, key string) []string {
	if lookup == nil {
		return nil
	}
	return lookup(key)
}

func commentText(lookup func(string) string, key string) string {
	if lookup == nil {
		return ""
	}
	return lookup(key)
}

func appendRawLines(lines *[]string, raw []string) {
	if len(raw) == 0 {
		return
	}
	for _, line := range raw {
		if strings.TrimSpace(line) == "" && len(*lines) > 0 && strings.TrimSpace((*lines)[len(*lines)-1]) == "" {
			continue
		}
		*lines = append(*lines, line)
	}
}

func appendSourceSpecLines(lines *[]string, spec SourceSpec) {
	appendSourceSpecLinesWithOrder(lines, spec, nil, nil, nil)
}

func appendSourceSpecLinesWithOrder(lines *[]string, spec SourceSpec, order []string, leading func(string) []string, trailing func(string) string) {
	seen := map[string]struct{}{}
	appendKnown := func(key string) {
		appendRawLines(lines, commentLines(leading, key))
		trailingComment := commentText(trailing, key)
		switch key {
		case "type":
			if spec.Type != "" {
				*lines = append(*lines, "type = "+quoteString(spec.Type)+trailingComment)
				seen[key] = struct{}{}
			}
		case "host":
			if spec.Host != "" {
				*lines = append(*lines, "host = "+quoteString(spec.Host)+trailingComment)
				seen[key] = struct{}{}
			}
		case "repo":
			if spec.Repo != "" {
				*lines = append(*lines, "repo = "+quoteString(spec.Repo)+trailingComment)
				seen[key] = struct{}{}
			}
		case "url":
			if spec.URL != "" {
				*lines = append(*lines, "url = "+quoteString(spec.URL)+trailingComment)
				seen[key] = struct{}{}
			}
		case "ref":
			if spec.Ref != "" {
				*lines = append(*lines, "ref = "+quoteString(spec.Ref)+trailingComment)
				seen[key] = struct{}{}
			}
		case "path":
			if spec.Path != "" {
				*lines = append(*lines, "path = "+quoteString(spec.Path)+trailingComment)
				seen[key] = struct{}{}
			}
		case "subdir":
			if spec.Subdir != "" {
				*lines = append(*lines, "subdir = "+quoteString(spec.Subdir)+trailingComment)
				seen[key] = struct{}{}
			}
		case "token_env":
			if spec.TokenEnv != "" {
				*lines = append(*lines, "token_env = "+quoteString(spec.TokenEnv)+trailingComment)
				seen[key] = struct{}{}
			}
		}
	}

	for _, key := range order {
		appendKnown(key)
	}
	for _, key := range []string{"type", "host", "repo", "url", "ref", "path", "subdir", "token_env"} {
		if _, ok := seen[key]; ok {
			continue
		}
		appendKnown(key)
	}
}

func renderStringArray(values []string) string {
	if len(values) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, quoteString(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func removeString(values []string, target string) ([]string, bool) {
	if len(values) == 0 {
		return values, false
	}
	out := make([]string, 0, len(values))
	removed := false
	for _, value := range values {
		if value == target {
			removed = true
			continue
		}
		out = append(out, value)
	}
	return out, removed
}

func quoteString(value string) string {
	return strconv.Quote(value)
}

func sortedSourceNames(sourceMap map[string]SourceSpec) []string {
	if len(sourceMap) == 0 {
		return nil
	}
	names := make([]string, 0, len(sourceMap))
	for name := range sourceMap {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func sortedScriptNames(scriptMap map[string]ScriptConfig) []string {
	if len(scriptMap) == 0 {
		return nil
	}
	names := make([]string, 0, len(scriptMap))
	for name := range scriptMap {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
