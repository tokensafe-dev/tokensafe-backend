package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/tokensafe-dev/tokensafe-backend/benchmark/runner"
)

func main() {
	outputDir := "."
	if len(os.Args) > 1 {
		outputDir = os.Args[1]
	} else {
		// Default to benchmark/ directory relative to repo root
		wd, err := os.Getwd()
		if err == nil {
			outputDir = filepath.Join(wd, "benchmark")
		}
	}

	_, _, err := runner.RunBenchmark(outputDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Benchmark failed: %v\n", err)
		os.Exit(1)
	}
}
