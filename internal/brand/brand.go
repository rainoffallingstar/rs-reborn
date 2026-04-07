package brand

import "strings"

const (
	CLIName       = "rvx"
	LegacyCLIName = "rs"
)

func Command(parts ...string) string {
	cleaned := make([]string, 0, len(parts)+1)
	cleaned = append(cleaned, CLIName)
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		cleaned = append(cleaned, part)
	}
	return strings.Join(cleaned, " ")
}
