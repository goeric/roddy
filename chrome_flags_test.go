package main

import (
	"strings"
	"testing"

	"github.com/go-rod/rod/lib/launcher"
)

// features returns the --disable-features values as rod would render them.
func features(l *launcher.Launcher) string {
	values, ok := l.GetFlags("disable-features")
	if !ok {
		return ""
	}
	return strings.Join(values, ",")
}

// HistoryEmbeddings runs in the browser process, so its CHECK failure takes the
// whole browser down rather than one tab. It is enabled by the field-trial
// testing config baked into the pinned Chromium snapshot.
func TestConfigureExperiments_DisablesFieldTrialConfig(t *testing.T) {
	l := configureExperiments(launcher.New())

	if !l.Has("disable-field-trial-config") {
		t.Error("--disable-field-trial-config missing; the snapshot will force-enable in-development features")
	}
}

func TestConfigureExperiments_DisablesHistoryEmbeddings(t *testing.T) {
	l := configureExperiments(launcher.New())

	if !strings.Contains(features(l), "HistoryEmbeddings") {
		t.Errorf("disable-features = %q, want it to contain HistoryEmbeddings", features(l))
	}
}

// rod disables features of its own, and configureExtensions appends one more.
// Setting rather than appending would silently re-enable them.
func TestConfigureExperiments_KeepsExistingDisabledFeatures(t *testing.T) {
	l := configureExperiments(launcher.New().Set("disable-features", "SomeRodDefault", "TranslateUI"))

	got := features(l)
	for _, want := range []string{"SomeRodDefault", "TranslateUI", "HistoryEmbeddings"} {
		if !strings.Contains(got, want) {
			t.Errorf("disable-features = %q, want it to contain %q", got, want)
		}
	}
}

// Later callers append to the same flag; the values must accumulate rather than
// replace each other.
func TestConfigureExperiments_LeavesRoomForLaterAppends(t *testing.T) {
	l := configureExperiments(launcher.New().Set("disable-features", "SomeRodDefault"))
	l = l.Append("disable-features", "SomeLaterFeature")

	got := features(l)
	for _, want := range []string{"SomeRodDefault", "HistoryEmbeddings", "SomeLaterFeature"} {
		if !strings.Contains(got, want) {
			t.Errorf("disable-features = %q, want it to contain %q", got, want)
		}
	}
}

// launcher.New() turns Site Isolation off twice; both have to go, and the rest
// of --disable-features has to survive.
func TestConfigureSiteIsolation_UndoesBothRodDefaults(t *testing.T) {
	l := configureSiteIsolation(launcher.New())

	if l.Has("disable-site-isolation-trials") {
		t.Error("--disable-site-isolation-trials survived; it disables site isolation on its own")
	}
	got := features(l)
	if strings.Contains(got, "site-per-process") {
		t.Errorf("disable-features = %q, want it NOT to contain site-per-process", got)
	}
	if !strings.Contains(got, "TranslateUI") {
		t.Errorf("disable-features = %q, want it to keep TranslateUI", got)
	}
}

// Filtering, not rewriting: values other callers appended stay put whichever
// order the configure* helpers run in.
func TestConfigureSiteIsolation_KeepsOtherDisabledFeatures(t *testing.T) {
	l := configureSiteIsolation(launcher.New().Set("disable-features", "site-per-process", "SomeLaterFeature"))

	got := features(l)
	if strings.Contains(got, "site-per-process") {
		t.Errorf("disable-features = %q, want it NOT to contain site-per-process", got)
	}
	if !strings.Contains(got, "SomeLaterFeature") {
		t.Errorf("disable-features = %q, want it to keep SomeLaterFeature", got)
	}
}

// An emptied flag is deleted rather than passed as --disable-features=.
func TestConfigureSiteIsolation_DeletesAnEmptiedFlag(t *testing.T) {
	l := configureSiteIsolation(launcher.New().Set("disable-features", "site-per-process"))

	if l.Has("disable-features") {
		t.Errorf("disable-features = %q, want the flag deleted", features(l))
	}
}

// On macOS --single-process makes any navigator.mediaDevices call abort the
// browser; on Linux it is what makes screenshots work under gVisor.
func TestSingleProcessSupported_SkipsMacOSOnly(t *testing.T) {
	for _, goos := range []string{"linux", "windows", "freebsd"} {
		if !singleProcessSupported(goos) {
			t.Errorf("singleProcessSupported(%q) = false, want true", goos)
		}
	}
	if singleProcessSupported("darwin") {
		t.Error(`singleProcessSupported("darwin") = true, want false`)
	}
}
