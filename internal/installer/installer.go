package installer

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
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

	"gr/internal/progresscmd"
	"gr/internal/project"
	"gr/internal/rdeps"
	"gr/internal/toolchainenv"
)

const (
	sourceCRAN          = "cran"
	sourceBioconductor  = "bioconductor"
	sourceLocal         = "local"
	sourceGit           = "git"
	sourceGitHub        = "github"
	localKindFileSHA256 = "file_sha256"
	localKindDirSHA256  = "dir_tree_sha256"
	localKindMissing    = "missing"
	localKindError      = "unavailable"
)

var (
	installerGOOS     = runtime.GOOS
	installerLookPath = exec.LookPath
	installerReadFile = os.ReadFile
	installerRunCmd   = func(cmd *exec.Cmd) error { return cmd.Run() }
)

type Runtime struct {
	RVersion string
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

	if inst.canParallelInstallPurePackages() {
		for _, layer := range installPlanLayers(inst.planned, inst.order) {
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
				inst.installedPackages[name] = installedPackageForPlanned(inst.planned[name])
			}
			for _, name := range compiled {
				installed, err := inst.installPlannedPackage(name)
				if err != nil {
					return err
				}
				if installed {
					inst.installedPackages[name] = installedPackageForPlanned(inst.planned[name])
				}
			}
		}
		return nil
	}

	for _, name := range inst.order {
		installed, err := inst.installPlannedPackage(name)
		if err != nil {
			return err
		}
		if installed {
			inst.installedPackages[name] = installedPackageForPlanned(inst.planned[name])
		}
	}

	return nil
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
		httpClient:        &http.Client{Timeout: 30 * time.Second},
		requirements:      map[string][]constraintRequest{},
		cranArchiveLoaded: map[string]bool{},
		selectedVersions:  map[string]string{},
	}
	if err := os.MkdirAll(inst.downloadRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create download cache dir: %w", err)
	}

	if req.Runtime.RVersion == "" {
		version, err := inspectRVersion(req.Interpreter, req.WorkDir, stderr)
		if err != nil {
			return nil, err
		}
		inst.req.Runtime.RVersion = version
	}

	inst.rBinary, err = resolveRBinary(req.Interpreter, req.WorkDir, stderr)
	if err != nil {
		return nil, err
	}
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
	desc, err := readDescriptionFromTarballURL(i.httpClient, record.TarballURL)
	if err != nil {
		return err
	}
	record.Dependencies = desc.Dependencies
	record.DepsLoaded = true
	i.replaceRepoCandidate(record.Name, *record)
	return nil
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
	if strings.HasSuffix(strings.ToLower(record.TarballURL), ".tar.gz") {
		needsCompilation := record.NeedsCompilation
		if !record.DepsLoaded {
			desc, err := readDescriptionFromTarballURL(i.httpClient, record.TarballURL)
			if err != nil {
				return fmt.Errorf("inspect %s source package: %w", record.Name, err)
			}
			needsCompilation = desc.NeedsCompilation
		}
		if needsCompilation {
			if err := i.ensurePackageBuildTools(record.Name); err != nil {
				return err
			}
		}
	}
	target, err := i.download(record.TarballURL, repoDownloadName(record))
	if err != nil {
		return fmt.Errorf("download %s: %w", record.Name, err)
	}
	if err := i.runRCommandInstall(target); err != nil {
		return fmt.Errorf("install %s from %s: %w", record.Name, record.Source, err)
	}
	if err := removeSourceMetadata(i.metaDir, record.Name); err != nil {
		return err
	}
	return nil
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
	if err := i.runRCommandInstall(prepared.InstallPath); err != nil {
		return fmt.Errorf("install %s from %s source: %w", prepared.Name, prepared.Source, err)
	}
	return writeSourceMetadata(i.metaDir, prepared.Name, prepared)
}

func (i *nativeInstaller) runRCommandInstall(target string) error {
	cmd, err := buildInstallCommand(i.rBinary, i.req.WorkDir, i.req.CacheRoot, i.req.LibraryPath, i.req.Environment, target)
	if err != nil {
		return err
	}
	label := fmt.Sprintf("installing R package %s", filepath.Base(target))
	if err := progresscmd.Run(cmd, label, i.stderr, i.stderr); err != nil {
		return err
	}
	return nil
}

func buildInstallCommand(rBinary, workDir, cacheRoot, libraryPath string, env []string, target string) (*exec.Cmd, error) {
	installEnv := withInstallEnv(withLibraryEnv(env, libraryPath), cacheRoot)
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

func (i *nativeInstaller) download(rawURL, name string) (string, error) {
	target := filepath.Join(i.downloadRoot, downloadCacheName(rawURL, name))
	if info, err := os.Stat(target); err == nil && !info.IsDir() && info.Size() > 0 {
		progresscmd.Stage(i.stderr, "reusing cached "+name)
		return target, nil
	}

	i.stage("downloading " + name)
	resp, err := getWithRetry(i.httpClient, rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, rawURL)
	}

	file, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if err := progresscmd.Copy(file, resp.Body, resp.ContentLength, "downloading "+name, i.stderr); err != nil {
		return "", err
	}
	return target, nil
}

func downloadCacheName(rawURL, name string) string {
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
	entries, err := os.ReadDir(i.req.LibraryPath)
	if errors.Is(err, os.ErrNotExist) {
		i.installedPackages = map[string]installedPackage{}
		return nil
	}
	if err != nil {
		return fmt.Errorf("read managed library: %w", err)
	}

	metaByName, err := readInstalledSourceMetadata(i.metaDir)
	if err != nil {
		return err
	}

	installed := map[string]installedPackage{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		descPath := filepath.Join(i.req.LibraryPath, entry.Name(), "DESCRIPTION")
		data, err := os.ReadFile(descPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return fmt.Errorf("read installed DESCRIPTION for %s: %w", entry.Name(), err)
		}
		fields := map[string]string{}
		for _, record := range parseDCF(data) {
			for key, value := range record {
				fields[key] = value
			}
			break
		}
		pkg := installedPackage{
			Name:    entry.Name(),
			Version: fields["Version"],
		}
		switch repository := fields["Repository"]; {
		case strings.EqualFold(repository, "CRAN"):
			pkg.Source = sourceCRAN
		case strings.Contains(strings.ToLower(repository), "bioconductor"):
			pkg.Source = sourceBioconductor
		}
		if meta, ok := metaByName[entry.Name()]; ok {
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
		}
		installed[entry.Name()] = pkg
	}
	i.installedPackages = installed
	return nil
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
	index, err := fetchRepoIndex(i.httpClient, strings.TrimRight(i.req.Repo, "/"), sourceCRAN, i.req.Runtime.RVersion)
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
	resp, err := getWithRetry(i.httpClient, archiveURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, archiveURL)
	}
	body, err := io.ReadAll(resp.Body)
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
	records, err := fetchRepoIndex(i.httpClient, biocMainRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor, i.req.Runtime.RVersion)
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
	records, err := fetchRepoIndex(i.httpClient, biocAnnotationRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor, i.req.Runtime.RVersion)
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
	records, err := fetchRepoIndex(i.httpClient, biocExperimentRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor, i.req.Runtime.RVersion)
	if err != nil {
		return fmt.Errorf("load Bioconductor experiment index: %w", err)
	}
	i.biocExpLoaded = true
	i.biocExpIndex = records
	return nil
}

func fetchRepoIndex(client *http.Client, baseURL, source, rVersion string) (map[string][]repoRecord, error) {
	contribURL, archiveExt := repositoryContribURL(strings.TrimRight(baseURL, "/"), source, rVersion)

	data, err := fetchPackagesFile(client, contribURL+"/PACKAGES.gz")
	if err != nil {
		data, err = fetchPackagesFile(client, contribURL+"/PACKAGES")
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
	resp, err := getWithRetry(client, rawURL)
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
	for attempt := 0; attempt < 3; attempt++ {
		resp, err := client.Get(rawURL)
		if err == nil {
			return resp, nil
		}
		lastErr = err
		if attempt < 2 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}
	return nil, lastErr
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
	data, err := readDescriptionFromTarball(target)
	if err != nil {
		return description{}, err
	}
	return parseDescription(data), nil
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

func resolveRBinary(interpreter, workDir string, stderr io.Writer) (string, error) {
	candidates := []string{
		filepath.Join(filepath.Dir(interpreter), "R"),
		filepath.Join(filepath.Dir(interpreter), "R.exe"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}

	binaryName := "R"
	if installerGOOS == "windows" {
		binaryName = "R.exe"
	}
	cmd := exec.Command(interpreter, "-e", fmt.Sprintf(`cat(file.path(R.home("bin"), %q))`, binaryName))
	cmd.Dir = workDir
	if stderr != nil {
		cmd.Stderr = stderr
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("resolve R binary: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
}

func inspectRVersion(interpreter, workDir string, stderr io.Writer) (string, error) {
	cmd := exec.Command(interpreter, "-e", `cat(as.character(getRversion()))`)
	cmd.Dir = workDir
	if stderr != nil {
		cmd.Stderr = stderr
	}
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("inspect R version: %w", err)
	}
	return strings.TrimSpace(string(output)), nil
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
				rootlessToolchainAdvice(),
				err,
			)
		}
		return nil
	}
	advice := linuxSourceBuildAdvice()
	if advice != "" {
		return fmt.Errorf(
			"package %s requires Linux source build tools, but required compilers are missing: %s\nnext step: %s\nnext step: %s",
			pkg,
			strings.Join(missing, ", "),
			advice,
			rootlessToolchainAdvice(),
		)
	}
	return fmt.Errorf(
		"package %s requires Linux source build tools, but required compilers are missing: %s\nnext step: %s",
		pkg,
		strings.Join(missing, ", "),
		rootlessToolchainAdvice(),
	)
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
		return "pacman -S --needed base-devel gcc-fortran"
	case distro == "debian", distro == "ubuntu":
		return "apt-get update && apt-get install -y build-essential gfortran"
	case distro == "rhel", distro == "centos", distro == "rocky", distro == "almalinux", distro == "fedora":
		return "dnf install -y gcc gcc-c++ gcc-gfortran make"
	default:
		return "install gcc, g++, gfortran, and make before retrying"
	}
}

func rootlessToolchainAdvice() string {
	base := "if you cannot install system packages, provide a user-local toolchain prefix with enva, Homebrew-in-home, micromamba, mamba, conda, or Spack, then expose it via RS_TOOLCHAIN_PREFIXES/RS_PKG_CONFIG_PATH or rs.toml; start with `rs toolchain detect`, `rs toolchain template auto`, and `rs doctor --toolchain-only`"
	candidate, err := toolchainenv.RecommendedCandidate("")
	if err != nil || candidate == nil {
		return base
	}
	return fmt.Sprintf("%s; detected recommended preset on this machine: %s; setup follow-up: `%s`; project follow-up: `%s`", base, candidate.Preset, candidate.SuggestedSetupCommand, candidate.SuggestedInitCommand)
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
		line := strings.TrimSpace(string(data))
		if line == "" {
			continue
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
		metaByName[name] = installedPackage{
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
	return metaByName, nil
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
