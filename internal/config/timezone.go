package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DetectTimezone best-effort detects the system's IANA timezone name.
// Go cannot name the local zone (time.Local.String() == "Local"), so this
// checks, in order: the TZ env var, the /etc/localtime symlink into the
// zoneinfo database, then /etc/timezone. Falls back to "UTC".
func DetectTimezone() string {
	var candidates []string

	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		candidates = append(candidates, tz)
	}

	if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(target, "/zoneinfo/"); i >= 0 {
			candidates = append(candidates, target[i+len("/zoneinfo/"):])
		}
	}

	if data, err := os.ReadFile("/etc/timezone"); err == nil {
		candidates = append(candidates, strings.TrimSpace(string(data)))
	}

	for _, name := range candidates {
		if name == "" {
			continue
		}
		if _, err := time.LoadLocation(name); err == nil {
			return name
		}
	}
	return "UTC"
}
