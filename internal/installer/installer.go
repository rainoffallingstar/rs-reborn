package installer

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rainoffallingstar/rs-reborn/internal/progresscmd"
	"github.com/rainoffallingstar/rs-reborn/internal/project"
	"github.com/rainoffallingstar/rs-reborn/internal/rdeps"
	"github.com/rainoffallingstar/rs-reborn/internal/toolchainenv"
)

const (
	sourceCRAN            = "cran"
	sourceBioconductor    = "bioconductor"
	sourceLocal           = "local"
	sourceGit             = "git"
	sourceGitHub          = "github"
	localKindFileSHA256   = "file_sha256"
	localKindDirSHA256    = "dir_tree_sha256"
	localKindMissing      = "missing"
	localKindError        = "unavailable"
	defaultHTTPTimeout    = 90 * time.Second
	httpRetryAttempts     = 3
	repoIndexCacheTTL     = 30 * time.Minute
	PackageStoreStateFile = ".rs-store-state.json"
)

var (
	installerGOOS                = runtime.GOOS
	installerLookPath            = exec.LookPath
	installerReadFile            = os.ReadFile
	installerRunCmd              = func(cmd *exec.Cmd) error { return cmd.Run() }
	installerReadDescriptionFile = readDescriptionFromTarball
)

type Runtime struct {
	Interpreter         string
	InterpreterIdentity string
	RVersion            string
	Platform            string
	Arch                string
	OS                  string
	PackageType         string
	InterpreterKind     string
}

type Request struct {
	Interpreter string
	WorkDir     string
	CacheRoot   string
	LibraryPath string
	Repo        string
	Environment []string
	Runtime     Runtime
	CRANDeps    []string
	BiocDeps    []string
	SourceDeps  map[string]project.SourceSpec
	Stdout      io.Writer
	Stderr      io.Writer
}

type nativeInstaller struct {
	req Request

	rBinary           string
	tempRoot          string
	downloadRoot      string
	metaDir           string
	stdout            io.Writer
	stderr            io.Writer
	planned           map[string]plannedPackage
	resolving         map[string]bool
	resolved          map[string]bool
	order             []string
	cranIndex         map[string][]repoRecord
	cranArchiveLoaded map[string]bool
	biocIndex         map[string][]repoRecord
	biocLoaded        bool
	biocAnnIndex      map[string][]repoRecord
	biocAnnLoaded     bool
	biocExpIndex      map[string][]repoRecord
	biocExpLoaded     bool
	sourceCache       map[string]preparedSource
	httpClient        *http.Client
	installedPackages map[string]installedPackage
	requirements      map[string][]constraintRequest
	selectedVersions  map[string]string
	buildToolsChecked bool
	prefetchedMu      sync.RWMutex
	prefetchedRepo    map[string]string
	descriptionMu     sync.RWMutex
	descriptionCache  map[string]description
}

func (i *nativeInstaller) stage(label string) {
	progresscmd.Stage(i.stderr, label)
}

type plannedPackage struct {
	Name     string
	Version  string
	Source   string
	Deps     []packageRequirement
	Repo     *repoRecord
	Prepared *preparedSource
}

type repoRecord struct {
	Name             string
	Version          string
	Dependencies     []packageRequirement
	TarballURL       string
	Source           string
	DepsLoaded       bool
	NeedsCompilation bool
}

type preparedSource struct {
	Name             string
	Version          string
	Source           string
	Host             string
	Location         string
	Ref              string
	Commit           string
	Subdir           string
	Fingerprint      string
	FingerprintKind  string
	Dependencies     []packageRequirement
	InstallPath      string
	NeedsCompilation bool
}

type installedPackage struct {
	Name            string
	Version         string
	Source          string
	Host            string
	Location        string
	Ref             string
	Commit          string
	Subdir          string
	Fingerprint     string
	FingerprintKind string
}

type PackageStoreState struct {
	Package         string `json:"package"`
	Version         string `json:"version"`
	Source          string `json:"source"`
	RuntimeIdentity string `json:"runtime_identity"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	LastUsedAt      string `json:"last_used_at,omitempty"`
}

type description struct {
	Package          string
	Version          string
	Dependencies     []packageRequirement
	NeedsCompilation bool
}

type packageRequirement struct {
	Name        string
	Constraints []versionConstraint
}

type versionConstraint struct {
	Operator string
	Version  string
}

type constraintRequest struct {
	RequiredBy  string
	Constraints []versionConstraint
	Chain       []string
}

type planningState struct {
	planned          map[string]plannedPackage
	resolved         map[string]bool
	order            []string
	requirements     map[string][]constraintRequest
	selectedVersions map[string]string
}

type versionSkips map[string]map[string]struct{}

type exhaustedCandidatesError struct {
	Package string
}

func (e exhaustedCandidatesError) Error() string {
	return fmt.Sprintf("all candidate versions exhausted for %s", e.Package)
}

type ConstraintConflictError struct {
	Package     string
	Version     string
	RequiredBy  string
	Operator    string
	Requirement string
	Chain       []string
}

func (e ConstraintConflictError) Error() string {
	chain := append([]string(nil), e.Chain...)
	chain = append(chain, e.Package)
	pathSuffix := ""
	if len(chain) > 1 {
		pathSuffix = fmt.Sprintf(" (dependency path: %s)", strings.Join(chain, " -> "))
	}
	if e.RequiredBy == "" {
		return fmt.Sprintf("dependency constraint conflict for %s: selected version %s does not satisfy %s %s%s", e.Package, e.Version, e.Operator, e.Requirement, pathSuffix)
	}
	return fmt.Sprintf("dependency constraint conflict for %s: selected version %s does not satisfy %s %s required by %s%s", e.Package, e.Version, e.Operator, e.Requirement, e.RequiredBy, pathSuffix)
}

type repoHint string

const (
	hintAny  repoHint = ""
	hintCRAN repoHint = "cran"
	hintBioc repoHint = "bioconductor"
)

func Validate(req Request) error {
	inst, err := newInstaller(req, false)
	if err != nil {
		return err
	}
	defer os.RemoveAll(inst.tempRoot)
	return inst.plan()
}

func Install(req Request) error {
	inst, err := newInstaller(req, true)
	if err != nil {
		return err
	}
	defer os.RemoveAll(inst.tempRoot)

	if err := inst.plan(); err != nil {
		return err
	}
	if err := inst.seedPlannedPackagesFromStore(); err != nil {
		return err
	}
	if err := inst.seedPlannedPackagesFromCache(); err != nil {
		return err
	}
	if err := inst.prefetchPlannedPackages(); err != nil {
		return err
	}

	if inst.canParallelInstallPurePackages() {
		layers := installPlanLayers(inst.planned, inst.order)
		for idx, layer := range layers {
			progresscmd.Stage(inst.stderr, fmt.Sprintf("installing dependency layer %d/%d", idx+1, len(layers)))
			pure := make([]string, 0, len(layer))
			compiled := make([]string, 0, len(layer))
			for _, name := range layer {
				if inst.isPlannedPackageInstalled(inst.planned[name]) {
					continue
				}
				if plannedPackageNeedsCompilation(inst.planned[name]) {
					compiled = append(compiled, name)
					continue
				}
				pure = append(pure, name)
			}
			installed, err := inst.installPackageBatch(pure)
			if err != nil {
				return err
			}
			for _, name := range installed {
				if err := inst.recordPlannedPackageInstalled(name); err != nil {
					return err
				}
			}
			for _, name := range compiled {
				installed, err := inst.installPlannedPackage(name)
				if err != nil {
					return err
				}
				if installed {
					if err := inst.recordPlannedPackageInstalled(name); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	for idx, name := range inst.order {
		progresscmd.Stage(inst.stderr, fmt.Sprintf("installing package %d/%d", idx+1, len(inst.order)))
		installed, err := inst.installPlannedPackage(name)
		if err != nil {
			return err
		}
		if installed {
			if err := inst.recordPlannedPackageInstalled(name); err != nil {
				return err
			}
		}
	}

	return nil
}

func (i *nativeInstaller) recordPlannedPackageInstalled(name string) error {
	pkg, ok := i.planned[name]
	if !ok {
		return fmt.Errorf("planned package %s not found", name)
	}
	i.installedPackages[name] = installedPackageForPlanned(pkg)
	return i.syncPlannedPackageToStore(name)
}

func (i *nativeInstaller) seedPlannedPackagesFromStore() error {
	if len(i.planned) == 0 || strings.TrimSpace(i.req.CacheRoot) == "" || strings.TrimSpace(i.req.LibraryPath) == "" {
		return nil
	}

	for name, pkg := range i.planned {
		if i.isPlannedPackageInstalled(pkg) {
			continue
		}
		storeLibrary := packageStorePathForPlanned(i.req.CacheRoot, pkg, i.req.Runtime)
		if strings.TrimSpace(storeLibrary) == "" {
			continue
		}
		installedPkg, ok, err := loadInstalledPackageFromLibrary(storeLibrary, name)
		if err != nil {
			return err
		}
		if !ok || !plannedPackageMatchesInstalled(pkg, installedPkg) {
			continue
		}
		if err := copyInstalledPackage(storeLibrary, i.req.LibraryPath, name); err != nil {
			return err
		}
		if err := copyInstalledPackageMetadata(storeLibrary, i.metaDir, name); err != nil {
			return err
		}
		if err := touchPackageStoreLastUsed(storeLibrary, pkg, i.req.Runtime, time.Now().UTC()); err != nil {
			return err
		}
		i.installedPackages[name] = installedPkg
		progresscmd.Stage(i.stderr, "reusing stored "+name)
	}
	return nil
}

func (i *nativeInstaller) seedPlannedPackagesFromCache() error {
	if len(i.planned) == 0 || strings.TrimSpace(i.req.CacheRoot) == "" || strings.TrimSpace(i.req.LibraryPath) == "" {
		return nil
	}

	libraryRoot := filepath.Join(i.req.CacheRoot, "lib")
	entries, err := os.ReadDir(libraryRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read managed library cache root: %w", err)
	}

	currentLibrary := filepath.Clean(i.req.LibraryPath)
	remaining := map[string]plannedPackage{}
	for name, pkg := range i.planned {
		if i.isPlannedPackageInstalled(pkg) {
			continue
		}
		remaining[name] = pkg
	}
	for _, entry := range entries {
		if len(remaining) == 0 {
			break
		}
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		candidateLibrary := filepath.Join(libraryRoot, entry.Name())
		if filepath.Clean(candidateLibrary) == currentLibrary {
			continue
		}
		installed, err := loadInstalledPackagesFromLibrary(candidateLibrary)
		if err != nil {
			return err
		}
		if len(installed) == 0 {
			continue
		}
		for name, pkg := range remaining {
			installedPkg, ok := installed[name]
			if !ok || !plannedPackageMatchesInstalled(pkg, installedPkg) {
				continue
			}
			if err := copyInstalledPackage(candidateLibrary, i.req.LibraryPath, name); err != nil {
				return err
			}
			if err := copyInstalledPackageMetadata(candidateLibrary, i.metaDir, name); err != nil {
				return err
			}
			i.installedPackages[name] = installedPkg
			if err := i.syncPlannedPackageToStore(name); err != nil {
				return err
			}
			delete(remaining, name)
			progresscmd.Stage(i.stderr, "reusing cached "+name+" from "+entry.Name())
		}
	}
	return nil
}

func (i *nativeInstaller) syncPlannedPackageToStore(name string) error {
	if strings.TrimSpace(i.req.CacheRoot) == "" || strings.TrimSpace(i.req.LibraryPath) == "" {
		return nil
	}
	pkg, ok := i.planned[name]
	if !ok {
		return nil
	}
	storeLibrary := packageStorePathForPlanned(i.req.CacheRoot, pkg, i.req.Runtime)
	if strings.TrimSpace(storeLibrary) == "" {
		return nil
	}
	if err := os.MkdirAll(storeLibrary, 0o755); err != nil {
		return fmt.Errorf("create package store dir for %s: %w", name, err)
	}
	if err := copyInstalledPackage(i.req.LibraryPath, storeLibrary, name); err != nil {
		return err
	}
	if err := copyInstalledPackageMetadata(i.req.LibraryPath, filepath.Join(storeLibrary, ".rs-source-meta"), name); err != nil {
		return err
	}
	now := time.Now().UTC()
	return writePackageStoreState(storeLibrary, pkg, i.req.Runtime, PackageStoreState{
		UpdatedAt:  now.Format(time.RFC3339),
		LastUsedAt: now.Format(time.RFC3339),
	})
}

func packageStoreRoot(cacheRoot string) string {
	if strings.TrimSpace(cacheRoot) == "" {
		return ""
	}
	return filepath.Join(cacheRoot, "pkgstore")
}

func packageStorePathForPlanned(cacheRoot string, pkg plannedPackage, runtime Runtime) string {
	root := packageStoreRoot(cacheRoot)
	if root == "" {
		return ""
	}
	sum := sha256.New()
	for _, part := range packageStoreIdentityParts(pkg, runtime) {
		_, _ = sum.Write([]byte(part))
		_, _ = sum.Write([]byte{0})
	}
	return filepath.Join(root, hex.EncodeToString(sum.Sum(nil)))
}

func packageStoreIdentityParts(pkg plannedPackage, runtime Runtime) []string {
	parts := []string{
		"v1",
		pkg.Name,
		pkg.Version,
		pkg.Source,
		runtimeInterpreterIdentity(runtime),
		runtime.RVersion,
		runtime.Platform,
		runtime.Arch,
		runtime.OS,
		runtime.PackageType,
	}
	if pkg.Prepared != nil {
		parts = append(parts,
			pkg.Prepared.Host,
			pkg.Prepared.Location,
			pkg.Prepared.Ref,
			pkg.Prepared.Commit,
			pkg.Prepared.Subdir,
			pkg.Prepared.Fingerprint,
			pkg.Prepared.FingerprintKind,
		)
	}
	return parts
}

func runtimeInterpreterIdentity(runtime Runtime) string {
	if identity := strings.TrimSpace(runtime.InterpreterIdentity); identity != "" {
		return identity
	}
	kind := strings.TrimSpace(runtime.InterpreterKind)
	cleaned := strings.TrimSpace(filepath.Clean(runtime.Interpreter))
	switch kind {
	case "managed":
		if version := managedInterpreterVersion(cleaned); version != "" {
			return "managed:" + version
		}
	case "external-conda":
		if envName := condaInterpreterEnvironmentName(cleaned); envName != "" {
			return "external-conda:" + envName
		}
	}
	if kind == "" {
		kind = "unknown"
	}
	location := interpreterLocationToken(cleaned)
	if location == "" {
		return kind
	}
	return kind + ":" + location
}

func writePackageStoreState(storeLibrary string, pkg plannedPackage, runtime Runtime, state PackageStoreState) error {
	if strings.TrimSpace(storeLibrary) == "" {
		return nil
	}
	if err := os.MkdirAll(storeLibrary, 0o755); err != nil {
		return err
	}
	state.Package = pkg.Name
	state.Version = pkg.Version
	state.Source = pkg.Source
	state.RuntimeIdentity = runtimeInterpreterIdentity(runtime)
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal package store state for %s: %w", pkg.Name, err)
	}
	if err := os.WriteFile(filepath.Join(storeLibrary, PackageStoreStateFile), append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write package store state for %s: %w", pkg.Name, err)
	}
	return nil
}

func readPackageStoreState(storeLibrary string) (PackageStoreState, error) {
	data, err := os.ReadFile(filepath.Join(storeLibrary, PackageStoreStateFile))
	if err != nil {
		return PackageStoreState{}, err
	}
	var state PackageStoreState
	if err := json.Unmarshal(data, &state); err != nil {
		return PackageStoreState{}, fmt.Errorf("decode package store state: %w", err)
	}
	return state, nil
}

func touchPackageStoreLastUsed(storeLibrary string, pkg plannedPackage, runtime Runtime, when time.Time) error {
	state, err := readPackageStoreState(storeLibrary)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return writePackageStoreState(storeLibrary, pkg, runtime, PackageStoreState{
			LastUsedAt: when.Format(time.RFC3339),
		})
	}
	if state.UpdatedAt == "" {
		state.UpdatedAt = when.Format(time.RFC3339)
	}
	state.LastUsedAt = when.Format(time.RFC3339)
	return writePackageStoreState(storeLibrary, pkg, runtime, state)
}

func managedInterpreterVersion(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for idx := 0; idx < len(parts)-1; idx++ {
		if parts[idx] != "versions" {
			continue
		}
		version := strings.TrimSpace(parts[idx+1])
		if version != "" {
			return version
		}
	}
	return ""
}

func condaInterpreterEnvironmentName(path string) string {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for idx := 0; idx < len(parts)-1; idx++ {
		if parts[idx] != "envs" {
			continue
		}
		name := strings.TrimSpace(parts[idx+1])
		if name != "" {
			return name
		}
	}
	return ""
}

func interpreterLocationToken(path string) string {
	if path == "" || path == "." {
		return ""
	}
	dir := filepath.Dir(path)
	base := strings.TrimSpace(filepath.Base(dir))
	switch {
	case base == "", base == ".", base == string(filepath.Separator), strings.EqualFold(base, "bin"), strings.EqualFold(base, "x64"):
		parent := strings.TrimSpace(filepath.Base(filepath.Dir(dir)))
		if parent != "" && parent != "." && parent != string(filepath.Separator) {
			return parent
		}
	default:
		return base
	}
	base = strings.TrimSpace(filepath.Base(path))
	if base == "" || base == "." {
		return ""
	}
	return base
}

func (i *nativeInstaller) canParallelInstallPurePackages() bool {
	if installerGOOS == "windows" {
		return false
	}
	if writerIsTTY(i.stderr) {
		return false
	}
	return true
}

func writerIsTTY(w io.Writer) bool {
	file, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

func plannedPackageNeedsCompilation(pkg plannedPackage) bool {
	switch {
	case pkg.Repo != nil:
		return pkg.Repo.NeedsCompilation
	case pkg.Prepared != nil:
		return pkg.Prepared.NeedsCompilation
	default:
		return false
	}
}

func installPlanLayers(planned map[string]plannedPackage, order []string) [][]string {
	if len(order) == 0 {
		return nil
	}

	orderIndex := make(map[string]int, len(order))
	for idx, name := range order {
		orderIndex[name] = idx
	}

	remaining := make(map[string]int, len(planned))
	dependents := make(map[string][]string, len(planned))
	for name := range planned {
		remaining[name] = 0
	}
	for name, pkg := range planned {
		seen := map[string]struct{}{}
		for _, dep := range pkg.Deps {
			if _, ok := planned[dep.Name]; !ok {
				continue
			}
			if _, ok := seen[dep.Name]; ok {
				continue
			}
			seen[dep.Name] = struct{}{}
			remaining[name]++
			dependents[dep.Name] = append(dependents[dep.Name], name)
		}
	}

	ready := make([]string, 0, len(planned))
	for _, name := range order {
		if remaining[name] == 0 {
			ready = append(ready, name)
		}
	}

	layers := make([][]string, 0, len(planned))
	processed := 0
	for len(ready) > 0 {
		slices.SortFunc(ready, func(a, b string) int {
			return orderIndex[a] - orderIndex[b]
		})
		layer := append([]string(nil), ready...)
		layers = append(layers, layer)
		next := make([]string, 0, len(planned))
		for _, name := range ready {
			processed++
			for _, dependent := range dependents[name] {
				remaining[dependent]--
				if remaining[dependent] == 0 {
					next = append(next, dependent)
				}
			}
		}
		ready = next
	}

	if processed == len(planned) {
		return layers
	}

	// Fall back to deterministic serial order if an unexpected cycle slips through planning.
	fallback := make([]string, 0, len(planned)-processed)
	for _, name := range order {
		if remaining[name] > 0 {
			fallback = append(fallback, name)
		}
	}
	if len(fallback) > 0 {
		layers = append(layers, fallback)
	}
	return layers
}

func (i *nativeInstaller) installPlannedPackage(name string) (bool, error) {
	pkg := i.planned[name]
	if i.isPlannedPackageInstalled(pkg) {
		return false, nil
	}
	switch pkg.Source {
	case sourceCRAN, sourceBioconductor:
		if err := i.installRepoPackage(*pkg.Repo); err != nil {
			return false, err
		}
	case sourceLocal, sourceGit, sourceGitHub:
		if err := i.installPreparedSource(*pkg.Prepared); err != nil {
			return false, err
		}
	default:
		return false, fmt.Errorf("unsupported native source %q for package %s", pkg.Source, name)
	}
	return true, nil
}

func (i *nativeInstaller) prefetchPlannedPackages() error {
	if len(i.planned) == 0 {
		return nil
	}
	type prefetchStats struct {
		reusedCache int
		downloaded  int
		hydrated    int
	}
	stats := prefetchStats{}
	records := make([]repoRecord, 0, len(i.planned))
	seen := map[string]struct{}{}
	for _, name := range i.order {
		pkg, ok := i.planned[name]
		if !ok || pkg.Repo == nil || i.isPlannedPackageInstalled(pkg) {
			continue
		}
		rawURL := strings.TrimSpace(pkg.Repo.TarballURL)
		if rawURL == "" {
			continue
		}
		if _, ok := seen[rawURL]; ok {
			continue
		}
		seen[rawURL] = struct{}{}
		records = append(records, *pkg.Repo)
	}
	if len(records) == 0 {
		return nil
	}

	progresscmd.Stage(i.stderr, fmt.Sprintf("prefetching %d package artifact(s)", len(records)))
	if len(records) == 1 {
		if _, ok := i.repoDownloadReadyPath(records[0]); ok {
			stats.reusedCache++
		}
		if _, err := i.ensureRepoPackageDownloaded(records[0]); err != nil {
			return err
		}
		if stats.reusedCache == 0 {
			stats.downloaded++
		}
		for _, name := range i.order {
			pkg, ok := i.planned[name]
			needsHydrate := ok && pkg.Repo != nil && !pkg.Repo.DepsLoaded
			if err := i.hydratePrefetchedRepoRecord(name); err != nil {
				return fmt.Errorf("hydrate prefetched metadata for %s: %w", name, err)
			}
			if needsHydrate {
				stats.hydrated++
			}
		}
		progresscmd.Stage(i.stderr, formatPrefetchSummary(len(records), stats.downloaded, stats.reusedCache, stats.hydrated))
		return nil
	}

	type prefetchResult struct {
		name   string
		reused bool
		err    error
	}

	workers := len(records)
	if workers > 4 {
		workers = 4
	}
	jobs := make(chan repoRecord)
	results := make(chan prefetchResult, len(records))

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for record := range jobs {
				_, reused := i.repoDownloadReadyPath(record)
				_, err := i.ensureRepoPackageDownloaded(record)
				results <- prefetchResult{name: record.Name, reused: reused, err: err}
			}
		}()
	}

	for _, record := range records {
		jobs <- record
	}
	close(jobs)
	wg.Wait()
	close(results)

	for result := range results {
		if result.err != nil {
			return fmt.Errorf("prefetch %s: %w", result.name, result.err)
		}
		if result.reused {
			stats.reusedCache++
			continue
		}
		stats.downloaded++
	}
	for _, name := range i.order {
		pkg, ok := i.planned[name]
		needsHydrate := ok && pkg.Repo != nil && !pkg.Repo.DepsLoaded
		if err := i.hydratePrefetchedRepoRecord(name); err != nil {
			return fmt.Errorf("hydrate prefetched metadata for %s: %w", name, err)
		}
		if needsHydrate {
			stats.hydrated++
		}
	}
	progresscmd.Stage(i.stderr, formatPrefetchSummary(len(records), stats.downloaded, stats.reusedCache, stats.hydrated))
	return nil
}

func formatPrefetchSummary(total, downloaded, reusedCache, hydrated int) string {
	parts := []string{fmt.Sprintf("prefetched %d package artifact(s)", total)}
	if downloaded > 0 {
		parts = append(parts, fmt.Sprintf("downloaded %d", downloaded))
	}
	if reusedCache > 0 {
		parts = append(parts, fmt.Sprintf("reused %d cached", reusedCache))
	}
	if hydrated > 0 {
		parts = append(parts, fmt.Sprintf("hydrated metadata for %d", hydrated))
	}
	return strings.Join(parts, ", ")
}

func (i *nativeInstaller) hydratePrefetchedRepoRecord(name string) error {
	pkg, ok := i.planned[name]
	if !ok || pkg.Repo == nil || pkg.Repo.DepsLoaded {
		return nil
	}
	target, err := i.ensureRepoPackageDownloaded(*pkg.Repo)
	if err != nil {
		return err
	}
	desc, err := i.readDescriptionFromCachedPath(target)
	if err != nil {
		return err
	}
	record := *pkg.Repo
	record.Dependencies = desc.Dependencies
	record.NeedsCompilation = desc.NeedsCompilation
	record.DepsLoaded = true
	pkg.Repo = &record
	pkg.Deps = desc.Dependencies
	i.planned[name] = pkg
	i.replaceRepoCandidate(name, record)
	return nil
}

func (i *nativeInstaller) installPackageBatch(names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) == 1 {
		installed, err := i.installPlannedPackage(names[0])
		if err != nil || !installed {
			return nil, err
		}
		return []string{names[0]}, nil
	}

	type installResult struct {
		name      string
		installed bool
		err       error
	}

	workers := len(names)
	if workers > 4 {
		workers = 4
	}
	jobs := make(chan string)
	results := make(chan installResult, len(names))

	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for name := range jobs {
				installed, err := i.installPlannedPackage(name)
				results <- installResult{name: name, installed: installed, err: err}
			}
		}()
	}

	for _, name := range names {
		jobs <- name
	}
	close(jobs)
	wg.Wait()
	close(results)

	byName := make(map[string]installResult, len(names))
	for result := range results {
		byName[result.name] = result
	}

	installed := make([]string, 0, len(names))
	for _, name := range names {
		result, ok := byName[name]
		if !ok {
			return nil, fmt.Errorf("install batch did not return a result for package %s", name)
		}
		if result.err != nil {
			return nil, result.err
		}
		if result.installed {
			installed = append(installed, name)
		}
	}
	return installed, nil
}

func newInstaller(req Request, prepareLibrary bool) (*nativeInstaller, error) {
	stdout := req.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := req.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	tempRoot, err := os.MkdirTemp("", "gr-native-install-*")
	if err != nil {
		return nil, fmt.Errorf("create native installer temp dir: %w", err)
	}

	metaDir := filepath.Join(req.LibraryPath, ".rs-source-meta")
	downloadRoot := filepath.Join(tempRoot, "downloads")
	if strings.TrimSpace(req.CacheRoot) != "" {
		downloadRoot = filepath.Join(req.CacheRoot, "downloads")
	}
	if prepareLibrary {
		if err := os.MkdirAll(metaDir, 0o755); err != nil {
			return nil, fmt.Errorf("create source metadata dir: %w", err)
		}
		if err := os.MkdirAll(req.LibraryPath, 0o755); err != nil {
			return nil, fmt.Errorf("create managed library: %w", err)
		}
	}

	inst := nativeInstaller{
		req:               req,
		tempRoot:          tempRoot,
		downloadRoot:      downloadRoot,
		metaDir:           metaDir,
		stdout:            stdout,
		stderr:            stderr,
		planned:           map[string]plannedPackage{},
		resolving:         map[string]bool{},
		resolved:          map[string]bool{},
		sourceCache:       map[string]preparedSource{},
		httpClient:        &http.Client{Timeout: defaultHTTPTimeout},
		requirements:      map[string][]constraintRequest{},
		cranArchiveLoaded: map[string]bool{},
		selectedVersions:  map[string]string{},
		prefetchedRepo:    map[string]string{},
	}
	if err := os.MkdirAll(inst.downloadRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create download cache dir: %w", err)
	}

	rBinary, siblingFound := resolveSiblingRBinary(req.Interpreter)
	if req.Runtime.RVersion == "" || !siblingFound {
		inspectedBinary, version, err := inspectRRuntime(req.Interpreter, req.WorkDir, stderr)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(rBinary) == "" {
			rBinary = inspectedBinary
		}
		if req.Runtime.RVersion == "" {
			inst.req.Runtime.RVersion = version
		}
	}
	if strings.TrimSpace(rBinary) == "" {
		return nil, fmt.Errorf("resolve R binary: empty result")
	}
	inst.rBinary = rBinary
	if len(inst.req.Environment) == 0 {
		inst.req.Environment = os.Environ()
	}

	if err := inst.loadInstalledPackages(); err != nil {
		return nil, err
	}

	return &inst, nil
}

func (i *nativeInstaller) plan() error {
	roots := mergeDeps(i.req.CRANDeps, i.req.BiocDeps, sourceDepNames(i.req.SourceDeps))
	return i.planRoots(roots, 0)
}

func (i *nativeInstaller) planRoots(roots []string, idx int) error {
	if idx >= len(roots) {
		return nil
	}
	name := roots[idx]
	hint := i.hintForRoot(name)
	if _, ok := i.req.SourceDeps[name]; ok {
		state := i.snapshotPlanningState()
		if err := i.planPackage(name, hint, "", nil, nil, nil); err != nil {
			i.restorePlanningState(state)
			return err
		}
		if err := i.planRoots(roots, idx+1); err != nil {
			i.restorePlanningState(state)
			return err
		}
		return nil
	}

	candidates, err := i.resolveRepoCandidates(name, hint)
	if err != nil {
		return err
	}
	state := i.snapshotPlanningState()
	var lastConflict error
	for _, candidate := range candidates {
		i.restorePlanningState(state)
		i.selectedVersions[name] = candidate.Version
		if err := i.planPackage(name, hint, "", nil, nil, nil); err != nil {
			var conflict ConstraintConflictError
			if errors.As(err, &conflict) {
				lastConflict = err
				delete(i.selectedVersions, name)
				continue
			}
			return err
		}
		if err := i.planRoots(roots, idx+1); err != nil {
			var conflict ConstraintConflictError
			if errors.As(err, &conflict) {
				lastConflict = err
				delete(i.selectedVersions, name)
				continue
			}
			return err
		}
		return nil
	}
	i.restorePlanningState(state)
	if lastConflict != nil {
		return lastConflict
	}
	return fmt.Errorf("package %s not found in CRAN or Bioconductor indexes", name)
}

func (i *nativeInstaller) hintForRoot(name string) repoHint {
	if _, ok := i.req.SourceDeps[name]; ok {
		return hintAny
	}
	if slices.Contains(i.req.BiocDeps, name) {
		return hintBioc
	}
	if slices.Contains(i.req.CRANDeps, name) {
		return hintCRAN
	}
	return hintAny
}

func (i *nativeInstaller) planPackage(name string, hint repoHint, requiredBy string, constraints []versionConstraint, chain []string, skips versionSkips) error {
	if rdeps.IsBundledPackage(name) {
		return nil
	}
	if len(constraints) > 0 {
		i.requirements[name] = append(i.requirements[name], constraintRequest{
			RequiredBy:  requiredBy,
			Constraints: append([]versionConstraint(nil), constraints...),
			Chain:       append([]string(nil), chain...),
		})
	}
	if i.resolved[name] {
		return i.validatePackageRequirements(i.planned[name])
	}
	if i.resolving[name] {
		return fmt.Errorf("circular dependency detected involving %s", name)
	}
	i.resolving[name] = true
	defer delete(i.resolving, name)

	if spec, ok := i.req.SourceDeps[name]; ok {
		prepared, err := i.prepareSource(spec)
		if err != nil {
			return err
		}
		pkg := plannedPackage{
			Name:     name,
			Version:  prepared.Version,
			Source:   prepared.Source,
			Deps:     prepared.Dependencies,
			Prepared: &prepared,
		}
		if err := i.validatePackageRequirements(pkg); err != nil {
			return err
		}
		return i.planConcretePackage(pkg, chain, skips)
	}

	candidates, err := i.resolveRepoCandidates(name, hint)
	if err != nil {
		return err
	}
	if forced := strings.TrimSpace(i.selectedVersions[name]); forced != "" {
		filtered := make([]repoRecord, 0, 1)
		for _, candidate := range candidates {
			if candidate.Version == forced {
				filtered = append(filtered, candidate)
				break
			}
		}
		if len(filtered) == 0 {
			return fmt.Errorf("package %s selected version %s is not available in current repository candidates", name, forced)
		}
		candidates = filtered
	}

	state := i.snapshotPlanningState()
	var lastConflict error
	sawCandidate := false
	triedCandidate := false
	for _, candidate := range candidates {
		sawCandidate = true
		if isVersionSkipped(skips, name, candidate.Version) {
			continue
		}
		triedCandidate = true
		i.restorePlanningState(state)
		if err := i.populateRepoRecordDependencies(&candidate); err != nil {
			return err
		}
		pkg := plannedPackage{
			Name:    name,
			Version: candidate.Version,
			Source:  candidate.Source,
			Deps:    candidate.Dependencies,
			Repo:    &candidate,
		}
		if err := i.validatePackageRequirements(pkg); err != nil {
			var conflict ConstraintConflictError
			if errors.As(err, &conflict) {
				lastConflict = err
				continue
			}
			return err
		}
		if err := i.planConcretePackage(pkg, chain, skips); err != nil {
			var conflictErr ConstraintConflictError
			if errors.As(err, &conflictErr) {
				lastConflict = err
				continue
			}
			return err
		}
		return nil
	}

	i.restorePlanningState(state)
	if lastConflict != nil {
		return lastConflict
	}
	if sawCandidate && !triedCandidate {
		return exhaustedCandidatesError{Package: name}
	}
	return fmt.Errorf("package %s not found in CRAN or Bioconductor indexes", name)
}

func (i *nativeInstaller) planConcretePackage(pkg plannedPackage, chain []string, skips versionSkips) error {
	if err := i.validatePackageRequirements(pkg); err != nil {
		return err
	}
	if err := i.validateBuildPrerequisites(pkg); err != nil {
		return err
	}
	i.planned[pkg.Name] = pkg
	childChain := append(append([]string(nil), chain...), pkg.Name)
	if err := i.planDependencyList(pkg.Deps, 0, pkg.Name, childChain, skips); err != nil {
		return err
	}
	i.resolved[pkg.Name] = true
	i.order = append(i.order, pkg.Name)
	return nil
}

func (i *nativeInstaller) validateBuildPrerequisites(pkg plannedPackage) error {
	needsCompilation := false
	switch {
	case pkg.Repo != nil:
		needsCompilation = pkg.Repo.NeedsCompilation
	case pkg.Prepared != nil:
		needsCompilation = pkg.Prepared.NeedsCompilation
	}
	if !needsCompilation {
		return nil
	}
	if i.buildToolsChecked {
		return nil
	}
	if err := i.ensurePackageBuildTools(pkg.Name); err != nil {
		return err
	}
	i.buildToolsChecked = true
	return nil
}

func (i *nativeInstaller) planDependencyList(deps []packageRequirement, idx int, parentName string, chain []string, skips versionSkips) error {
	if idx >= len(deps) {
		return nil
	}

	dep := deps[idx]
	state := i.snapshotPlanningState()
	localSkips := cloneVersionSkips(skips)
	if localSkips == nil {
		localSkips = versionSkips{}
	}
	var lastConflict error

	for {
		i.restorePlanningState(state)
		if err := i.planPackage(dep.Name, hintAny, parentName, dep.Constraints, chain, localSkips); err != nil {
			var conflict ConstraintConflictError
			if errors.As(err, &conflict) {
				lastConflict = err
				if !addSkippedVersion(localSkips, conflict.Package, conflict.Version) {
					return lastConflict
				}
				continue
			}
			var exhausted exhaustedCandidatesError
			if errors.As(err, &exhausted) && lastConflict != nil {
				return lastConflict
			}
			return fmt.Errorf("resolve dependency %s for %s: %w", dep.Name, parentName, err)
		}

		if err := i.planDependencyList(deps, idx+1, parentName, chain, localSkips); err != nil {
			var conflict ConstraintConflictError
			if errors.As(err, &conflict) {
				lastConflict = err
				if !addSkippedVersion(localSkips, conflict.Package, conflict.Version) {
					return lastConflict
				}
				continue
			}
			var exhausted exhaustedCandidatesError
			if errors.As(err, &exhausted) && lastConflict != nil {
				return lastConflict
			}
			return err
		}
		return nil
	}
}

func isVersionSkipped(skips versionSkips, pkg, version string) bool {
	if len(skips) == 0 || version == "" {
		return false
	}
	versions := skips[pkg]
	if len(versions) == 0 {
		return false
	}
	_, ok := versions[version]
	return ok
}

func cloneVersionSkips(skips versionSkips) versionSkips {
	if len(skips) == 0 {
		return nil
	}
	cloned := make(versionSkips, len(skips))
	for pkg, versions := range skips {
		if len(versions) == 0 {
			continue
		}
		clonedVersions := make(map[string]struct{}, len(versions))
		for version := range versions {
			clonedVersions[version] = struct{}{}
		}
		cloned[pkg] = clonedVersions
	}
	return cloned
}

func addSkippedVersion(skips versionSkips, pkg, version string) bool {
	if version == "" {
		return false
	}
	if skips[pkg] == nil {
		skips[pkg] = map[string]struct{}{}
	}
	if _, ok := skips[pkg][version]; ok {
		return false
	}
	skips[pkg][version] = struct{}{}
	return true
}

func (i *nativeInstaller) validatePackageRequirements(pkg plannedPackage) error {
	requests := i.requirements[pkg.Name]
	for _, request := range requests {
		for _, constraint := range request.Constraints {
			if versionSatisfies(pkg.Version, constraint) {
				continue
			}
			return ConstraintConflictError{
				Package:     pkg.Name,
				Version:     pkg.Version,
				RequiredBy:  request.RequiredBy,
				Operator:    constraint.Operator,
				Requirement: constraint.Version,
				Chain:       append([]string(nil), request.Chain...),
			}
		}
	}
	return nil
}

func (i *nativeInstaller) hasSatisfyingCandidate(name string, candidates []repoRecord) bool {
	for _, candidate := range candidates {
		if err := i.validatePackageRequirements(plannedPackage{Name: name, Version: candidate.Version}); err == nil {
			return true
		}
	}
	return false
}

func (i *nativeInstaller) snapshotPlanningState() planningState {
	planned := make(map[string]plannedPackage, len(i.planned))
	for name, pkg := range i.planned {
		planned[name] = pkg
	}
	resolved := make(map[string]bool, len(i.resolved))
	for name, value := range i.resolved {
		resolved[name] = value
	}
	requirements := make(map[string][]constraintRequest, len(i.requirements))
	for name, requests := range i.requirements {
		clonedRequests := make([]constraintRequest, 0, len(requests))
		for _, request := range requests {
			clonedRequests = append(clonedRequests, constraintRequest{
				RequiredBy:  request.RequiredBy,
				Constraints: append([]versionConstraint(nil), request.Constraints...),
				Chain:       append([]string(nil), request.Chain...),
			})
		}
		requirements[name] = clonedRequests
	}
	selectedVersions := make(map[string]string, len(i.selectedVersions))
	for name, version := range i.selectedVersions {
		selectedVersions[name] = version
	}
	return planningState{
		planned:          planned,
		resolved:         resolved,
		order:            append([]string(nil), i.order...),
		requirements:     requirements,
		selectedVersions: selectedVersions,
	}
}

func (i *nativeInstaller) restorePlanningState(state planningState) {
	i.planned = state.planned
	i.resolved = state.resolved
	i.order = state.order
	i.requirements = state.requirements
	i.selectedVersions = state.selectedVersions
}

func (i *nativeInstaller) selectRepoRecord(name string, candidates []repoRecord) (repoRecord, bool) {
	if len(candidates) == 0 {
		return repoRecord{}, false
	}
	for _, candidate := range candidates {
		pkg := plannedPackage{
			Name:    name,
			Version: candidate.Version,
		}
		if err := i.validatePackageRequirements(pkg); err != nil {
			continue
		}
		if err := i.populateRepoRecordDependencies(&candidate); err != nil {
			continue
		}
		return candidate, true
	}
	return repoRecord{}, false
}

func (i *nativeInstaller) populateRepoRecordDependencies(record *repoRecord) error {
	if record == nil || record.DepsLoaded {
		return nil
	}
	desc, err := i.readRepoRecordDescription(*record)
	if err != nil {
		return err
	}
	record.Dependencies = desc.Dependencies
	record.NeedsCompilation = desc.NeedsCompilation
	record.DepsLoaded = true
	i.replaceRepoCandidate(record.Name, *record)
	return nil
}

func (i *nativeInstaller) readRepoRecordDescription(record repoRecord) (description, error) {
	if cached := i.repoDownloadPath(record); cached != "" {
		if info, err := os.Stat(cached); err == nil && !info.IsDir() && info.Size() > 0 {
			return i.readDescriptionFromCachedPath(cached)
		}
	}
	key := "url:" + strings.TrimSpace(record.TarballURL)
	if desc, ok := i.cachedDescription(key); ok {
		return desc, nil
	}
	desc, err := readDescriptionFromTarballURL(i.httpClient, record.TarballURL)
	if err != nil {
		return description{}, err
	}
	i.storeCachedDescription(key, desc)
	return desc, nil
}

func (i *nativeInstaller) replaceRepoCandidate(name string, record repoRecord) {
	indexes := []*map[string][]repoRecord{
		&i.cranIndex,
		&i.biocIndex,
		&i.biocAnnIndex,
		&i.biocExpIndex,
	}
	for _, index := range indexes {
		candidates := (*index)[name]
		for idx := range candidates {
			if candidates[idx].Version == record.Version && candidates[idx].TarballURL == record.TarballURL {
				candidates[idx] = record
				(*index)[name] = candidates
				return
			}
		}
	}
}

func (i *nativeInstaller) resolveRepoCandidates(name string, hint repoHint) ([]repoRecord, error) {
	tryBiocFirst := hint == hintBioc || (hint == hintAny && rdeps.IsKnownBiocPackage(name))

	if tryBiocFirst {
		if err := i.ensureBiocMainIndex(); err != nil {
			return nil, err
		}
		if candidates := i.biocIndex[name]; len(candidates) > 0 {
			return append([]repoRecord(nil), candidates...), nil
		}
		if err := i.ensureBiocAnnotationIndex(); err == nil {
			if candidates := i.biocAnnIndex[name]; len(candidates) > 0 {
				return append([]repoRecord(nil), candidates...), nil
			}
		}
		if err := i.ensureBiocExperimentIndex(); err == nil {
			if candidates := i.biocExpIndex[name]; len(candidates) > 0 {
				return append([]repoRecord(nil), candidates...), nil
			}
		}
		if hint == hintBioc {
			return nil, fmt.Errorf("package %s not found in configured Bioconductor repositories", name)
		}
	}

	if err := i.ensureCRANIndex(); err != nil {
		return nil, err
	}
	if candidates := i.cranIndex[name]; len(candidates) > 0 {
		if !i.hasSatisfyingCandidate(name, candidates) {
			if err := i.ensureCRANArchiveCandidates(name); err == nil {
				candidates = i.cranIndex[name]
			}
		}
		return append([]repoRecord(nil), candidates...), nil
	}
	if err := i.ensureCRANArchiveCandidates(name); err == nil {
		if candidates := i.cranIndex[name]; len(candidates) > 0 {
			return append([]repoRecord(nil), candidates...), nil
		}
	}

	if !tryBiocFirst {
		if err := i.ensureBiocMainIndex(); err == nil {
			if candidates := i.biocIndex[name]; len(candidates) > 0 {
				return append([]repoRecord(nil), candidates...), nil
			}
		}
		if err := i.ensureBiocAnnotationIndex(); err == nil {
			if candidates := i.biocAnnIndex[name]; len(candidates) > 0 {
				return append([]repoRecord(nil), candidates...), nil
			}
		}
		if err := i.ensureBiocExperimentIndex(); err == nil {
			if candidates := i.biocExpIndex[name]; len(candidates) > 0 {
				return append([]repoRecord(nil), candidates...), nil
			}
		}
	}

	return nil, fmt.Errorf("package %s not found in CRAN or Bioconductor indexes", name)
}

func (i *nativeInstaller) prepareSource(spec project.SourceSpec) (preparedSource, error) {
	if cached, ok := i.sourceCache[spec.Package]; ok {
		return cached, nil
	}

	var prepared preparedSource
	switch spec.Type {
	case sourceLocal:
		desc, err := readDescriptionFromPath(spec.Path)
		if err != nil {
			return preparedSource{}, fmt.Errorf("read local source %s: %w", spec.Package, err)
		}
		if desc.Package != spec.Package {
			return preparedSource{}, fmt.Errorf("local source %s provides package %s", spec.Package, desc.Package)
		}
		kind, fingerprint := describeLocalFingerprint(spec.Path)
		prepared = preparedSource{
			Name:             desc.Package,
			Version:          desc.Version,
			Source:           sourceLocal,
			Location:         spec.Path,
			Fingerprint:      fingerprint,
			FingerprintKind:  kind,
			Dependencies:     desc.Dependencies,
			InstallPath:      spec.Path,
			NeedsCompilation: desc.NeedsCompilation,
		}
	case sourceGit:
		cloneDir, err := i.cloneGitSource(spec.URL, spec.Ref, spec.Package, "")
		if err != nil {
			return preparedSource{}, err
		}
		installPath := cloneDir
		if spec.Subdir != "" {
			installPath = filepath.Join(cloneDir, filepath.FromSlash(spec.Subdir))
		}
		desc, err := readDescriptionFromPath(installPath)
		if err != nil {
			return preparedSource{}, fmt.Errorf("read git source %s: %w", spec.Package, err)
		}
		if desc.Package != spec.Package {
			return preparedSource{}, fmt.Errorf("git source %s provides package %s", spec.Package, desc.Package)
		}
		commit, err := gitOutput(cloneDir, "rev-parse", "HEAD")
		if err != nil {
			return preparedSource{}, fmt.Errorf("resolve git commit for %s: %w", spec.Package, err)
		}
		prepared = preparedSource{
			Name:             desc.Package,
			Version:          desc.Version,
			Source:           sourceGit,
			Location:         spec.URL,
			Ref:              spec.Ref,
			Commit:           strings.TrimSpace(commit),
			Subdir:           spec.Subdir,
			Dependencies:     desc.Dependencies,
			InstallPath:      installPath,
			NeedsCompilation: desc.NeedsCompilation,
		}
	case sourceGitHub:
		cloneURL, metaHost, err := githubCloneURL(spec)
		if err != nil {
			return preparedSource{}, err
		}
		cloneDir, err := i.cloneGitSource(cloneURL, spec.Ref, spec.Package, spec.TokenEnv)
		if err != nil {
			return preparedSource{}, err
		}
		installPath := cloneDir
		if spec.Subdir != "" {
			installPath = filepath.Join(cloneDir, filepath.FromSlash(spec.Subdir))
		}
		desc, err := readDescriptionFromPath(installPath)
		if err != nil {
			return preparedSource{}, fmt.Errorf("read github source %s: %w", spec.Package, err)
		}
		if desc.Package != spec.Package {
			return preparedSource{}, fmt.Errorf("github source %s provides package %s", spec.Package, desc.Package)
		}
		commit, err := gitOutput(cloneDir, "rev-parse", "HEAD")
		if err != nil {
			return preparedSource{}, fmt.Errorf("resolve github commit for %s: %w", spec.Package, err)
		}
		prepared = preparedSource{
			Name:             desc.Package,
			Version:          desc.Version,
			Source:           sourceGitHub,
			Host:             spec.Host,
			Location:         spec.Repo,
			Ref:              spec.Ref,
			Commit:           strings.TrimSpace(commit),
			Subdir:           spec.Subdir,
			Dependencies:     desc.Dependencies,
			InstallPath:      installPath,
			NeedsCompilation: desc.NeedsCompilation,
		}
		if prepared.Host == "" {
			prepared.Host = metaHost
		}
	default:
		return preparedSource{}, fmt.Errorf("unsupported source type %q for native installer", spec.Type)
	}

	i.sourceCache[spec.Package] = prepared
	return prepared, nil
}

func (i *nativeInstaller) installRepoPackage(record repoRecord) error {
	fmt.Fprintf(i.stdout, "[rs] installing %s package %s %s via native backend\n", record.Source, record.Name, record.Version)
	if strings.HasSuffix(strings.ToLower(record.TarballURL), ".tar.gz") && record.NeedsCompilation {
		if err := i.ensurePackageBuildTools(record.Name); err != nil {
			return err
		}
	}
	target, err := i.ensureRepoPackageDownloaded(record)
	if err != nil {
		return fmt.Errorf("download %s: %w", record.Name, err)
	}
	if strings.HasSuffix(strings.ToLower(record.TarballURL), ".tar.gz") {
		needsCompilation := record.NeedsCompilation
		if !record.DepsLoaded {
			desc, err := i.readDescriptionFromCachedPath(target)
			if err != nil {
				return fmt.Errorf("inspect %s source package: %w", record.Name, err)
			}
			needsCompilation = desc.NeedsCompilation
		}
		if needsCompilation && !record.NeedsCompilation {
			if err := i.ensurePackageBuildTools(record.Name); err != nil {
				return err
			}
		}
	}
	if err := i.runRCommandInstall(record.Name, target); err != nil {
		return fmt.Errorf("install %s from %s: %w", record.Name, record.Source, err)
	}
	if err := removeSourceMetadata(i.metaDir, record.Name); err != nil {
		return err
	}
	return nil
}

func (i *nativeInstaller) ensureRepoPackageDownloaded(record repoRecord) (string, error) {
	rawURL := strings.TrimSpace(record.TarballURL)
	if rawURL == "" {
		return "", fmt.Errorf("package %s has no download URL", record.Name)
	}

	if target, ok := i.repoDownloadReadyPath(record); ok {
		return target, nil
	}

	target, err := i.download(rawURL, repoDownloadName(record))
	if err != nil {
		return "", err
	}

	i.prefetchedMu.Lock()
	i.prefetchedRepo[rawURL] = target
	i.prefetchedMu.Unlock()
	return target, nil
}

func (i *nativeInstaller) repoDownloadReadyPath(record repoRecord) (string, bool) {
	rawURL := strings.TrimSpace(record.TarballURL)
	if rawURL == "" {
		return "", false
	}

	i.prefetchedMu.RLock()
	target, ok := i.prefetchedRepo[rawURL]
	i.prefetchedMu.RUnlock()
	if ok {
		if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
			return target, true
		}
	}

	target = i.repoDownloadPath(record)
	if strings.TrimSpace(target) == "" {
		return "", false
	}
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
		if err := validateCachedDownload(target); err == nil {
			return target, true
		}
	}
	return "", false
}

func (i *nativeInstaller) repoDownloadPath(record repoRecord) string {
	rawURL := strings.TrimSpace(record.TarballURL)
	if rawURL == "" || strings.TrimSpace(i.downloadRoot) == "" {
		return ""
	}
	return filepath.Join(i.downloadRoot, downloadCacheName(rawURL, repoDownloadName(record)))
}

func repoDownloadName(record repoRecord) string {
	ext := ".tar.gz"
	lower := strings.ToLower(record.TarballURL)
	switch {
	case strings.HasSuffix(lower, ".zip"):
		ext = ".zip"
	case strings.HasSuffix(lower, ".tgz"):
		ext = ".tgz"
	}
	return fmt.Sprintf("%s_%s%s", record.Name, record.Version, ext)
}

func descriptionSidecarPath(target string) string {
	if strings.TrimSpace(target) == "" {
		return ""
	}
	return target + ".description.json"
}

func (i *nativeInstaller) readDescriptionFromCachedPath(path string) (description, error) {
	key := "path:" + path
	if desc, ok := i.cachedDescription(key); ok {
		return desc, nil
	}
	desc, err := readDescriptionFromPath(path)
	if err != nil {
		return description{}, err
	}
	i.storeCachedDescription(key, desc)
	return desc, nil
}

func (i *nativeInstaller) cachedDescription(key string) (description, bool) {
	if strings.TrimSpace(key) == "" {
		return description{}, false
	}
	i.descriptionMu.RLock()
	desc, ok := i.descriptionCache[key]
	i.descriptionMu.RUnlock()
	return desc, ok
}

func (i *nativeInstaller) storeCachedDescription(key string, desc description) {
	if strings.TrimSpace(key) == "" {
		return
	}
	i.descriptionMu.Lock()
	if i.descriptionCache == nil {
		i.descriptionCache = map[string]description{}
	}
	i.descriptionCache[key] = desc
	i.descriptionMu.Unlock()
}

func (i *nativeInstaller) installPreparedSource(prepared preparedSource) error {
	switch prepared.Source {
	case sourceLocal:
		fmt.Fprintf(i.stdout, "[rs] installing local package %s from %s via native backend\n", prepared.Name, prepared.Location)
	case sourceGit:
		fmt.Fprintf(i.stdout, "[rs] installing git package %s from %s via native backend\n", prepared.Name, prepared.Location)
	case sourceGitHub:
		label := prepared.Location
		if prepared.Ref != "" {
			label += "@" + prepared.Ref
		}
		fmt.Fprintf(i.stdout, "[rs] installing github package %s from %s via native backend\n", prepared.Name, label)
	}
	if prepared.NeedsCompilation {
		if err := i.ensurePackageBuildTools(prepared.Name); err != nil {
			return err
		}
	}
	if err := i.runRCommandInstall(prepared.Name, prepared.InstallPath); err != nil {
		return fmt.Errorf("install %s from %s source: %w", prepared.Name, prepared.Source, err)
	}
	return writeSourceMetadata(i.metaDir, prepared.Name, prepared)
}

func (i *nativeInstaller) runRCommandInstall(packageName, target string) error {
	installTarget, err := i.prepareInstallTarget(packageName, target)
	if err != nil {
		return err
	}
	cmd, err := buildInstallCommand(i.rBinary, i.req.WorkDir, i.req.CacheRoot, i.req.LibraryPath, i.req.Environment, packageName, installTarget)
	if err != nil {
		return err
	}
	label := fmt.Sprintf("installing R package %s", filepath.Base(target))
	if err := progresscmd.Run(cmd, label, i.stderr, i.stderr); err != nil {
		return err
	}
	return nil
}

func buildInstallCommand(rBinary, workDir, cacheRoot, libraryPath string, env []string, packageName, target string) (*exec.Cmd, error) {
	installEnv := withInstallEnv(withPackageNativeFixups(withLibraryEnv(env, libraryPath), packageName), cacheRoot)
	wrappedName, wrappedArgs, wrappedEnv, _, err := toolchainenv.WrapCommand(
		rBinary,
		[]string{"CMD", "INSTALL", "-l", libraryPath, target},
		installEnv,
	)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(wrappedName, wrappedArgs...)
	cmd.Dir = workDir
	cmd.Env = wrappedEnv
	return cmd, nil
}

func (i *nativeInstaller) prepareInstallTarget(packageName, target string) (string, error) {
	categories := toolchainenv.NativeCategoriesForPackages([]string{packageName})
	if !slices.Contains(categories, "encoding") {
		return target, nil
	}
	libDir := encodingLibraryDir(i.req.Environment)
	if libDir == "" {
		return target, nil
	}

	info, err := os.Stat(target)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return target, nil
	}

	sourceRoot, err := unpackSourceTarget(target, i.tempRoot)
	if err != nil {
		return "", err
	}
	if err := patchEncodingMakevars(sourceRoot, libDir); err != nil {
		return "", err
	}
	return sourceRoot, nil
}

func encodingLibraryDir(env []string) string {
	for _, prefix := range toolchainenv.PrefixesFromEnv(env) {
		libDir := filepath.Join(strings.TrimSpace(prefix), "lib")
		entries, err := os.ReadDir(libDir)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			name := strings.ToLower(strings.TrimSpace(entry.Name()))
			if name == "libiconv.so" || strings.HasPrefix(name, "libiconv.so.") || name == "libiconv.dylib" || strings.HasPrefix(name, "libiconv.dylib.") || name == "libiconv.a" {
				return libDir
			}
		}
	}
	return ""
}

func unpackSourceTarget(target, tempRoot string) (string, error) {
	dest, err := os.MkdirTemp(tempRoot, "patched-source-*")
	if err != nil {
		return "", err
	}

	lower := strings.ToLower(target)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		if err := unpackTarGz(target, dest); err != nil {
			return "", err
		}
	case strings.HasSuffix(lower, ".zip"):
		if err := unpackZipArchive(target, dest); err != nil {
			return "", err
		}
	default:
		return target, nil
	}
	return unpackedSourceRoot(dest)
}

func unpackedSourceRoot(dest string) (string, error) {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return "", err
	}
	dirs := make([]fs.DirEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry)
		}
	}
	if len(dirs) == 1 {
		return filepath.Join(dest, dirs[0].Name()), nil
	}
	return dest, nil
}

func unpackTarGz(target, dest string) error {
	file, err := os.Open(target)
	if err != nil {
		return err
	}
	defer file.Close()

	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		path, err := archiveEntryPath(dest, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, fs.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(file, tarReader); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
		}
	}
}

func unpackZipArchive(target, dest string) error {
	reader, err := zip.OpenReader(target)
	if err != nil {
		return err
	}
	defer reader.Close()

	for _, file := range reader.File {
		path, err := archiveEntryPath(dest, file.Name)
		if err != nil {
			return err
		}
		if file.FileInfo().IsDir() {
			if err := os.MkdirAll(path, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		rc, err := file.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
		if err != nil {
			rc.Close()
			return err
		}
		if _, err := io.Copy(out, rc); err != nil {
			out.Close()
			rc.Close()
			return err
		}
		if err := out.Close(); err != nil {
			rc.Close()
			return err
		}
		if err := rc.Close(); err != nil {
			return err
		}
	}
	return nil
}

func archiveEntryPath(dest, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || clean == string(filepath.Separator) {
		return dest, nil
	}
	full := filepath.Join(dest, clean)
	rel, err := filepath.Rel(dest, full)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("archive entry escapes destination: %s", name)
	}
	return full, nil
}

func patchEncodingMakevars(sourceRoot, libDir string) error {
	flags := []string{"-L" + libDir, "-liconv"}
	patched := false
	for _, relative := range []string{"src/Makevars.in", "src/Makevars"} {
		path := filepath.Join(sourceRoot, filepath.FromSlash(relative))
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := prependMakevarsFlags(path, "PKG_LIBS", flags); err != nil {
			return err
		}
		patched = true
	}
	if patched {
		return nil
	}
	return fmt.Errorf("encoding fixup could not find src/Makevars(.in) under %s", sourceRoot)
}

func prependMakevarsFlags(path, variable string, flags []string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := strings.Split(string(data), "\n")
	needle := strings.Join(flags, " ")
	changed := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, variable) {
			continue
		}
		if strings.Contains(line, needle) {
			return nil
		}
		assign := strings.Index(line, "=")
		if assign < 0 {
			continue
		}
		prefix := strings.TrimRight(line[:assign+1], " \t")
		value := strings.TrimSpace(line[assign+1:])
		switch {
		case strings.Contains(value, "@libs@"):
			value = strings.Replace(value, "@libs@", needle+" @libs@", 1)
		case value == "":
			value = needle
		default:
			value = needle + " " + value
		}
		lines[i] = prefix + " " + value
		changed = true
		break
	}

	if !changed {
		lines = append(lines, variable+" += "+needle)
	}
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644)
}

func withPackageNativeFixups(env []string, packageName string) []string {
	categories := toolchainenv.NativeCategoriesForPackages([]string{packageName})
	if len(categories) == 0 {
		return env
	}

	prefixes := toolchainenv.PrefixesFromEnv(env)
	pkgConfigPaths := toolchainenv.PkgConfigPathsFromEnv(env)
	plan := toolchainenv.BuildNativeFixupPlanWithEnv(env, prefixes, pkgConfigPaths, categories)
	if len(plan.CPPFLAGS) == 0 && len(plan.LDFLAGS) == 0 && len(plan.LIBS) == 0 {
		return env
	}
	return withMakeLinkerFixups(toolchainenv.ApplyWithPlan(env, prefixes, pkgConfigPaths, plan), prefixes, plan)
}

func withMakeLinkerFixups(env, prefixes []string, plan toolchainenv.NativeFixupPlan) []string {
	flags := make([]string, 0, len(plan.LDFLAGS)+len(plan.LIBS))
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		flags = append(flags, "-L"+filepath.Join(prefix, "lib"))
	}
	flags = append(flags, plan.LDFLAGS...)
	flags = append(flags, plan.LIBS...)
	if len(flags) == 0 {
		return env
	}

	current := ""
	filtered := make([]string, 0, len(env)+1)
	for _, entry := range env {
		if strings.HasPrefix(entry, "SAN_LIBS=") {
			current = strings.TrimSpace(strings.TrimPrefix(entry, "SAN_LIBS="))
			continue
		}
		filtered = append(filtered, entry)
	}

	merged := make([]string, 0, len(strings.Fields(current))+len(flags))
	for _, flag := range strings.Fields(current) {
		if flag == "" || slices.Contains(merged, flag) {
			continue
		}
		merged = append(merged, flag)
	}
	for _, flag := range flags {
		flag = strings.TrimSpace(flag)
		if flag == "" || slices.Contains(merged, flag) {
			continue
		}
		merged = append(merged, flag)
	}
	if len(merged) == 0 {
		return filtered
	}
	return append(filtered, "SAN_LIBS="+strings.Join(merged, " "))
}

func (i *nativeInstaller) download(rawURL, name string) (string, error) {
	target := filepath.Join(i.downloadRoot, downloadCacheName(rawURL, name))
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
		if err := validateCachedDownload(target); err == nil {
			progresscmd.Stage(i.stderr, "reusing cached "+name)
			return target, nil
		}
		_ = os.Remove(target)
	}
	legacyTarget := filepath.Join(i.downloadRoot, legacyDownloadCacheName(rawURL, name))
	if info, err := os.Stat(legacyTarget); err == nil && !info.IsDir() && info.Size() > 0 {
		if err := validateCachedDownload(legacyTarget); err == nil {
			progresscmd.Stage(i.stderr, "reusing cached "+name)
			return legacyTarget, nil
		}
		_ = os.Remove(legacyTarget)
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return "", err
	}

	i.stage("downloading " + name)
	if err := downloadWithRetry(i.httpClient, rawURL, target, "downloading "+name, i.stderr); err != nil {
		return "", err
	}
	return target, nil
}

func downloadCacheName(rawURL, name string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return filepath.Join(fmt.Sprintf("%x", sum[:8]), name)
}

func legacyDownloadCacheName(rawURL, name string) string {
	sum := sha256.Sum256([]byte(rawURL))
	return fmt.Sprintf("%x-%s", sum[:8], name)
}

func (i *nativeInstaller) cloneGitSource(rawURL, ref, pkg, tokenEnv string) (string, error) {
	cloneURL := rawURL
	if tokenEnv != "" {
		token := strings.TrimSpace(os.Getenv(tokenEnv))
		if token == "" {
			return "", fmt.Errorf("source %q requires environment variable %s", pkg, tokenEnv)
		}
		cloneURL = injectGitToken(rawURL, token)
	}

	cloneDir := filepath.Join(i.tempRoot, fmt.Sprintf("%s-src-%d", pkg, time.Now().UnixNano()))
	if err := runCommand(i.req.WorkDir, i.stderr, i.stderr, "cloning "+pkg+" source", "git", "clone", cloneURL, cloneDir); err != nil {
		return "", fmt.Errorf("clone %s from %s: %w", pkg, rawURL, err)
	}
	if ref != "" {
		if err := runCommand(i.req.WorkDir, i.stderr, i.stderr, "checking out "+pkg+"@"+ref, "git", "-C", cloneDir, "checkout", ref); err != nil {
			return "", fmt.Errorf("checkout %s for %s: %w", ref, pkg, err)
		}
	}
	return cloneDir, nil
}

func (i *nativeInstaller) loadInstalledPackages() error {
	installed, err := loadInstalledPackagesFromLibrary(i.req.LibraryPath)
	if err != nil {
		return err
	}
	i.installedPackages = installed
	return nil
}

func loadInstalledPackagesFromLibrary(libraryPath string) (map[string]installedPackage, error) {
	entries, err := os.ReadDir(libraryPath)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]installedPackage{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read managed library: %w", err)
	}

	metaByName, err := readInstalledSourceMetadata(filepath.Join(libraryPath, ".rs-source-meta"))
	if err != nil {
		return nil, err
	}

	installed := map[string]installedPackage{}
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		descPath := filepath.Join(libraryPath, entry.Name(), "DESCRIPTION")
		data, err := os.ReadFile(descPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read installed DESCRIPTION for %s: %w", entry.Name(), err)
		}
		installed[entry.Name()] = installedPackageFromDescription(entry.Name(), data, metaByName[entry.Name()])
	}
	return installed, nil
}

func loadInstalledPackageFromLibrary(libraryPath, pkg string) (installedPackage, bool, error) {
	if strings.TrimSpace(libraryPath) == "" || strings.TrimSpace(pkg) == "" {
		return installedPackage{}, false, nil
	}
	descPath := filepath.Join(libraryPath, pkg, "DESCRIPTION")
	data, err := os.ReadFile(descPath)
	if errors.Is(err, os.ErrNotExist) {
		return installedPackage{}, false, nil
	}
	if err != nil {
		return installedPackage{}, false, fmt.Errorf("read installed DESCRIPTION for %s: %w", pkg, err)
	}
	meta, err := readInstalledSourceMetadataForPackage(filepath.Join(libraryPath, ".rs-source-meta"), pkg)
	if err != nil {
		return installedPackage{}, false, err
	}
	return installedPackageFromDescription(pkg, data, meta), true, nil
}

func installedPackageFromDescription(name string, data []byte, meta installedPackage) installedPackage {
	fields := map[string]string{}
	for _, record := range parseDCF(data) {
		for key, value := range record {
			fields[key] = value
		}
		break
	}
	pkg := installedPackage{
		Name:    name,
		Version: fields["Version"],
	}
	switch repository := fields["Repository"]; {
	case strings.EqualFold(repository, "CRAN"):
		pkg.Source = sourceCRAN
	case strings.Contains(strings.ToLower(repository), "bioconductor"):
		pkg.Source = sourceBioconductor
	}
	if meta.Source != "" {
		pkg.Source = meta.Source
	}
	pkg.Host = meta.Host
	pkg.Location = meta.Location
	pkg.Ref = meta.Ref
	pkg.Commit = meta.Commit
	pkg.Subdir = meta.Subdir
	pkg.Fingerprint = meta.Fingerprint
	pkg.FingerprintKind = meta.FingerprintKind
	return pkg
}

func copyInstalledPackage(sourceLibrary, targetLibrary, pkg string) error {
	sourcePath := filepath.Join(sourceLibrary, pkg)
	targetPath := filepath.Join(targetLibrary, pkg)
	if err := os.RemoveAll(targetPath); err != nil {
		return fmt.Errorf("clear target package dir for %s: %w", pkg, err)
	}
	return copyDirectoryTree(sourcePath, targetPath)
}

func copyInstalledPackageMetadata(sourceLibrary, targetMetaDir, pkg string) error {
	if strings.TrimSpace(targetMetaDir) == "" {
		return nil
	}
	sourceMeta := filepath.Join(sourceLibrary, ".rs-source-meta", pkg+".tsv")
	data, err := os.ReadFile(sourceMeta)
	if errors.Is(err, os.ErrNotExist) {
		return removeSourceMetadata(targetMetaDir, pkg)
	}
	if err != nil {
		return fmt.Errorf("read source metadata for %s: %w", pkg, err)
	}
	if err := os.MkdirAll(targetMetaDir, 0o755); err != nil {
		return fmt.Errorf("create source metadata dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(targetMetaDir, pkg+".tsv"), data, 0o644); err != nil {
		return fmt.Errorf("write source metadata for %s: %w", pkg, err)
	}
	return nil
}

func copyDirectoryTree(sourceRoot, targetRoot string) error {
	return filepath.WalkDir(sourceRoot, func(sourcePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(sourceRoot, sourcePath)
		if err != nil {
			return err
		}
		targetPath := targetRoot
		if rel != "." {
			targetPath = filepath.Join(targetRoot, rel)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		switch {
		case entry.IsDir():
			return os.MkdirAll(targetPath, info.Mode().Perm())
		case entry.Type()&os.ModeSymlink != 0:
			linkTarget, err := os.Readlink(sourcePath)
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			return os.Symlink(linkTarget, targetPath)
		default:
			if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
				return err
			}
			return hardLinkOrCopyFile(sourcePath, targetPath, info.Mode().Perm())
		}
	})
}

func hardLinkOrCopyFile(sourcePath, targetPath string, mode fs.FileMode) error {
	if err := os.Link(sourcePath, targetPath); err == nil {
		return nil
	}
	return copyFileWithMode(sourcePath, targetPath, mode)
}

func copyFileWithMode(sourcePath, targetPath string, mode fs.FileMode) error {
	in, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(targetPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

func (i *nativeInstaller) isPlannedPackageInstalled(pkg plannedPackage) bool {
	installed, ok := i.installedPackages[pkg.Name]
	if !ok {
		return false
	}
	return plannedPackageMatchesInstalled(pkg, installed)
}

func plannedPackageMatchesInstalled(pkg plannedPackage, installed installedPackage) bool {
	if pkg.Version == "" || installed.Version == "" || pkg.Version != installed.Version {
		return false
	}

	switch pkg.Source {
	case sourceCRAN, sourceBioconductor:
		return installed.Source == "" || installed.Source == pkg.Source
	case sourceLocal, sourceGit, sourceGitHub:
		if installed.Source != pkg.Source {
			return false
		}
		if pkg.Prepared == nil {
			return false
		}
		if pkg.Prepared.Host != "" && installed.Host != pkg.Prepared.Host {
			return false
		}
		if pkg.Prepared.Location != "" && installed.Location != pkg.Prepared.Location {
			return false
		}
		if pkg.Prepared.Ref != "" && installed.Ref != pkg.Prepared.Ref {
			return false
		}
		if pkg.Prepared.Commit != "" && installed.Commit != pkg.Prepared.Commit {
			return false
		}
		if pkg.Prepared.Subdir != "" && installed.Subdir != pkg.Prepared.Subdir {
			return false
		}
		if pkg.Prepared.FingerprintKind != "" && installed.FingerprintKind != pkg.Prepared.FingerprintKind {
			return false
		}
		if pkg.Prepared.Fingerprint != "" && installed.Fingerprint != pkg.Prepared.Fingerprint {
			return false
		}
		return true
	default:
		return false
	}
}

func installedPackageForPlanned(pkg plannedPackage) installedPackage {
	installed := installedPackage{
		Name:    pkg.Name,
		Version: pkg.Version,
		Source:  pkg.Source,
	}
	if pkg.Prepared == nil {
		return installed
	}
	installed.Host = pkg.Prepared.Host
	installed.Location = pkg.Prepared.Location
	installed.Ref = pkg.Prepared.Ref
	installed.Commit = pkg.Prepared.Commit
	installed.Subdir = pkg.Prepared.Subdir
	installed.Fingerprint = pkg.Prepared.Fingerprint
	installed.FingerprintKind = pkg.Prepared.FingerprintKind
	return installed
}

func (i *nativeInstaller) ensureCRANIndex() error {
	if i.cranIndex != nil {
		return nil
	}
	i.stage("fetching CRAN package index")
	index, err := fetchRepoIndexCached(i.httpClient, strings.TrimRight(i.req.Repo, "/"), sourceCRAN, i.req.Runtime.RVersion, i.repoIndexCacheDir())
	if err != nil {
		return fmt.Errorf("load CRAN index: %w", err)
	}
	i.cranIndex = index
	return nil
}

func (i *nativeInstaller) ensureCRANArchiveCandidates(name string) error {
	if i.cranArchiveLoaded[name] {
		return nil
	}
	i.cranArchiveLoaded[name] = true

	baseURL := strings.TrimRight(i.req.Repo, "/")
	archiveURL := baseURL + "/src/contrib/Archive/" + name + "/"
	i.stage("checking CRAN archive for " + name)
	body, err := fetchURLBytesCached(i.httpClient, archiveURL, archiveIndexCachePath(i.repoIndexCacheDir(), archiveURL))
	if err != nil {
		return err
	}
	versions := parseArchiveVersions(name, string(body))
	for _, version := range versions {
		appendRepoCandidate(i.cranIndex, repoRecord{
			Name:       name,
			Version:    version,
			TarballURL: fmt.Sprintf("%s%s_%s.tar.gz", archiveURL, name, version),
			Source:     sourceCRAN,
			DepsLoaded: false,
		})
	}
	return nil
}

func (i *nativeInstaller) ensureBiocMainIndex() error {
	if i.biocLoaded {
		return nil
	}
	i.stage("fetching Bioconductor package index")
	records, err := fetchRepoIndexCached(i.httpClient, biocMainRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor, i.req.Runtime.RVersion, i.repoIndexCacheDir())
	if err != nil {
		return fmt.Errorf("load Bioconductor index: %w", err)
	}
	i.biocLoaded = true
	i.biocIndex = records
	return nil
}

func (i *nativeInstaller) ensureBiocAnnotationIndex() error {
	if i.biocAnnLoaded {
		return nil
	}
	i.stage("fetching Bioconductor annotation index")
	records, err := fetchRepoIndexCached(i.httpClient, biocAnnotationRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor, i.req.Runtime.RVersion, i.repoIndexCacheDir())
	if err != nil {
		return fmt.Errorf("load Bioconductor annotation index: %w", err)
	}
	i.biocAnnLoaded = true
	i.biocAnnIndex = records
	return nil
}

func (i *nativeInstaller) ensureBiocExperimentIndex() error {
	if i.biocExpLoaded {
		return nil
	}
	i.stage("fetching Bioconductor experiment index")
	records, err := fetchRepoIndexCached(i.httpClient, biocExperimentRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor, i.req.Runtime.RVersion, i.repoIndexCacheDir())
	if err != nil {
		return fmt.Errorf("load Bioconductor experiment index: %w", err)
	}
	i.biocExpLoaded = true
	i.biocExpIndex = records
	return nil
}

func fetchRepoIndex(client *http.Client, baseURL, source, rVersion string) (map[string][]repoRecord, error) {
	return fetchRepoIndexCached(client, baseURL, source, rVersion, "")
}

func fetchRepoIndexCached(client *http.Client, baseURL, source, rVersion, cacheDir string) (map[string][]repoRecord, error) {
	contribURL, archiveExt := repositoryContribURL(strings.TrimRight(baseURL, "/"), source, rVersion)

	data, err := fetchPackagesFileCached(client, contribURL+"/PACKAGES.gz", cacheDir)
	if err != nil {
		data, err = fetchPackagesFileCached(client, contribURL+"/PACKAGES", cacheDir)
		if err != nil {
			return nil, err
		}
	}

	records := parseDCF(data)
	index := make(map[string][]repoRecord, len(records))
	for _, fields := range records {
		name := fields["Package"]
		version := fields["Version"]
		if name == "" || version == "" {
			continue
		}
		appendRepoCandidate(index, repoRecord{
			Name:             name,
			Version:          version,
			Dependencies:     parseDependencies(fields["Depends"], fields["Imports"], fields["LinkingTo"]),
			TarballURL:       fmt.Sprintf("%s/%s_%s%s", contribURL, name, version, archiveExt),
			Source:           source,
			DepsLoaded:       true,
			NeedsCompilation: parseNeedsCompilation(fields["NeedsCompilation"]),
		})
	}
	return index, nil
}

func (i *nativeInstaller) repoIndexCacheDir() string {
	if strings.TrimSpace(i.downloadRoot) == "" {
		return ""
	}
	return filepath.Join(i.downloadRoot, "indexes")
}

func appendRepoCandidate(index map[string][]repoRecord, record repoRecord) {
	candidates := index[record.Name]
	for _, existing := range candidates {
		if existing.Version == record.Version && existing.TarballURL == record.TarballURL {
			return
		}
	}
	candidates = append(candidates, record)
	slices.SortFunc(candidates, func(a, b repoRecord) int {
		return -compareVersions(a.Version, b.Version)
	})
	index[record.Name] = candidates
}

func fetchPackagesFile(client *http.Client, rawURL string) ([]byte, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < httpRetryAttempts; attempt++ {
		data, err := fetchPackagesFileOnce(client, rawURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if attempt < httpRetryAttempts-1 && shouldRetryHTTPOperation(err) {
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		break
	}
	return nil, lastErr
}

func fetchPackagesFileCached(client *http.Client, rawURL, cacheDir string) ([]byte, error) {
	cachePath := repoIndexCachePath(cacheDir, rawURL)
	if data, ok := readFreshRepoIndexCache(cachePath, time.Now()); ok {
		return data, nil
	}

	data, err := fetchPackagesFile(client, rawURL)
	if err == nil {
		if writeErr := writeRepoIndexCache(cachePath, data); writeErr != nil {
			return data, nil
		}
		return data, nil
	}
	if stale, ok := readAnyRepoIndexCache(cachePath); ok {
		return stale, nil
	}
	return nil, err
}

func repoIndexCachePath(cacheDir, rawURL string) string {
	return repoMetadataCachePath(cacheDir, rawURL, "PACKAGES.dcf")
}

func archiveIndexCachePath(cacheDir, rawURL string) string {
	return repoMetadataCachePath(filepath.Join(cacheDir, "archives"), rawURL, "archive.html")
}

func repoMetadataCachePath(cacheDir, rawURL, name string) string {
	if strings.TrimSpace(cacheDir) == "" || strings.TrimSpace(rawURL) == "" {
		return ""
	}
	base := strings.TrimSpace(name)
	if base == "" {
		base = filepath.Base(rawURL)
	}
	if strings.TrimSpace(base) == "" || base == "." || base == string(filepath.Separator) {
		base = "metadata"
	}
	return filepath.Join(cacheDir, downloadCacheName(rawURL, base))
}

func readFreshRepoIndexCache(path string, now time.Time) ([]byte, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return nil, false
	}
	if now.Sub(info.ModTime()) > repoIndexCacheTTL {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

func readAnyRepoIndexCache(path string) ([]byte, bool) {
	if strings.TrimSpace(path) == "" {
		return nil, false
	}
	info, err := os.Stat(path)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return nil, false
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil, false
	}
	return data, true
}

func writeRepoIndexCache(path string, data []byte) error {
	if strings.TrimSpace(path) == "" || len(data) == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	part := path + ".part"
	if err := os.WriteFile(part, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(part, path); err != nil {
		_ = os.Remove(part)
		return err
	}
	return nil
}

func fetchURLBytesCached(client *http.Client, rawURL, cachePath string) ([]byte, error) {
	if data, ok := readFreshRepoIndexCache(cachePath, time.Now()); ok {
		return data, nil
	}
	data, err := fetchURLBytes(client, rawURL)
	if err == nil {
		if writeErr := writeRepoIndexCache(cachePath, data); writeErr != nil {
			return data, nil
		}
		return data, nil
	}
	if stale, ok := readAnyRepoIndexCache(cachePath); ok {
		return stale, nil
	}
	return nil, err
}

func fetchURLBytes(client *http.Client, rawURL string) ([]byte, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < httpRetryAttempts; attempt++ {
		data, err := fetchURLBytesOnce(client, rawURL)
		if err == nil {
			return data, nil
		}
		lastErr = err
		if attempt < httpRetryAttempts-1 && shouldRetryHTTPOperation(err) {
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		break
	}
	return nil, lastErr
}

func fetchURLBytesOnce(client *http.Client, rawURL string) ([]byte, error) {
	resp, err := getOnce(client, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, rawURL)
	}
	return io.ReadAll(resp.Body)
}

func fetchPackagesFileOnce(client *http.Client, rawURL string) ([]byte, error) {
	resp, err := getOnce(client, rawURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, rawURL)
	}

	var reader io.Reader = resp.Body
	if strings.HasSuffix(rawURL, ".gz") {
		gzReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, err
		}
		defer gzReader.Close()
		reader = gzReader
	}
	return io.ReadAll(reader)
}

func parseArchiveVersions(pkg, body string) []string {
	pattern := regexp.MustCompile(fmt.Sprintf(`href="%s_([^"/]+)\.tar\.gz"`, regexp.QuoteMeta(pkg)))
	matches := pattern.FindAllStringSubmatch(body, -1)
	versions := make([]string, 0, len(matches))
	seen := map[string]struct{}{}
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		version := match[1]
		if _, ok := seen[version]; ok {
			continue
		}
		seen[version] = struct{}{}
		versions = append(versions, version)
	}
	slices.SortFunc(versions, func(a, b string) int {
		return -compareVersions(a, b)
	})
	return versions
}

func getWithRetry(client *http.Client, rawURL string) (*http.Response, error) {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < httpRetryAttempts; attempt++ {
		resp, err := getOnce(client, rawURL)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt < httpRetryAttempts-1 && shouldRetryHTTPOperation(err) {
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		break
	}
	return nil, lastErr
}

func getOnce(client *http.Client, rawURL string) (*http.Response, error) {
	return client.Get(rawURL)
}

func shouldRetryHTTPOperation(err error) bool {
	if err == nil {
		return false
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") ||
		strings.Contains(msg, "deadline exceeded") ||
		strings.Contains(msg, "temporary") ||
		strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "unexpected eof")
}

func downloadWithRetry(client *http.Client, rawURL, target, label string, progress io.Writer) error {
	var lastErr error
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < httpRetryAttempts; attempt++ {
		err := downloadOnce(client, rawURL, target, label, progress)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < httpRetryAttempts-1 && shouldRetryHTTPOperation(err) {
			time.Sleep(backoff)
			backoff *= 2
			continue
		}
		break
	}
	return lastErr
}

func downloadOnce(client *http.Client, rawURL, target, label string, progress io.Writer) error {
	resp, err := getOnce(client, rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, rawURL)
	}

	part := target + ".part"
	_ = os.Remove(part)
	file, err := os.Create(part)
	if err != nil {
		return err
	}
	defer func() {
		file.Close()
		_ = os.Remove(part)
	}()

	if err := progresscmd.Copy(file, resp.Body, resp.ContentLength, label, progress); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := validateCachedDownload(part); err != nil {
		return err
	}
	if err := os.Rename(part, target); err != nil {
		return err
	}
	return nil
}

func validateCachedDownload(path string) error {
	lower := strings.ToLower(path)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return validateTarGz(path)
	case strings.HasSuffix(lower, ".zip"):
		return validateZip(path)
	default:
		info, err := os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size() <= 0 {
			return fmt.Errorf("empty cached file")
		}
		return nil
	}
}

func validateTarGz(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gzReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzReader.Close()
	tarReader := tar.NewReader(gzReader)
	for {
		_, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, tarReader); err != nil {
			return err
		}
	}
}

func validateZip(path string) error {
	reader, err := zip.OpenReader(path)
	if err != nil {
		return err
	}
	defer reader.Close()
	for _, file := range reader.File {
		rc, err := file.Open()
		if err != nil {
			return err
		}
		if _, err := io.Copy(io.Discard, rc); err != nil {
			rc.Close()
			return err
		}
		if err := rc.Close(); err != nil {
			return err
		}
	}
	return nil
}

func parseDCF(data []byte) []map[string]string {
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	records := []map[string]string{}
	current := map[string]string{}
	lastKey := ""

	flush := func() {
		if len(current) == 0 {
			return
		}
		records = append(records, current)
		current = map[string]string{}
		lastKey = ""
	}

	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if (strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")) && lastKey != "" {
			current[lastKey] += " " + strings.TrimSpace(line)
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		lastKey = strings.TrimSpace(key)
		current[lastKey] = strings.TrimSpace(value)
	}
	flush()
	return records
}

func parseDependencies(values ...string) []packageRequirement {
	seen := map[string]packageRequirement{}
	names := []string{}
	for _, value := range values {
		normalized := strings.ReplaceAll(value, "\n", " ")
		for _, part := range strings.Split(normalized, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			req := parseDependencyRequirement(part)
			if req.Name == "" || req.Name == "R" {
				continue
			}
			if existing, ok := seen[req.Name]; ok {
				existing.Constraints = append(existing.Constraints, req.Constraints...)
				seen[req.Name] = existing
				continue
			}
			seen[req.Name] = req
			names = append(names, req.Name)
		}
	}
	slices.Sort(names)
	deps := make([]packageRequirement, 0, len(names))
	for _, name := range names {
		deps = append(deps, seen[name])
	}
	return deps
}

func parseDependencyRequirement(raw string) packageRequirement {
	part := strings.TrimSpace(strings.TrimSuffix(raw, ","))
	if part == "" {
		return packageRequirement{}
	}
	req := packageRequirement{Name: part}
	if idx := strings.Index(part, "("); idx >= 0 {
		req.Name = strings.TrimSpace(part[:idx])
		endIdx := strings.LastIndex(part, ")")
		if endIdx > idx {
			req.Constraints = parseVersionConstraints(part[idx+1 : endIdx])
		}
	}
	return req
}

func parseVersionConstraints(raw string) []versionConstraint {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return nil
	}
	operator := strings.TrimSpace(fields[0])
	version := strings.TrimSpace(strings.Join(fields[1:], " "))
	switch operator {
	case ">=", ">", "<=", "<", "==", "=":
		return []versionConstraint{{Operator: operator, Version: version}}
	default:
		return nil
	}
}

func versionSatisfies(version string, constraint versionConstraint) bool {
	cmp := compareVersions(version, constraint.Version)
	switch constraint.Operator {
	case ">":
		return cmp > 0
	case ">=", "":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case "=", "==":
		return cmp == 0
	default:
		return true
	}
}

func compareVersions(left, right string) int {
	leftTokens := tokenizeVersion(left)
	rightTokens := tokenizeVersion(right)
	maxLen := len(leftTokens)
	if len(rightTokens) > maxLen {
		maxLen = len(rightTokens)
	}
	for idx := 0; idx < maxLen; idx++ {
		var leftToken, rightToken versionToken
		if idx < len(leftTokens) {
			leftToken = leftTokens[idx]
		}
		if idx < len(rightTokens) {
			rightToken = rightTokens[idx]
		}
		if cmp := compareVersionToken(leftToken, rightToken); cmp != 0 {
			return cmp
		}
	}
	return 0
}

type versionToken struct {
	raw      string
	isNumber bool
}

func tokenizeVersion(version string) []versionToken {
	if version == "" {
		return nil
	}
	tokens := []versionToken{}
	current := strings.Builder{}
	currentNumeric := false
	hasCurrent := false
	flush := func() {
		if !hasCurrent {
			return
		}
		tokens = append(tokens, versionToken{
			raw:      current.String(),
			isNumber: currentNumeric,
		})
		current.Reset()
		hasCurrent = false
	}

	for _, r := range version {
		switch {
		case r >= '0' && r <= '9':
			if hasCurrent && !currentNumeric {
				flush()
			}
			currentNumeric = true
			current.WriteRune(r)
			hasCurrent = true
		case (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z'):
			if hasCurrent && currentNumeric {
				flush()
			}
			currentNumeric = false
			current.WriteRune(r)
			hasCurrent = true
		default:
			flush()
		}
	}
	flush()
	return tokens
}

func compareVersionToken(left, right versionToken) int {
	switch {
	case left.raw == "" && right.raw == "":
		return 0
	case left.raw == "":
		if right.isNumber {
			if strings.TrimLeft(right.raw, "0") == "" {
				return 0
			}
		}
		return -1
	case right.raw == "":
		if left.isNumber {
			if strings.TrimLeft(left.raw, "0") == "" {
				return 0
			}
		}
		return 1
	case left.isNumber && right.isNumber:
		leftTrimmed := strings.TrimLeft(left.raw, "0")
		rightTrimmed := strings.TrimLeft(right.raw, "0")
		if leftTrimmed == "" {
			leftTrimmed = "0"
		}
		if rightTrimmed == "" {
			rightTrimmed = "0"
		}
		if len(leftTrimmed) != len(rightTrimmed) {
			if len(leftTrimmed) < len(rightTrimmed) {
				return -1
			}
			return 1
		}
		if leftTrimmed < rightTrimmed {
			return -1
		}
		if leftTrimmed > rightTrimmed {
			return 1
		}
		return 0
	case left.isNumber:
		return 1
	case right.isNumber:
		return -1
	default:
		leftLower := strings.ToLower(left.raw)
		rightLower := strings.ToLower(right.raw)
		if leftLower < rightLower {
			return -1
		}
		if leftLower > rightLower {
			return 1
		}
		return 0
	}
}

func readDescriptionFromPath(target string) (description, error) {
	info, err := os.Stat(target)
	if err != nil {
		return description{}, err
	}
	if info.IsDir() {
		data, err := os.ReadFile(filepath.Join(target, "DESCRIPTION"))
		if err != nil {
			return description{}, err
		}
		return parseDescription(data), nil
	}
	if desc, ok := readDescriptionSidecar(target, info.ModTime()); ok {
		return desc, nil
	}
	data, err := installerReadDescriptionFile(target)
	if err != nil {
		return description{}, err
	}
	desc := parseDescription(data)
	writeDescriptionSidecar(target, desc)
	return desc, nil
}

func readDescriptionFromTarball(target string) ([]byte, error) {
	file, err := os.Open(target)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return readDescriptionFromTarReader(file)
}

func readDescriptionFromTarballURL(client *http.Client, rawURL string) (description, error) {
	resp, err := getWithRetry(client, rawURL)
	if err != nil {
		return description{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return description{}, fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, rawURL)
	}
	data, err := readDescriptionFromTarReader(resp.Body)
	if err != nil {
		return description{}, err
	}
	return parseDescription(data), nil
}

func readDescriptionFromTarReader(reader io.Reader) ([]byte, error) {
	gzReader, err := gzip.NewReader(reader)
	if err != nil {
		return nil, err
	}
	defer gzReader.Close()

	tarReader := tar.NewReader(gzReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasSuffix(header.Name, "/DESCRIPTION") {
			continue
		}
		return io.ReadAll(tarReader)
	}
	return nil, fmt.Errorf("DESCRIPTION not found in tar stream")
}

func readDescriptionSidecar(target string, targetModTime time.Time) (description, bool) {
	sidecar := descriptionSidecarPath(target)
	if strings.TrimSpace(sidecar) == "" {
		return description{}, false
	}
	info, err := os.Stat(sidecar)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return description{}, false
	}
	if info.ModTime().Before(targetModTime) {
		return description{}, false
	}
	data, err := os.ReadFile(sidecar)
	if err != nil || len(data) == 0 {
		return description{}, false
	}
	var desc description
	if err := json.Unmarshal(data, &desc); err != nil {
		return description{}, false
	}
	return desc, true
}

func writeDescriptionSidecar(target string, desc description) {
	sidecar := descriptionSidecarPath(target)
	if strings.TrimSpace(sidecar) == "" {
		return
	}
	data, err := json.Marshal(desc)
	if err != nil {
		return
	}
	part := sidecar + ".part"
	if err := os.WriteFile(part, data, 0o644); err != nil {
		return
	}
	if err := os.Rename(part, sidecar); err != nil {
		_ = os.Remove(part)
	}
}

func parseDescription(data []byte) description {
	fields := map[string]string{}
	for _, record := range parseDCF(data) {
		for key, value := range record {
			fields[key] = value
		}
		break
	}
	return description{
		Package:          fields["Package"],
		Version:          fields["Version"],
		Dependencies:     parseDependencies(fields["Depends"], fields["Imports"], fields["LinkingTo"]),
		NeedsCompilation: parseNeedsCompilation(fields["NeedsCompilation"]),
	}
}

func resolveSiblingRBinary(interpreter string) (string, bool) {
	candidates := []string{
		filepath.Join(filepath.Dir(interpreter), "R"),
		filepath.Join(filepath.Dir(interpreter), "R.exe"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, true
		}
	}
	return "", false
}

func inspectRRuntime(interpreter, workDir string, stderr io.Writer) (string, string, error) {
	binaryName := "R"
	if installerGOOS == "windows" {
		binaryName = "R.exe"
	}
	cmd := exec.Command(interpreter, "-e", fmt.Sprintf(`cat(file.path(R.home("bin"), %q)); cat("\n"); cat(as.character(getRversion()))`, binaryName))
	cmd.Dir = workDir
	if stderr != nil {
		cmd.Stderr = stderr
	}
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("inspect R runtime: %w", err)
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(output)), "\r\n", "\n"), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("inspect R runtime: unexpected output %q", strings.TrimSpace(string(output)))
	}
	rBinary := strings.TrimSpace(lines[0])
	version := strings.TrimSpace(lines[len(lines)-1])
	if rBinary == "" {
		return "", "", fmt.Errorf("inspect R runtime: missing R binary path")
	}
	if version == "" {
		return "", "", fmt.Errorf("inspect R runtime: missing R version")
	}
	return rBinary, version, nil
}

func biocMainRepositoryURL(rVersion string) string {
	biocVersion := biocVersionForR(rVersion)
	base := "https://bioconductor.org/packages"
	if biocVersion == "" {
		return base + "/release/bioc"
	}
	return fmt.Sprintf("%s/%s/bioc", base, biocVersion)
}

func biocAnnotationRepositoryURL(rVersion string) string {
	biocVersion := biocVersionForR(rVersion)
	base := "https://bioconductor.org/packages"
	if biocVersion == "" {
		return base + "/release/data/annotation"
	}
	return fmt.Sprintf("%s/%s/data/annotation", base, biocVersion)
}

func biocExperimentRepositoryURL(rVersion string) string {
	biocVersion := biocVersionForR(rVersion)
	base := "https://bioconductor.org/packages"
	if biocVersion == "" {
		return base + "/release/data/experiment"
	}
	return fmt.Sprintf("%s/%s/data/experiment", base, biocVersion)
}

func biocVersionForR(rVersion string) string {
	switch {
	case strings.HasPrefix(rVersion, "4.5"):
		return "3.21"
	case strings.HasPrefix(rVersion, "4.4"):
		return "3.20"
	case strings.HasPrefix(rVersion, "4.3"):
		return "3.18"
	case strings.HasPrefix(rVersion, "4.2"):
		return "3.16"
	default:
		return ""
	}
}

func repositoryContribURL(baseURL, source, rVersion string) (string, string) {
	baseURL = strings.TrimRight(baseURL, "/")
	if installerGOOS == "windows" && (source == sourceCRAN || source == sourceBioconductor) {
		return fmt.Sprintf("%s/bin/windows/contrib/%s", baseURL, windowsBinaryMinorVersion(rVersion)), ".zip"
	}
	return baseURL + "/src/contrib", ".tar.gz"
}

func windowsBinaryMinorVersion(rVersion string) string {
	parts := strings.Split(strings.TrimSpace(rVersion), ".")
	if len(parts) >= 2 {
		return parts[0] + "." + parts[1]
	}
	if len(parts) == 1 && parts[0] != "" {
		return parts[0]
	}
	return "release"
}

func parseNeedsCompilation(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "yes", "true":
		return true
	default:
		return false
	}
}

func (i *nativeInstaller) ensurePackageBuildTools(pkg string) error {
	switch installerGOOS {
	case "windows":
		return ensureWindowsSourceBuildTools(pkg, i.req.Environment)
	case "linux":
		return ensureLinuxSourceBuildTools(pkg, i.req.Environment)
	default:
		return nil
	}
}

func ensureWindowsSourceBuildTools(pkg string, env []string) error {
	if installerGOOS != "windows" {
		return nil
	}
	if windowsSourceBuildToolsAvailable(env) {
		return nil
	}
	return fmt.Errorf("package %s requires Windows source build tools, but Rtools was not detected\nnext step: install Rtools from https://cran.r-project.org/bin/windows/Rtools/ and ensure make.exe and gcc.exe are on PATH before retrying", pkg)
}

func windowsSourceBuildToolsAvailable(env []string) bool {
	required := []string{"make", "gcc"}
	pathOK := true
	for _, tool := range required {
		if _, err := findInstallerTool(tool, env); err != nil {
			pathOK = false
			break
		}
	}
	if pathOK {
		return true
	}

	roots := []string{
		strings.TrimSpace(os.Getenv("RTOOLS44_HOME")),
		strings.TrimSpace(os.Getenv("RTOOLS43_HOME")),
		strings.TrimSpace(os.Getenv("RTOOLS42_HOME")),
		strings.TrimSpace(os.Getenv("RTOOLS40_HOME")),
		`C:\rtools44`,
		`C:\rtools43`,
		`C:\rtools42`,
		`C:\rtools40`,
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		makePath := filepath.Join(root, "usr", "bin", "make.exe")
		gccCandidates := []string{
			filepath.Join(root, "ucrt64", "bin", "gcc.exe"),
			filepath.Join(root, "mingw64", "bin", "gcc.exe"),
		}
		if info, err := os.Stat(makePath); err != nil || info.IsDir() {
			continue
		}
		for _, gccPath := range gccCandidates {
			if info, err := os.Stat(gccPath); err == nil && !info.IsDir() {
				return true
			}
		}
	}
	return false
}

func ensureLinuxSourceBuildTools(pkg string, env []string) error {
	if installerGOOS != "linux" {
		return nil
	}
	missing := missingLinuxSourceBuildTools(env)
	if len(missing) == 0 {
		if err := verifyLinuxSourceToolchain(env); err != nil {
			return fmt.Errorf(
				"package %s requires Linux source build tools, but the detected C/C++ toolchain could not compile a test program\nnext step: verify the active compiler toolchain can link executables (for example with `%s`)\nnext step: %s\nprobe: %s",
				pkg,
				strings.TrimSpace(toolchainProbeExample(env)),
				rootlessToolchainAdvice(env),
				err,
			)
		}
		if issue := linuxPackageSpecificBuildToolIssue(pkg, env); issue != "" {
			advice := linuxSourceBuildAdvice()
			if advice != "" {
				return fmt.Errorf(
					"package %s requires additional Linux build tools, but %s\nnext step: %s\nnext step: %s",
					pkg,
					issue,
					advice,
					rootlessToolchainAdvice(env),
				)
			}
			return fmt.Errorf(
				"package %s requires additional Linux build tools, but %s\nnext step: %s",
				pkg,
				issue,
				rootlessToolchainAdvice(env),
			)
		}
		return nil
	}
	if issue := linuxPackageSpecificBuildToolIssue(pkg, env); issue != "" {
		advice := linuxSourceBuildAdvice()
		if advice != "" {
			return fmt.Errorf(
				"package %s requires additional Linux build tools, but %s\nnext step: %s\nnext step: %s",
				pkg,
				issue,
				advice,
				rootlessToolchainAdvice(env),
			)
		}
		return fmt.Errorf(
			"package %s requires additional Linux build tools, but %s\nnext step: %s",
			pkg,
			issue,
			rootlessToolchainAdvice(env),
		)
	}
	advice := linuxSourceBuildAdvice()
	if advice != "" {
		return fmt.Errorf(
			"package %s requires Linux source build tools, but required compilers are missing: %s\nnext step: %s\nnext step: %s",
			pkg,
			strings.Join(missing, ", "),
			advice,
			rootlessToolchainAdvice(env),
		)
	}
	return fmt.Errorf(
		"package %s requires Linux source build tools, but required compilers are missing: %s\nnext step: %s",
		pkg,
		strings.Join(missing, ", "),
		rootlessToolchainAdvice(env),
	)
}

func linuxPackageSpecificBuildToolIssue(pkg string, env []string) string {
	switch strings.ToLower(strings.TrimSpace(pkg)) {
	case "fs":
		return requireCMakeVersion(env, "3.10")
	default:
		return ""
	}
}

func requireCMakeVersion(env []string, minimum string) string {
	cmakePath, err := findInstallerTool("cmake", env)
	if err != nil {
		return fmt.Sprintf("cmake >= %s was not found on PATH", minimum)
	}
	version, err := installedToolVersion(cmakePath, env, `cmake version ([0-9]+(?:\.[0-9]+){0,2})`)
	if err != nil {
		return fmt.Sprintf("cmake >= %s is required, but the active cmake could not be inspected: %v", minimum, err)
	}
	if compareVersions(version, minimum) < 0 {
		return fmt.Sprintf("cmake >= %s is required, but the active cmake is %s", minimum, version)
	}
	return ""
}

func installedToolVersion(toolPath string, env []string, pattern string) (string, error) {
	cmd := exec.Command(toolPath, "--version")
	if len(env) > 0 {
		cmd.Env = env
	}
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("run %s --version: %w", filepath.Base(toolPath), err)
	}
	re := regexp.MustCompile(pattern)
	matches := re.FindStringSubmatch(string(output))
	if len(matches) < 2 {
		return "", fmt.Errorf("parse version from output %q", strings.TrimSpace(string(output)))
	}
	return matches[1], nil
}

func missingLinuxSourceBuildTools(env []string) []string {
	choice := preferredLinuxCompilerChoice(env)
	missing := []string{}
	if choice.C == "" {
		missing = append(missing, "gcc")
	}
	if choice.CXX == "" {
		missing = append(missing, "g++")
	}
	if choice.FC == "" {
		missing = append(missing, "gfortran")
	}
	if _, err := findInstallerTool("make", env); err != nil {
		missing = append(missing, "make")
	}
	return missing
}

func findInstallerTool(name string, env []string) (string, error) {
	if len(env) == 0 {
		return installerLookPath(name)
	}
	return toolchainenv.FindInPath(name, env)
}

func verifyLinuxSourceToolchain(env []string) error {
	tmpDir, err := os.MkdirTemp("", "gr-toolchain-smoke-*")
	if err != nil {
		return fmt.Errorf("prepare toolchain smoke test: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	sourcePath := filepath.Join(tmpDir, "main.cpp")
	outputPath := filepath.Join(tmpDir, "main")
	if err := os.WriteFile(sourcePath, []byte("int main() { return 0; }\n"), 0o644); err != nil {
		return fmt.Errorf("write toolchain smoke source: %w", err)
	}

	choice := preferredLinuxCompilerChoice(env)
	compiler := choice.CXX
	if compiler == "" {
		compiler = "g++"
	}
	name, args, wrappedEnv, wrapped, err := toolchainenv.WrapCommand(compiler, []string{sourcePath, "-o", outputPath}, env)
	if err != nil {
		return fmt.Errorf("prepare toolchain smoke command: %w", err)
	}
	if !wrapped {
		resolved, err := findInstallerTool(compiler, env)
		if err != nil {
			return fmt.Errorf("resolve %s for toolchain smoke test: %w", compiler, err)
		}
		name = resolved
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = tmpDir
	cmd.Env = wrappedEnv
	output, runErr := cmd.CombinedOutput()
	if runErr == nil {
		return nil
	}
	summary := strings.TrimSpace(string(output))
	if summary == "" {
		summary = runErr.Error()
	} else {
		lines := strings.Split(summary, "\n")
		if len(lines) > 8 {
			lines = lines[:8]
		}
		summary = strings.Join(lines, " | ")
	}
	return fmt.Errorf("%s: %s", shellQuoteCommand(name, args), summary)
}

func toolchainProbeExample(env []string) string {
	choice := preferredLinuxCompilerChoice(env)
	compiler := choice.CXX
	if compiler == "" {
		compiler = "g++"
	}
	candidate, err := toolchainenv.CandidateFromEnvironment(env)
	if err != nil || candidate == nil {
		return fmt.Sprintf(`%s smoke.cpp -o smoke`, compiler)
	}
	switch candidate.Preset {
	case "enva":
		if len(candidate.ToolchainPrefixes) > 0 && !toolchainenvIsManagedEnvaPrefix(candidate.ToolchainPrefixes[0]) {
			return fmt.Sprintf(`"%s" smoke.cpp -o smoke`, filepath.Join(candidate.ToolchainPrefixes[0], "bin", compiler))
		}
		return fmt.Sprintf(`enva run rs-sysdeps -- %s smoke.cpp -o smoke`, compiler)
	case "micromamba", "mamba", "conda":
		if len(candidate.ToolchainPrefixes) > 0 {
			return fmt.Sprintf(`%s run -p "%s" -- %s smoke.cpp -o smoke`, candidate.Preset, candidate.ToolchainPrefixes[0], compiler)
		}
	}
	return fmt.Sprintf(`%s smoke.cpp -o smoke`, compiler)
}

func shellQuoteCommand(name string, args []string) string {
	parts := append([]string{name}, args...)
	quoted := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.ContainsAny(part, " \t\"'") {
			quoted = append(quoted, strconv.Quote(part))
			continue
		}
		quoted = append(quoted, part)
	}
	return strings.Join(quoted, " ")
}

func linuxSourceBuildAdvice() string {
	distro, err := detectInstallerLinuxDistro()
	if err != nil {
		return "install gcc, g++, gfortran, and make before retrying"
	}
	switch {
	case distro == "arch":
		return "pacman -S --needed base-devel gcc-fortran cmake"
	case distro == "debian", distro == "ubuntu":
		return "apt-get update && apt-get install -y build-essential gfortran cmake"
	case distro == "rhel", distro == "centos", distro == "rocky", distro == "almalinux", distro == "fedora":
		return "dnf install -y gcc gcc-c++ gcc-gfortran make cmake"
	default:
		return "install gcc, g++, gfortran, make, and cmake before retrying"
	}
}

func rootlessToolchainAdvice(env []string) string {
	base := "if you cannot install system packages, provide a user-local toolchain prefix with enva, Homebrew-in-home, micromamba, mamba, conda, or Spack, then expose it via RS_TOOLCHAIN_PREFIXES/RS_PKG_CONFIG_PATH or rs.toml; start with `rs toolchain detect`, `rs toolchain template auto`, and `rs doctor --toolchain-only`"
	candidate, err := toolchainenv.CandidateFromEnvironment(env)
	if err == nil && candidate != nil {
		return fmt.Sprintf("%s; detected recommended preset on this machine: %s; setup follow-up: `%s`; project follow-up: `%s`", base, candidate.Preset, candidate.SuggestedSetupCommand, candidate.SuggestedInitCommand)
	}
	candidate, err = toolchainenv.RecommendedCandidate("")
	if err != nil || candidate == nil {
		return base
	}
	return fmt.Sprintf("%s; detected recommended preset on this machine: %s; setup follow-up: `%s`; project follow-up: `%s`", base, candidate.Preset, candidate.SuggestedSetupCommand, candidate.SuggestedInitCommand)
}

func toolchainenvIsManagedEnvaPrefix(prefix string) bool {
	cleaned := strings.ToLower(filepath.ToSlash(strings.TrimSpace(prefix)))
	return strings.Contains(cleaned, "/rattler/envs/")
}

func detectInstallerLinuxDistro() (string, error) {
	data, err := installerReadFile("/etc/os-release")
	if err != nil {
		return "", err
	}
	values := map[string]string{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	if id := strings.ToLower(strings.TrimSpace(values["ID"])); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("linux distribution id not found")
}

func withLibraryEnv(env []string, libraryPath string) []string {
	filtered := make([]string, 0, len(env)+2)
	for _, entry := range env {
		if strings.HasPrefix(entry, "R_LIBS=") || strings.HasPrefix(entry, "R_LIBS_USER=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, "R_LIBS="+libraryPath, "R_LIBS_USER="+libraryPath)
	return filtered
}

func withInstallEnv(env []string, cacheRoot string) []string {
	if len(env) == 0 {
		env = os.Environ()
	}
	filtered := make([]string, 0, len(env)+10)
	hasMakeflags := false
	hasCMake := false
	hasCC := false
	hasCXX := false
	hasFC := false
	hasCCacheDir := false
	hasSCCacheDir := false
	for _, entry := range env {
		switch {
		case strings.HasPrefix(entry, "MAKEFLAGS="):
			hasMakeflags = true
		case strings.HasPrefix(entry, "CMAKE_BUILD_PARALLEL_LEVEL="):
			hasCMake = true
		case strings.HasPrefix(entry, "CC="):
			hasCC = true
		case strings.HasPrefix(entry, "CXX="):
			hasCXX = true
		case strings.HasPrefix(entry, "FC="):
			hasFC = true
		case strings.HasPrefix(entry, "CCACHE_DIR="):
			hasCCacheDir = true
		case strings.HasPrefix(entry, "SCCACHE_DIR="):
			hasSCCacheDir = true
		}
		filtered = append(filtered, entry)
	}
	jobs := strconv.Itoa(defaultInstallJobs())
	if !hasMakeflags {
		filtered = append(filtered, "MAKEFLAGS=-j"+jobs)
	}
	if !hasCMake {
		filtered = append(filtered, "CMAKE_BUILD_PARALLEL_LEVEL="+jobs)
	}
	choice := preferredLinuxCompilerChoice(filtered)
	launcher, ok := compilerLauncher(filtered)
	if ok && (!hasCC || !hasCXX || (!hasFC && choice.FC != "")) {
		if !hasCC {
			cc := "gcc"
			if choice.C != "" {
				cc = choice.C
			}
			filtered = append(filtered, "CC="+launcher+" "+cc)
		}
		if !hasCXX {
			cxx := "g++"
			if choice.CXX != "" {
				cxx = choice.CXX
			}
			filtered = append(filtered, "CXX="+launcher+" "+cxx)
		}
		if !hasFC && choice.FC != "" {
			filtered = append(filtered, "FC="+launcher+" "+choice.FC)
		}
		switch launcher {
		case "ccache":
			if cacheRoot != "" && !hasCCacheDir {
				filtered = append(filtered, "CCACHE_DIR="+filepath.Join(cacheRoot, "ccache"))
			}
		case "sccache":
			if cacheRoot != "" && !hasSCCacheDir {
				filtered = append(filtered, "SCCACHE_DIR="+filepath.Join(cacheRoot, "sccache"))
			}
		}
	} else {
		if !hasCC && choice.C != "" {
			filtered = append(filtered, "CC="+choice.C)
		}
		if !hasCXX && choice.CXX != "" {
			filtered = append(filtered, "CXX="+choice.CXX)
		}
		if !hasFC && choice.FC != "" {
			filtered = append(filtered, "FC="+choice.FC)
		}
	}
	return filtered
}

func defaultInstallJobs() int {
	jobs := runtime.NumCPU()
	if jobs < 1 {
		return 1
	}
	if jobs > 8 {
		return 8
	}
	return jobs
}

func compilerLauncher(env []string) (string, bool) {
	choice := preferredLinuxCompilerChoice(env)
	cc := "gcc"
	if choice.C != "" {
		cc = choice.C
	}
	cxx := "g++"
	if choice.CXX != "" {
		cxx = choice.CXX
	}
	for _, launcher := range []string{"ccache", "sccache"} {
		if _, err := findInstallerTool(launcher, env); err != nil {
			continue
		}
		if _, err := findInstallerTool(cc, env); err != nil {
			continue
		}
		if _, err := findInstallerTool(cxx, env); err != nil {
			continue
		}
		return launcher, true
	}
	return "", false
}

type linuxCompilerChoice struct {
	C   string
	CXX string
	FC  string
}

func preferredLinuxCompilerChoice(env []string) linuxCompilerChoice {
	choice := linuxCompilerChoice{}
	if installerGOOS != "linux" {
		return choice
	}
	if candidate, err := toolchainenv.CandidateFromEnvironment(env); err == nil && candidate != nil && isCondaLikePreset(candidate.Preset) {
		choice.C = firstAvailableCompiler(env, condaCompilerCandidates("gcc")...)
		choice.CXX = firstAvailableCompiler(env, condaCompilerCandidates("c++")...)
		if choice.CXX == "" {
			choice.CXX = firstAvailableCompiler(env, condaCompilerCandidates("g++")...)
		}
		choice.FC = firstAvailableCompiler(env, condaCompilerCandidates("gfortran")...)
	}
	if choice.C == "" {
		choice.C = firstAvailableCompiler(env, "gcc")
	}
	if choice.CXX == "" {
		choice.CXX = firstAvailableCompiler(env, "g++", "c++")
	}
	if choice.FC == "" {
		choice.FC = firstAvailableCompiler(env, "gfortran")
	}
	return choice
}

func isCondaLikePreset(preset string) bool {
	switch preset {
	case "enva", "micromamba", "mamba", "conda":
		return true
	default:
		return false
	}
}

func condaCompilerCandidates(tool string) []string {
	triplets := []string{}
	seen := map[string]struct{}{}
	addTriplet := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		triplets = append(triplets, value)
	}

	switch runtime.GOARCH {
	case "amd64":
		addTriplet("x86_64-conda-linux-gnu")
	case "arm64":
		addTriplet("aarch64-conda-linux-gnu")
	case "ppc64le":
		addTriplet("powerpc64le-conda-linux-gnu")
	case "s390x":
		addTriplet("s390x-conda-linux-gnu")
	case "riscv64":
		addTriplet("riscv64-conda-linux-gnu")
	}
	for _, fallback := range []string{
		"x86_64-conda-linux-gnu",
		"aarch64-conda-linux-gnu",
		"powerpc64le-conda-linux-gnu",
		"s390x-conda-linux-gnu",
		"riscv64-conda-linux-gnu",
	} {
		addTriplet(fallback)
	}

	candidates := make([]string, 0, len(triplets)+1)
	for _, triplet := range triplets {
		candidates = append(candidates, triplet+"-"+tool)
	}
	return candidates
}

func firstAvailableCompiler(env []string, names ...string) string {
	for _, name := range names {
		if _, err := findInstallerTool(name, env); err == nil {
			return name
		}
	}
	return ""
}

func runCommand(workDir string, progress, errors io.Writer, label string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	return progresscmd.Run(cmd, label, progress, errors)
}

func gitOutput(repoDir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", repoDir}, args...)...)
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(output), nil
}

func githubCloneURL(spec project.SourceSpec) (string, string, error) {
	host := strings.TrimSpace(spec.Host)
	if host == "" {
		return "https://github.com/" + spec.Repo + ".git", "api.github.com", nil
	}

	raw := host
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	parsed, err := neturl.Parse(raw)
	if err != nil {
		return "", "", fmt.Errorf("parse github host for %s: %w", spec.Package, err)
	}

	clonePrefix := strings.TrimSuffix(strings.TrimSuffix(parsed.Path, "/"), "/api/v3")
	clonePath := path.Join(clonePrefix, spec.Repo+".git")
	if !strings.HasPrefix(clonePath, "/") {
		clonePath = "/" + clonePath
	}
	parsed.Path = clonePath
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), host, nil
}

func injectGitToken(rawURL, token string) string {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return rawURL
	}
	parsed.User = neturl.UserPassword("x-access-token", token)
	return parsed.String()
}

func writeSourceMetadata(metaDir, pkg string, prepared preparedSource) error {
	if metaDir == "" {
		return nil
	}
	encode := func(value string) string {
		return neturl.QueryEscape(value)
	}
	line := strings.Join([]string{
		encode(prepared.Source),
		encode(prepared.Host),
		encode(prepared.Location),
		encode(prepared.Ref),
		encode(prepared.Commit),
		encode(prepared.Subdir),
		encode(prepared.Fingerprint),
		encode(prepared.FingerprintKind),
	}, "\t")
	return os.WriteFile(filepath.Join(metaDir, pkg+".tsv"), []byte(line+"\n"), 0o644)
}

func removeSourceMetadata(metaDir, pkg string) error {
	if metaDir == "" {
		return nil
	}
	err := os.Remove(filepath.Join(metaDir, pkg+".tsv"))
	if err == nil || errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return fmt.Errorf("remove source metadata for %s: %w", pkg, err)
}

func readInstalledSourceMetadata(metaDir string) (map[string]installedPackage, error) {
	entries, err := os.ReadDir(metaDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read source metadata dir: %w", err)
	}

	metaByName := make(map[string]installedPackage, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".tsv") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".tsv")
		data, err := os.ReadFile(filepath.Join(metaDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read source metadata file: %w", err)
		}
		decoded := decodeInstalledSourceMetadata(name, data)
		if decoded.Name == "" {
			continue
		}
		metaByName[name] = decoded
	}
	return metaByName, nil
}

func readInstalledSourceMetadataForPackage(metaDir, pkg string) (installedPackage, error) {
	if strings.TrimSpace(metaDir) == "" || strings.TrimSpace(pkg) == "" {
		return installedPackage{}, nil
	}
	data, err := os.ReadFile(filepath.Join(metaDir, pkg+".tsv"))
	if errors.Is(err, os.ErrNotExist) {
		return installedPackage{}, nil
	}
	if err != nil {
		return installedPackage{}, fmt.Errorf("read source metadata file: %w", err)
	}
	return decodeInstalledSourceMetadata(pkg, data), nil
}

func decodeInstalledSourceMetadata(name string, data []byte) installedPackage {
	line := strings.TrimSpace(string(data))
	if line == "" {
		return installedPackage{}
	}
	fields := strings.Split(line, "\t")
	for len(fields) < 8 {
		fields = append(fields, "")
	}
	decode := func(value string) string {
		decoded, err := neturl.QueryUnescape(value)
		if err != nil {
			return value
		}
		return decoded
	}
	return installedPackage{
		Name:            name,
		Source:          decode(fields[0]),
		Host:            decode(fields[1]),
		Location:        decode(fields[2]),
		Ref:             decode(fields[3]),
		Commit:          decode(fields[4]),
		Subdir:          decode(fields[5]),
		Fingerprint:     decode(fields[6]),
		FingerprintKind: decode(fields[7]),
	}
}

func mergeDeps(groups ...[]string) []string {
	seen := map[string]struct{}{}
	merged := []string{}
	for _, group := range groups {
		for _, name := range group {
			if name == "" {
				continue
			}
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = struct{}{}
			merged = append(merged, name)
		}
	}
	slices.Sort(merged)
	return merged
}

func sourceDepNames(sourceDeps map[string]project.SourceSpec) []string {
	names := make([]string, 0, len(sourceDeps))
	for name := range sourceDeps {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func describeLocalFingerprint(path string) (string, string) {
	kind, fingerprint, err := readLocalFingerprint(path)
	if err == nil {
		return kind, fingerprint
	}
	if errors.Is(err, os.ErrNotExist) {
		return localKindMissing, ""
	}
	return localKindError, ""
}

func readLocalFingerprint(target string) (string, string, error) {
	info, err := os.Stat(target)
	if err != nil {
		return "", "", err
	}
	if info.IsDir() {
		fingerprint, err := fingerprintDirectory(target)
		if err != nil {
			return "", "", err
		}
		return localKindDirSHA256, fingerprint, nil
	}
	fingerprint, err := fingerprintFile(target)
	if err != nil {
		return "", "", err
	}
	return localKindFileSHA256, fingerprint, nil
}

func fingerprintFile(target string) (string, error) {
	file, err := os.Open(target)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func fingerprintDirectory(root string) (string, error) {
	sum := sha256.New()
	err := filepath.WalkDir(root, func(target string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, target)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "" {
			rel = "."
		}

		entryType := "other"
		value := ""
		switch {
		case entry.IsDir():
			entryType = "dir"
		case entry.Type()&os.ModeSymlink != 0:
			entryType = "symlink"
			targetPath, err := os.Readlink(target)
			if err != nil {
				return err
			}
			value = targetPath
		default:
			info, err := entry.Info()
			if err != nil {
				return err
			}
			if info.Mode().IsRegular() {
				entryType = "file"
				value, err = fingerprintFile(target)
				if err != nil {
					return err
				}
			} else {
				entryType = info.Mode().Type().String()
			}
		}

		if _, err := io.WriteString(sum, rel+"\t"+entryType+"\t"+value+"\n"); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}
