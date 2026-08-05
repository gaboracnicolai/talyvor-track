package config

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

// ⚠ A DOCUMENTED KNOB THAT DOES NOTHING.
//
// TRACK_LOG_LEVEL is in .env.example, forwarded by docker-compose, and parsed into Config.LogLevel
// — and nothing in the repository ever read that field. The logger is built as
// slog.NewJSONHandler(os.Stdout, nil), and nil options mean the package default, Info. So setting
// TRACK_LOG_LEVEL=debug changed nothing at all, silently, and an operator turning it up during an
// incident would conclude the problem was elsewhere.
//
// The value is only worth parsing if something can act on it, so this pins the parse.
func TestLogLevel_TheDocumentedValuesAreUnderstood(t *testing.T) {
	for in, want := range map[string]slog.Level{
		"debug": slog.LevelDebug,
		"info":  slog.LevelInfo,
		"warn":  slog.LevelWarn,
		"error": slog.LevelError,
		"INFO":  slog.LevelInfo, // case is not a reason to silently fall back
		"WARN":  slog.LevelWarn,
	} {
		if got := ParseLogLevel(in); got != want {
			t.Errorf("ParseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}

// ⚠ AN UNRECOGNISED VALUE FALLS BACK TO Info RATHER THAN FAILING TO BOOT. A typo in a log level
// must not take the service down, but it must not silently mean "off" either.
func TestLogLevel_AnUnknownValueIsInfoNotSilence(t *testing.T) {
	for _, in := range []string{"", "verbose", "trace", "nonsense"} {
		if got := ParseLogLevel(in); got != slog.LevelInfo {
			t.Errorf("ParseLogLevel(%q) = %v, want Info", in, got)
		}
	}
}

// ⚠ AND THE PARSED LEVEL MUST ACTUALLY CHANGE WHAT IS EMITTED. Parsing correctly into a value
// nobody applies is the original defect wearing a different hat, so this builds the handler the
// way main does and checks a Debug record survives at debug and is dropped at info.
func TestLogLevel_TheParsedLevelChangesWhatIsEmitted(t *testing.T) {
	emits := func(env string) bool {
		var buf bytes.Buffer
		slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
			Level: ParseLogLevel(env),
		})).Debug("probe")
		return strings.Contains(buf.String(), "probe")
	}
	if !emits("debug") {
		t.Error("TRACK_LOG_LEVEL=debug does not emit debug records — the knob still does nothing")
	}
	if emits("info") {
		t.Error("TRACK_LOG_LEVEL=info emitted a debug record")
	}
	if emits("error") {
		t.Error("TRACK_LOG_LEVEL=error emitted a debug record")
	}
}
