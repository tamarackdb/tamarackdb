// Package buildinfo holds the version string every TamarackDB binary
// shares. It's set at build time, with
// -ldflags "-X github.com/tamarackdb/tamarackdb/internal/buildinfo.Version=...",
// and never read or changed at runtime.
package buildinfo
