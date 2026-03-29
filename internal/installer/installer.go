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
	"slices"
	"strings"
	"time"

	"gr/internal/project"
	"gr/internal/rdeps"
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

type Runtime struct {
	RVersion string
}

type Request struct {
	Interpreter string
	WorkDir     string
	LibraryPath string
	Repo        string
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
	Name         string
	Version      string
	Dependencies []packageRequirement
	TarballURL   string
	Source       string
	DepsLoaded   bool
}

type preparedSource struct {
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
	Dependencies    []packageRequirement
	InstallPath     string
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
	Package      string
	Version      string
	Dependencies []packageRequirement
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

	for _, name := range inst.order {
		pkg := inst.planned[name]
		if inst.isPlannedPackageInstalled(pkg) {
			continue
		}
		switch pkg.Source {
		case sourceCRAN, sourceBioconductor:
			if err := inst.installRepoPackage(*pkg.Repo); err != nil {
				return err
			}
		case sourceLocal, sourceGit, sourceGitHub:
			if err := inst.installPreparedSource(*pkg.Prepared); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported native source %q for package %s", pkg.Source, name)
		}
		inst.installedPackages[name] = installedPackageForPlanned(pkg)
	}

	return nil
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
	i.planned[pkg.Name] = pkg
	childChain := append(append([]string(nil), chain...), pkg.Name)
	if err := i.planDependencyList(pkg.Deps, 0, pkg.Name, childChain, skips); err != nil {
		return err
	}
	i.resolved[pkg.Name] = true
	i.order = append(i.order, pkg.Name)
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
			Name:            desc.Package,
			Version:         desc.Version,
			Source:          sourceLocal,
			Location:        spec.Path,
			Fingerprint:     fingerprint,
			FingerprintKind: kind,
			Dependencies:    desc.Dependencies,
			InstallPath:     spec.Path,
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
			Name:         desc.Package,
			Version:      desc.Version,
			Source:       sourceGit,
			Location:     spec.URL,
			Ref:          spec.Ref,
			Commit:       strings.TrimSpace(commit),
			Subdir:       spec.Subdir,
			Dependencies: desc.Dependencies,
			InstallPath:  installPath,
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
			Name:         desc.Package,
			Version:      desc.Version,
			Source:       sourceGitHub,
			Host:         spec.Host,
			Location:     spec.Repo,
			Ref:          spec.Ref,
			Commit:       strings.TrimSpace(commit),
			Subdir:       spec.Subdir,
			Dependencies: desc.Dependencies,
			InstallPath:  installPath,
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
	target, err := i.download(record.TarballURL, fmt.Sprintf("%s_%s.tar.gz", record.Name, record.Version))
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
	if err := i.runRCommandInstall(prepared.InstallPath); err != nil {
		return fmt.Errorf("install %s from %s source: %w", prepared.Name, prepared.Source, err)
	}
	return writeSourceMetadata(i.metaDir, prepared.Name, prepared)
}

func (i *nativeInstaller) runRCommandInstall(target string) error {
	cmd := exec.Command(i.rBinary, "CMD", "INSTALL", "-l", i.req.LibraryPath, target)
	cmd.Dir = i.req.WorkDir
	cmd.Stdout = i.stdout
	cmd.Stderr = i.stderr
	cmd.Env = withLibraryEnv(os.Environ(), i.req.LibraryPath)
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (i *nativeInstaller) download(rawURL, name string) (string, error) {
	resp, err := getWithRetry(i.httpClient, rawURL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("unexpected HTTP %d from %s", resp.StatusCode, rawURL)
	}

	target := filepath.Join(i.tempRoot, name)
	file, err := os.Create(target)
	if err != nil {
		return "", err
	}
	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", err
	}
	return target, nil
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
	if err := runCommand(i.req.WorkDir, i.stdout, i.stderr, "git", "clone", cloneURL, cloneDir); err != nil {
		return "", fmt.Errorf("clone %s from %s: %w", pkg, rawURL, err)
	}
	if ref != "" {
		if err := runCommand(i.req.WorkDir, i.stdout, i.stderr, "git", "-C", cloneDir, "checkout", ref); err != nil {
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
	index, err := fetchRepoIndex(i.httpClient, strings.TrimRight(i.req.Repo, "/"), sourceCRAN)
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
	records, err := fetchRepoIndex(i.httpClient, biocMainRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor)
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
	records, err := fetchRepoIndex(i.httpClient, biocAnnotationRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor)
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
	records, err := fetchRepoIndex(i.httpClient, biocExperimentRepositoryURL(i.req.Runtime.RVersion), sourceBioconductor)
	if err != nil {
		return fmt.Errorf("load Bioconductor experiment index: %w", err)
	}
	i.biocExpLoaded = true
	i.biocExpIndex = records
	return nil
}

func fetchRepoIndex(client *http.Client, baseURL, source string) (map[string][]repoRecord, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	contribURL := baseURL + "/src/contrib"

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
			Name:         name,
			Version:      version,
			Dependencies: parseDependencies(fields["Depends"], fields["Imports"], fields["LinkingTo"]),
			TarballURL:   fmt.Sprintf("%s/%s_%s.tar.gz", contribURL, name, version),
			Source:       source,
			DepsLoaded:   true,
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
		Package:      fields["Package"],
		Version:      fields["Version"],
		Dependencies: parseDependencies(fields["Depends"], fields["Imports"], fields["LinkingTo"]),
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

	cmd := exec.Command(interpreter, "-e", `cat(file.path(R.home("bin"), "R"))`)
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

func runCommand(workDir string, stdout, stderr io.Writer, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = workDir
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
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
