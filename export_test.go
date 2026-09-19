package engineer

import "github.com/pacenote-sim/clientplugin"

// Compare is compare, for the tests.
func Compare(corners, ref []clientplugin.Corner) []ReportCorner { return compare(corners, ref) }

// ReportCorner is reportCorner.
type ReportCorner = reportCorner
