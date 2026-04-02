package runner

import (
	"fmt"
	"testing"

	"github.com/rainoffallingstar/rs-reborn/internal/project"
)

func BenchmarkPreviewStringsLargeSlice(b *testing.B) {
	values := make([]string, 0, 256)
	for i := 0; i < 256; i++ {
		values = append(values, fmt.Sprintf("pkg%03d", i))
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := previewStrings(values, 8)
		if out == "" {
			b.Fatal("previewStrings() returned empty output")
		}
	}
}

func BenchmarkSourceSummaryAndPreview(b *testing.B) {
	sourceDeps := make(map[string]project.SourceSpec, 128)
	for i := 0; i < 128; i++ {
		name := fmt.Sprintf("pkg%03d", i)
		sourceDeps[name] = project.SourceSpec{
			Package: name,
			Type:    "github",
			Repo:    fmt.Sprintf("owner/%s", name),
			Ref:     "main",
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := previewStrings(sourceSummary(sourceDeps), 6)
		if out == "" {
			b.Fatal("sourceSummary preview returned empty output")
		}
	}
}
