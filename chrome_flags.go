package main

import (
	"github.com/go-rod/rod/lib/launcher"
)

// configureExperiments stops Chromium from running features that are still
// being written.
//
// rodney's pinned browser (128.0.6568.0) is a development snapshot, and those
// builds apply testing/variations/fieldtrial_testing_config.json by default,
// which force-enables in-development features. One of them, HistoryEmbeddings,
// computes passage embeddings on the browser's main thread after a navigation
// and CHECK-fails when its cache lookup misses, killing the browser process a
// few seconds after a text-heavy page loads:
//
//	FATAL:history_embeddings_service.cc(597)] Check failed:
//	cached_embedding != embedding_cache.end()
//
// It needs the on-device model the optimization guide downloads, so it only
// starts biting once a profile has been used for a while — a long-lived
// ~/.rodney profile crashes where a throwaway one does not.
//
// --disable-field-trial-config drops that config wholesale, so rodney drives a
// browser with shipped defaults rather than whatever happened to be mid-flight
// when the snapshot was cut. HistoryEmbeddings is named explicitly as well
// because a browser supplied through ROD_CHROME_BIN can enable it from a
// server-side variations seed, which that switch does not cover.
func configureExperiments(l *launcher.Launcher) *launcher.Launcher {
	// Append, not Set: rod already disables features of its own.
	return l.Set("disable-field-trial-config").
		Append("disable-features", "HistoryEmbeddings")
}
