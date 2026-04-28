package kclgen

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"github.com/warpstreamlabs/bento/internal/config/schema"
	"github.com/warpstreamlabs/bento/internal/docs"
)

// updateGolden rewrites the golden fixture rather than asserting against it.
// Use `go test ./internal/kclgen/... -run TestGolden -update` after making
// an intentional generator change.
var updateGolden = flag.Bool("update", false, "update golden file")

// goldenFixture builds a small but representative schema.Full covering
// scalar/object/array/map fields, mixin-bearing categories, keyword
// escaping, nested dedupe, decorators, and tag comments. The result is
// intentionally stable and does not depend on the registered component
// set so the golden file does not churn when new components are added.
func goldenFixture() schema.Full {
	tlsChildren := docs.FieldSpecs{
		docs.FieldBool("enabled", "Whether TLS is enabled.").HasDefault(false),
		docs.FieldString("root_cas", "PEM root CAs.").HasDefault(""),
	}
	tlsField := docs.FieldObject("tls", "TLS configuration.").WithChildren(tlsChildren...)

	generate := docs.ComponentSpec{
		Name:    "generate",
		Type:    docs.TypeInput,
		Summary: "Generate messages.",
		Config: docs.FieldObject("", "").WithChildren(
			docs.FieldString("mapping", "A bloblang mapping."),
			docs.FieldString("interval", "Interval between messages.").HasDefault("1s"),
			docs.FieldInt("count", "Number of messages to generate.").HasDefault(0),
			// "check" is a KCL keyword, should render as $check.
			docs.FieldString("check", "An optional check expression.").Optional(),
			tlsField,
		),
	}
	httpClient := docs.ComponentSpec{
		Name:    "http_client",
		Type:    docs.TypeInput,
		Summary: "Consume messages from an HTTP endpoint.",
		Config: docs.FieldObject("", "").WithChildren(
			docs.FieldString("url", "The endpoint URL."),
			docs.FieldString("token", "API token.").Secret().HasDefault(""),
			docs.FieldString("legacy", "Use url instead.").Deprecated().HasDefault(""),
			tlsField,
		),
	}
	stdout := docs.ComponentSpec{
		Name:    "stdout",
		Type:    docs.TypeOutput,
		Summary: "Write to stdout.",
		Config: docs.FieldObject("", "").WithChildren(
			docs.FieldString("codec", "Output codec.").HasDefault("lines"),
		),
	}

	// Array-of-processor root: `try: [Processor]`.
	tryProc := docs.ComponentSpec{
		Name:    "try",
		Type:    docs.TypeProcessor,
		Summary: "Executes a list of child processors.",
		Config:  docs.FieldProcessor("", "").Array(),
	}

	// Scalar-rooted processor: `mapping: str`.
	mappingProc := docs.ComponentSpec{
		Name:    "mapping",
		Type:    docs.TypeProcessor,
		Summary: "Executes a Bloblang mapping.",
		Config:  docs.FieldString("", ""),
	}

	// Array-of-object root: `switch: [{case, processors}]`.
	switchProc := docs.ComponentSpec{
		Name:    "switch",
		Type:    docs.TypeProcessor,
		Summary: "Conditionally routes messages to processors.",
		Config: docs.FieldObject("", "").WithChildren(
			docs.FieldString("check", "A Bloblang check expression.").Optional(),
			docs.FieldProcessor("processors", "Processors to apply when the case matches.").Array(),
		).Array(),
	}

	return schema.Full{
		Version: "golden",
		Date:    "test",
		Config: docs.FieldSpecs{
			docs.FieldInput("input", "The input."),
			docs.FieldOutput("output", "The output."),
		},
		Inputs:     []docs.ComponentSpec{generate, httpClient},
		Outputs:    []docs.ComponentSpec{stdout},
		Processors: []docs.ComponentSpec{tryProc, mappingProc, switchProc},
	}
}

func TestGolden(t *testing.T) {
	out, err := GenerateSchema(goldenFixture())
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}

	goldenPath := filepath.Join("testdata", "golden.k")

	if *updateGolden {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatalf("mkdir testdata: %v", err)
		}
		if err := os.WriteFile(goldenPath, out, 0o644); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden (run with -update to regenerate): %v", err)
	}
	if string(out) != string(want) {
		t.Errorf("generator output differs from golden file. Run `go test ./internal/kclgen/... -run TestGolden -update` to regenerate.\n--- got ---\n%s\n--- want ---\n%s", out, want)
	}
}
