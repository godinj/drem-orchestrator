package depth

// GrowthReport compares depth metrics between two states of a package
// to detect when its exported surface grows faster than its implementation.
type GrowthReport struct {
	PackagePath        string
	OldExportedSymbols int
	NewExportedSymbols int
	OldTotalLOC        int
	NewTotalLOC        int
	ExportGrowthRate   float64 // percentage change in exported symbols
	LOCGrowthRate      float64 // percentage change in LOC
	Disproportionate   bool    // true if export growth rate > LOC growth rate
}

// CompareGrowth compares two depth reports for the same package and flags
// disproportionate interface growth. The old report represents the previous
// state and the new report represents the current state.
func CompareGrowth(old, new *DepthReport) *GrowthReport {
	gr := &GrowthReport{
		PackagePath:        new.PackagePath,
		OldExportedSymbols: old.ExportedSymbols,
		NewExportedSymbols: new.ExportedSymbols,
		OldTotalLOC:        old.TotalLOC,
		NewTotalLOC:        new.TotalLOC,
	}

	// Calculate export growth rate as percentage change.
	if old.ExportedSymbols > 0 {
		gr.ExportGrowthRate = float64(new.ExportedSymbols-old.ExportedSymbols) / float64(old.ExportedSymbols) * 100
	} else if new.ExportedSymbols > 0 {
		// From zero to something is infinite growth; represent as 100%.
		gr.ExportGrowthRate = 100
	}

	// Calculate LOC growth rate as percentage change.
	if old.TotalLOC > 0 {
		gr.LOCGrowthRate = float64(new.TotalLOC-old.TotalLOC) / float64(old.TotalLOC) * 100
	} else if new.TotalLOC > 0 {
		gr.LOCGrowthRate = 100
	}

	// Disproportionate if exports are growing faster than implementation.
	gr.Disproportionate = gr.ExportGrowthRate > gr.LOCGrowthRate

	return gr
}
