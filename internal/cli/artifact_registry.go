package cli

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/godinj/drem-orchestrator/internal/artifactregistry"
)

type artifactRegistryValidationJSON struct {
	OK       bool                               `json:"ok"`
	Errors   int                                `json:"errors"`
	Warnings int                                `json:"warnings"`
	Issues   []artifactregistry.ValidationIssue `json:"issues"`
}

func handleArtifactRegistry(db *gorm.DB, args []string, w io.Writer, jsonMode bool) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: drem cli artifact-registry <validate>")
	}

	sub := args[0]
	switch sub {
	case "validate", "audit":
		return handleArtifactRegistryValidate(db, w, jsonMode)
	default:
		return fmt.Errorf("unknown artifact-registry subcommand: %q", sub)
	}
}

func handleArtifactRegistryValidate(db *gorm.DB, w io.Writer, jsonMode bool) error {
	report, err := artifactregistry.NewRegistry(db).Validate(context.Background())
	if err != nil {
		return err
	}
	errors := report.ErrorCount()
	warnings := report.WarningCount()

	if jsonMode {
		if err := writeJSON(w, artifactRegistryValidationJSON{
			OK:       errors == 0,
			Errors:   errors,
			Warnings: warnings,
			Issues:   report.Issues,
		}); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(w, "Artifact registry validation: %d error(s), %d warning(s)\n", errors, warnings)
		if len(report.Issues) > 0 {
			tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "SEVERITY\tARTIFACT\tCONTENT_URI\tMESSAGE")
			for _, issue := range report.Issues {
				artifactID := "-"
				if issue.ArtifactID != uuid.Nil {
					artifactID = shortID(issue.ArtifactID)
				}
				contentURI := issue.ContentURI
				if contentURI == "" {
					contentURI = "-"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", issue.Severity, artifactID, contentURI, issue.Message)
			}
			if err := tw.Flush(); err != nil {
				return err
			}
		}
	}

	if errors > 0 {
		return fmt.Errorf("artifact registry validation failed: %d error(s)", errors)
	}
	return nil
}
