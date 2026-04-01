package installer

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkLoadInstalledPackageFromLibraryStoreStateFastPath(b *testing.B) {
	library := b.TempDir()
	pkg := plannedPackage{Name: "cli", Version: "3.6.5", Source: sourceCRAN}
	runtime := Runtime{
		Interpreter:     filepath.Join(library, "bin", "Rscript"),
		InterpreterKind: "managed",
	}
	if err := writePackageStoreState(library, pkg, runtime, PackageStoreState{
		UpdatedAt:  time.Now().UTC().Format(time.RFC3339),
		LastUsedAt: time.Now().UTC().Format(time.RFC3339),
	}); err != nil {
		b.Fatalf("writePackageStoreState() error = %v", err)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, ok, err := loadInstalledPackageFromLibrary(library, "cli")
		if err != nil {
			b.Fatalf("loadInstalledPackageFromLibrary() error = %v", err)
		}
		if !ok {
			b.Fatal("loadInstalledPackageFromLibrary() ok = false, want true")
		}
		if got.Version != "3.6.5" {
			b.Fatalf("loadInstalledPackageFromLibrary() version = %q, want 3.6.5", got.Version)
		}
	}
}

func BenchmarkFindReusablePackagesInLibrary(b *testing.B) {
	library := b.TempDir()
	if err := os.MkdirAll(filepath.Join(library, ".rs-source-meta"), 0o755); err != nil {
		b.Fatalf("MkdirAll(.rs-source-meta) error = %v", err)
	}

	remaining := map[string]plannedPackage{}
	for i := 0; i < 256; i++ {
		name := fmt.Sprintf("pkg%03d", i)
		path := filepath.Join(library, name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			b.Fatalf("MkdirAll(%q) error = %v", path, err)
		}
		if i >= 8 {
			continue
		}
		desc := fmt.Sprintf("Package: %s\nVersion: 1.0.%d\nRepository: CRAN\n", name, i)
		if err := os.WriteFile(filepath.Join(path, "DESCRIPTION"), []byte(desc), 0o644); err != nil {
			b.Fatalf("WriteFile(%q) error = %v", filepath.Join(path, "DESCRIPTION"), err)
		}
		remaining[name] = plannedPackage{Name: name, Version: fmt.Sprintf("1.0.%d", i), Source: sourceCRAN}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		got, err := findReusablePackagesInLibrary(library, remaining)
		if err != nil {
			b.Fatalf("findReusablePackagesInLibrary() error = %v", err)
		}
		if len(got) != len(remaining) {
			b.Fatalf("findReusablePackagesInLibrary() len = %d, want %d", len(got), len(remaining))
		}
	}
}

func BenchmarkDiscoverReusablePackagesInLibraries(b *testing.B) {
	root := b.TempDir()
	libraries := make([]cacheSeedLibrary, 0, 6)
	remaining := map[string]plannedPackage{}

	for libIdx := 0; libIdx < 6; libIdx++ {
		libraryPath := filepath.Join(root, fmt.Sprintf("lib-%d", libIdx))
		if err := os.MkdirAll(filepath.Join(libraryPath, ".rs-source-meta"), 0o755); err != nil {
			b.Fatalf("MkdirAll(%q) error = %v", filepath.Join(libraryPath, ".rs-source-meta"), err)
		}
		for pkgIdx := 0; pkgIdx < 4; pkgIdx++ {
			name := fmt.Sprintf("pkg-%d-%d", libIdx, pkgIdx)
			pkgDir := filepath.Join(libraryPath, name)
			if err := os.MkdirAll(pkgDir, 0o755); err != nil {
				b.Fatalf("MkdirAll(%q) error = %v", pkgDir, err)
			}
			version := fmt.Sprintf("1.%d.%d", libIdx, pkgIdx)
			desc := fmt.Sprintf("Package: %s\nVersion: %s\nRepository: CRAN\n", name, version)
			if err := os.WriteFile(filepath.Join(pkgDir, "DESCRIPTION"), []byte(desc), 0o644); err != nil {
				b.Fatalf("WriteFile(%q) error = %v", filepath.Join(pkgDir, "DESCRIPTION"), err)
			}
			remaining[name] = plannedPackage{Name: name, Version: version, Source: sourceCRAN}
		}
		libraries = append(libraries, cacheSeedLibrary{
			entryName: fmt.Sprintf("lib-%d", libIdx),
			path:      libraryPath,
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		results, err := discoverReusablePackagesInLibraries(libraries, remaining)
		if err != nil {
			b.Fatalf("discoverReusablePackagesInLibraries() error = %v", err)
		}
		if len(results) != len(libraries) {
			b.Fatalf("discoverReusablePackagesInLibraries() len = %d, want %d", len(results), len(libraries))
		}
	}
}

func BenchmarkPrefetchPlannedPackagesCachedArtifacts(b *testing.B) {
	dir := b.TempDir()
	downloadRoot := filepath.Join(dir, "downloads")
	if err := os.MkdirAll(downloadRoot, 0o755); err != nil {
		b.Fatalf("MkdirAll(%q) error = %v", downloadRoot, err)
	}

	records := make([]repoRecord, 0, 16)
	for i := 0; i < 16; i++ {
		record := repoRecord{
			Name:       fmt.Sprintf("pkg%02d", i),
			Version:    fmt.Sprintf("1.0.%d", i),
			Source:     sourceCRAN,
			TarballURL: fmt.Sprintf("https://example.test/src/contrib/pkg%02d_1.0.%d.tar.gz", i, i),
			DepsLoaded: false,
		}
		archivePath := filepath.Join(downloadRoot, downloadCacheName(record.TarballURL, repoDownloadName(record)))
		if err := os.MkdirAll(filepath.Dir(archivePath), 0o755); err != nil {
			b.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(archivePath), err)
		}
		archive := benchmarkTarGzBytes(b, map[string]string{
			fmt.Sprintf("%s/DESCRIPTION", record.Name): fmt.Sprintf("Package: %s\nVersion: %s\nImports: cli\nNeedsCompilation: no\n", record.Name, record.Version),
		})
		if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
			b.Fatalf("WriteFile(%q) error = %v", archivePath, err)
		}
		desc := description{
			Package:          record.Name,
			Version:          record.Version,
			Dependencies:     []packageRequirement{{Name: "cli"}},
			NeedsCompilation: false,
		}
		writeDescriptionSidecar(archivePath, desc)
		records = append(records, record)
	}

	newInstaller := func() nativeInstaller {
		planned := make(map[string]plannedPackage, len(records))
		order := make([]string, 0, len(records))
		for _, record := range records {
			record := record
			planned[record.Name] = plannedPackage{
				Name:    record.Name,
				Version: record.Version,
				Source:  sourceCRAN,
				Repo:    &record,
			}
			order = append(order, record.Name)
		}
		return nativeInstaller{
			downloadRoot:     downloadRoot,
			stderr:           io.Discard,
			prefetchedRepo:   map[string]string{},
			descriptionCache: map[string]description{},
			planned:          planned,
			order:            order,
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		inst := newInstaller()
		if err := inst.prefetchPlannedPackages(); err != nil {
			b.Fatalf("prefetchPlannedPackages() error = %v", err)
		}
	}
}

func benchmarkTarGzBytes(b *testing.B, files map[string]string) []byte {
	b.Helper()
	var gzBuf bytes.Buffer
	gzWriter := gzip.NewWriter(&gzBuf)
	tarWriter := tar.NewWriter(gzWriter)
	for name, body := range files {
		data := []byte(body)
		if err := tarWriter.WriteHeader(&tar.Header{
			Name: name,
			Mode: 0o644,
			Size: int64(len(data)),
		}); err != nil {
			b.Fatalf("WriteHeader(%q) error = %v", name, err)
		}
		if _, err := tarWriter.Write(data); err != nil {
			b.Fatalf("Write(%q) error = %v", name, err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		b.Fatalf("tarWriter.Close() error = %v", err)
	}
	if err := gzWriter.Close(); err != nil {
		b.Fatalf("gzWriter.Close() error = %v", err)
	}
	return gzBuf.Bytes()
}
