package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/nogataka/gridbase-oss/tunnel/pkg/buildinfo"
)

func main() {
	var jsonOutput bool
	flag.BoolVar(&jsonOutput, "json", false, "print build info as JSON")
	flag.Parse()

	info := buildinfo.Current()
	if jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(info); err != nil {
			fmt.Fprintf(os.Stderr, "failed to encode build info: %v\n", err)
			os.Exit(1)
		}
		return
	}

	fmt.Fprintf(os.Stdout, "Version: %s\nCommit: %s\nBuilt: %s\n", info.Version, info.CommitSHA, info.BuildTime)
}
