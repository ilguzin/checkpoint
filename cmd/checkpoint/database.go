package main

import "os"

// Native setup owns this marker. Detect it without reading its private contents,
// so local administration also works as the checkpoint service user.
func databasePath() string {
	return resolveDatabasePath(os.Getenv("CHECKPOINT_DATABASE"), "/etc/checkpoint/setup-state.json")
}

func resolveDatabasePath(explicit, nativeMarker string) string {
	if explicit != "" {
		return explicit
	}
	if info, err := os.Lstat(nativeMarker); err == nil && info.Mode().IsRegular() {
		return "/var/lib/checkpoint/checkpoint.db"
	}
	return "/data/checkpoint.db"
}
