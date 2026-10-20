package buildinfo

// Info captures build metadata injected via ldflags.
type Info struct {
	Version   string `json:"version"`
	CommitSHA string `json:"commit_sha"`
	BuildTime string `json:"build_time"`
}

var (
	// Version represents the semantic version or channel for this build.
	Version = "dev"
	// CommitSHA identifies the source revision used at build time.
	CommitSHA = "unknown"
	// BuildTime records the UTC timestamp when the binary was produced.
	BuildTime = "unknown"
)

// Current returns the active build metadata.
func Current() Info {
	return Info{
		Version:   Version,
		CommitSHA: CommitSHA,
		BuildTime: BuildTime,
	}
}
