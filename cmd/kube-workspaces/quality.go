package main

import "flag"

// Explicit quality/compression values retain their historical fixed meaning.
// An explicit --adaptive-quality=true takes precedence, independent of flag
// order; callers can keep a script's old flags while opting into adaptation.
func adaptiveQualityEnabled(fs *flag.FlagSet, enabled bool) bool {
	manual, explicit := false, false
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "quality", "compress":
			manual = true
		case "adaptive-quality":
			explicit = true
		}
	})
	return enabled && (!manual || explicit)
}
