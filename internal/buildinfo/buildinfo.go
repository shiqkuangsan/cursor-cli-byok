package buildinfo

// Version is replaced at build time with -ldflags "-X .../buildinfo.Version=<version>".
var Version = "dev"
