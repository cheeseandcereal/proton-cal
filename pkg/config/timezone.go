package config

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DetectTimezone best-effort detects the system IANA timezone (Go can't name
// the local zone), checking TZ, the /etc/localtime zoneinfo symlink, then
// /etc/timezone, falling back to "UTC".
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
