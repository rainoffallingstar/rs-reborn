package project

import internalproject "github.com/rainoffallingstar/rs-reborn/internal/project"

const (
	ConfigFileName = internalproject.ConfigFileName
	DefaultRepo    = internalproject.DefaultRepo
)

type ScriptConfig = internalproject.ScriptConfig
type SourceSpec = internalproject.SourceSpec
type Config = internalproject.Config
type EditMetadata = internalproject.EditMetadata
type ResolvedScriptConfig = internalproject.ResolvedScriptConfig
type InitOptions = internalproject.InitOptions
type AddPackageOptions = internalproject.AddPackageOptions
type RemovePackageOptions = internalproject.RemovePackageOptions

func Discover(start string) (Config, bool, error) {
	return internalproject.Discover(start)
}

func Load(path string) (Config, error) {
	return internalproject.Load(path)
}

func Parse(src string) (Config, error) {
	return internalproject.Parse(src)
}

func LoadEditable(path string) (Config, error) {
	return internalproject.LoadEditable(path)
}

func Save(path string, cfg Config) error {
	return internalproject.Save(path, cfg)
}

func Render(cfg Config) string {
	return internalproject.Render(cfg)
}

func NewDefaultConfig(opts InitOptions) Config {
	return internalproject.NewDefaultConfig(opts)
}

func NewConfigFromScript(opts InitOptions, rootDir string, scriptPath string, writeScriptBlock bool) (Config, error) {
	return internalproject.NewConfigFromScript(opts, rootDir, scriptPath, writeScriptBlock)
}

func NewConfigFromScripts(opts InitOptions, rootDir string, scriptConfigs map[string]ScriptConfig, writeScriptBlock bool) (Config, error) {
	return internalproject.NewConfigFromScripts(opts, rootDir, scriptConfigs, writeScriptBlock)
}

func AddPackage(cfg *Config, opts AddPackageOptions) error {
	return internalproject.AddPackage((*internalproject.Config)(cfg), internalproject.AddPackageOptions(opts))
}

func RemovePackage(cfg *Config, opts RemovePackageOptions) error {
	return internalproject.RemovePackage((*internalproject.Config)(cfg), internalproject.RemovePackageOptions(opts))
}
