// Package version: versi build yang di-inject lewat -ldflags saat release.
//
// Default "dev" untuk build lokal. CI mengisi:
//
//	go build -ldflags "-X github.com/jhopan/agenpulsa-server/internal/version.Version=v1.2.0 \
//	                   -X github.com/jhopan/agenpulsa-server/internal/version.Commit=abc1234 \
//	                   -X github.com/jhopan/agenpulsa-server/internal/version.BuildDate=2026-10-25T20:00:00Z"
package version

var (
	// Version tag rilis, mis. "v1.2.0". Default "dev" (build lokal).
	Version = "dev"
	// Commit SHA pendek saat build.
	Commit = ""
	// BuildDate waktu build (RFC3339).
	BuildDate = ""
)

// String: "v1.2.0 (abc1234, 2026-10-25T20:00:00Z)" atau "dev".
func String() string {
	if Version == "" || Version == "dev" {
		return "dev"
	}
	s := Version
	if Commit != "" {
		s += " (" + Commit
		if BuildDate != "" {
			s += ", " + BuildDate
		}
		s += ")"
	}
	return s
}
