package rdeps

import (
	"os"
	"regexp"
	"sort"
)

var (
	libraryPattern          = regexp.MustCompile(`\b(?:library|require)\s*\(\s*["']?([A-Za-z][A-Za-z0-9.]*)["']?`)
	requireNamespacePattern = regexp.MustCompile(`\brequireNamespace\s*\(\s*["']([A-Za-z][A-Za-z0-9.]*)["']`)
	namespacePattern        = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9.]*)::[A-Za-z][A-Za-z0-9._]*`)
	internalPattern         = regexp.MustCompile(`\b([A-Za-z][A-Za-z0-9.]*):::[A-Za-z][A-Za-z0-9._]*`)
	bundledPackages         = map[string]struct{}{
		"base":       {},
		"boot":       {},
		"class":      {},
		"cluster":    {},
		"codetools":  {},
		"compiler":   {},
		"datasets":   {},
		"foreign":    {},
		"graphics":   {},
		"grDevices":  {},
		"grid":       {},
		"KernSmooth": {},
		"lattice":    {},
		"MASS":       {},
		"Matrix":     {},
		"methods":    {},
		"mgcv":       {},
		"nlme":       {},
		"nnet":       {},
		"parallel":   {},
		"rpart":      {},
		"spatial":    {},
		"splines":    {},
		"stats":      {},
		"stats4":     {},
		"survival":   {},
		"tcltk":      {},
		"tools":      {},
		"utils":      {},
	}
	knownBiocPackages = map[string]struct{}{
		"AnnotationDbi":        {},
		"Biobase":              {},
		"BiocGenerics":         {},
		"BiocParallel":         {},
		"Biostrings":           {},
		"ComplexHeatmap":       {},
		"DESeq2":               {},
		"edgeR":                {},
		"GenomicAlignments":    {},
		"GenomicFeatures":      {},
		"GenomicRanges":        {},
		"GEOquery":             {},
		"IRanges":              {},
		"limma":                {},
		"org.Hs.eg.db":         {},
		"org.Mm.eg.db":         {},
		"Rsamtools":            {},
		"S4Vectors":            {},
		"SingleCellExperiment": {},
		"SummarizedExperiment": {},
		"tximport":             {},
		"VariantAnnotation":    {},
	}
)

func FromFile(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	return FromSource(string(content)), nil
}

func FromSource(src string) []string {
	cleaned := stripComments(src)
	seen := map[string]struct{}{}

	for _, match := range libraryPattern.FindAllStringSubmatch(cleaned, -1) {
		seen[match[1]] = struct{}{}
	}
	for _, match := range requireNamespacePattern.FindAllStringSubmatch(cleaned, -1) {
		seen[match[1]] = struct{}{}
	}
	for _, match := range namespacePattern.FindAllStringSubmatch(cleaned, -1) {
		seen[match[1]] = struct{}{}
	}
	for _, match := range internalPattern.FindAllStringSubmatch(cleaned, -1) {
		seen[match[1]] = struct{}{}
	}

	deps := make([]string, 0, len(seen))
	for dep := range seen {
		deps = append(deps, dep)
	}
	sort.Strings(deps)
	return deps
}

func IsBundledPackage(name string) bool {
	_, ok := bundledPackages[name]
	return ok
}

func FilterInstallable(deps []string) []string {
	filtered := make([]string, 0, len(deps))
	for _, dep := range deps {
		if IsBundledPackage(dep) {
			continue
		}
		filtered = append(filtered, dep)
	}
	return filtered
}

func IsKnownBiocPackage(name string) bool {
	_, ok := knownBiocPackages[name]
	return ok
}

func SplitBiocPackages(deps []string) ([]string, []string) {
	cran := make([]string, 0, len(deps))
	bioc := make([]string, 0, len(deps))
	for _, dep := range deps {
		if IsKnownBiocPackage(dep) {
			bioc = append(bioc, dep)
			continue
		}
		cran = append(cran, dep)
	}
	return cran, bioc
}

func stripComments(src string) string {
	runes := []rune(src)
	out := make([]rune, 0, len(runes))
	inSingle := false
	inDouble := false
	escape := false

	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escape:
			out = append(out, r)
			escape = false
		case r == '\\' && (inSingle || inDouble):
			out = append(out, r)
			escape = true
		case r == '\'' && !inDouble:
			inSingle = !inSingle
			out = append(out, r)
		case r == '"' && !inSingle:
			inDouble = !inDouble
			out = append(out, r)
		case r == '#' && !inSingle && !inDouble:
			for i < len(runes) && runes[i] != '\n' {
				i++
			}
			if i < len(runes) && runes[i] == '\n' {
				out = append(out, '\n')
			}
		default:
			out = append(out, r)
		}
	}

	return string(out)
}
