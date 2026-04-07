package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/rainoffallingstar/rs-reborn/internal/cli"
	"github.com/rainoffallingstar/rs-reborn/internal/runner"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		var exitErr runner.ExitError
		if errors.As(err, &exitErr) {
			os.Exit(exitErr.Code)
		}
		var doctorErr runner.DoctorError
		if errors.As(err, &doctorErr) {
			if doctorErr.Code != 0 {
				os.Exit(doctorErr.Code)
			}
			os.Exit(1)
		}
		var reportedErr runner.ReportedError
		if errors.As(err, &reportedErr) {
			os.Exit(1)
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
