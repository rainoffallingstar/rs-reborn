package lockfile

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type File struct {
	Version     int       `json:"version"`
	GeneratedAt time.Time `json:"generated_at"`
	Script      string    `json:"script"`
	Repo        string    `json:"repo,omitempty"`
	Library     string    `json:"library"`
	Metadata    Metadata  `json:"metadata"`
	Packages    []Package `json:"packages"`
}

type Metadata struct {
	Interpreter string `json:"interpreter"`
	RVersion    string `json:"r_version"`
	Platform    string `json:"platform"`
	Arch        string `json:"arch,omitempty"`
	OS          string `json:"os,omitempty"`
	PackageType string `json:"package_type,omitempty"`
}

type Package struct {
	Name                  string `json:"name"`
	Version               string `json:"version"`
	Source                string `json:"source"`
	SourceHost            string `json:"source_host,omitempty"`
	SourceLocation        string `json:"source_location,omitempty"`
	SourceRef             string `json:"source_ref,omitempty"`
	SourceCommit          string `json:"source_commit,omitempty"`
	SourceSubdir          string `json:"source_subdir,omitempty"`
	SourceFingerprint     string `json:"source_fingerprint,omitempty"`
	SourceFingerprintKind string `json:"source_fingerprint_kind,omitempty"`
	Priority              string `json:"priority,omitempty"`
}

func Write(path string, file File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lockfile dir: %w", err)
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal lockfile: %w", err)
	}

	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write lockfile: %w", err)
	}
	return nil
}

func Read(path string) (File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return File{}, err
		}
		return File{}, fmt.Errorf("read lockfile: %w", err)
	}

	var file File
	if err := json.Unmarshal(data, &file); err != nil {
		return File{}, fmt.Errorf("parse lockfile: %w", err)
	}
	return file, nil
}
