package model

// CanaryV17Marker is a build-time marker used to verify that v17 of the
// model package is present in a deployed binary.
type CanaryV17Marker struct {
	Label string
	At    string
}
