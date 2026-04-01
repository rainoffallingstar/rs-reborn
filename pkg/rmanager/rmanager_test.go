package rmanager_test

import (
	"strings"
	"testing"

	publicrmanager "github.com/rainoffallingstar/rs-reborn/pkg/rmanager"
)

func TestPublicVersionHelpers(t *testing.T) {
	if !publicrmanager.LooksLikeVersionSpec("4.5") {
		t.Fatal("LooksLikeVersionSpec(4.5) = false, want true")
	}
	if publicrmanager.LooksLikeVersionSpec("/usr/bin/Rscript") {
		t.Fatal("LooksLikeVersionSpec(/usr/bin/Rscript) = true, want false")
	}
	if !publicrmanager.VersionMatchesSpec("4.5", "4.5.3") {
		t.Fatal("VersionMatchesSpec(4.5, 4.5.3) = false, want true")
	}
	if publicrmanager.VersionMatchesSpec("4.4", "4.5.3") {
		t.Fatal("VersionMatchesSpec(4.4, 4.5.3) = true, want false")
	}
}

func TestPublicBootstrapAdviceIncludesCommand(t *testing.T) {
	advice := publicrmanager.BootstrapAdviceFor("4.5.3")
	if strings.TrimSpace(advice.AutoEnableEnv) == "" {
		t.Fatalf("AutoEnableEnv = %q", advice.AutoEnableEnv)
	}
	if !strings.Contains(advice.ManualMessageWithCommand(), "rs r install 4.5.3") {
		t.Fatalf("ManualMessageWithCommand() = %q", advice.ManualMessageWithCommand())
	}
}
