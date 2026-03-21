package model

import "fmt"

// BugReportCategory classifies the type of problem a bug report describes.
type BugReportCategory string

const (
	BugCategoryTooling             BugReportCategory = "tooling"
	BugCategoryMergeConflict       BugReportCategory = "merge_conflict"
	BugCategoryRequirements        BugReportCategory = "requirements"
	BugCategoryConstraintViolation BugReportCategory = "constraint_violation"
	BugCategoryUpstreamCode        BugReportCategory = "upstream_code"
	BugCategoryTestFailure         BugReportCategory = "test_failure"
	BugCategoryEnvironment         BugReportCategory = "environment"
	BugCategoryOther               BugReportCategory = "other"
)

// allBugReportCategories lists every valid BugReportCategory value for parsing.
var allBugReportCategories = []BugReportCategory{
	BugCategoryTooling,
	BugCategoryMergeConflict,
	BugCategoryRequirements,
	BugCategoryConstraintViolation,
	BugCategoryUpstreamCode,
	BugCategoryTestFailure,
	BugCategoryEnvironment,
	BugCategoryOther,
}

// String returns the string representation of a BugReportCategory.
func (c BugReportCategory) String() string {
	return string(c)
}

// ParseBugReportCategory converts a raw string to a BugReportCategory,
// returning an error if the string does not match any known value.
func ParseBugReportCategory(s string) (BugReportCategory, error) {
	for _, c := range allBugReportCategories {
		if string(c) == s {
			return c, nil
		}
	}
	return "", fmt.Errorf("unknown bug report category: %q", s)
}

// BugReportSeverity indicates the impact level of a bug report.
type BugReportSeverity string

const (
	BugSeverityBlocking      BugReportSeverity = "blocking"
	BugSeverityDegraded      BugReportSeverity = "degraded"
	BugSeverityInformational BugReportSeverity = "informational"
)

// allBugReportSeverities lists every valid BugReportSeverity value for parsing.
var allBugReportSeverities = []BugReportSeverity{
	BugSeverityBlocking,
	BugSeverityDegraded,
	BugSeverityInformational,
}

// String returns the string representation of a BugReportSeverity.
func (s BugReportSeverity) String() string {
	return string(s)
}

// ParseBugReportSeverity converts a raw string to a BugReportSeverity,
// returning an error if the string does not match any known value.
func ParseBugReportSeverity(s string) (BugReportSeverity, error) {
	for _, sv := range allBugReportSeverities {
		if string(sv) == s {
			return sv, nil
		}
	}
	return "", fmt.Errorf("unknown bug report severity: %q", s)
}

// BugReportStatus represents the lifecycle state of a bug report.
type BugReportStatus string

const (
	BugStatusOpen         BugReportStatus = "open"
	BugStatusAcknowledged BugReportStatus = "acknowledged"
	BugStatusPromoted     BugReportStatus = "promoted"
	BugStatusDismissed    BugReportStatus = "dismissed"
)

// allBugReportStatuses lists every valid BugReportStatus value for parsing.
var allBugReportStatuses = []BugReportStatus{
	BugStatusOpen,
	BugStatusAcknowledged,
	BugStatusPromoted,
	BugStatusDismissed,
}

// String returns the string representation of a BugReportStatus.
func (s BugReportStatus) String() string {
	return string(s)
}

// ParseBugReportStatus converts a raw string to a BugReportStatus,
// returning an error if the string does not match any known value.
func ParseBugReportStatus(s string) (BugReportStatus, error) {
	for _, st := range allBugReportStatuses {
		if string(st) == s {
			return st, nil
		}
	}
	return "", fmt.Errorf("unknown bug report status: %q", s)
}
