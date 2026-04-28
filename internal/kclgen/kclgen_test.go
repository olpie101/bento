package kclgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/warpstreamlabs/bento/internal/config/schema"
	"github.com/warpstreamlabs/bento/internal/docs"
)

func TestEscapeFieldName(t *testing.T) {
	cases := map[string]string{
		"check":      "$check",
		"type":       "$type",
		"assert":     "$assert",
		"map":        "$map",
		"filter":     "$filter",
		"final":      "$final",
		"import":     "$import",
		"if":         "$if",
		"else":       "$else",
		"mapping":    "mapping",
		"processors": "processors",
		"url":        "url",
	}
	for in, want := range cases {
		if got := escapeFieldName(in); got != want {
			t.Errorf("escapeFieldName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestToSchemaName(t *testing.T) {
	cases := map[string]string{
		"generate":    "Generate",
		"http_client": "HttpClient",
		"amqp_0_9":    "Amqp09",
		"aws_s3":      "AwsS3",
		"rate-limit":  "RateLimit",
	}
	for in, want := range cases {
		if got := toSchemaName(in); got != want {
			t.Errorf("toSchemaName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderDefault(t *testing.T) {
	cases := []struct {
		in   any
		want string
	}{
		{nil, "None"},
		{true, "True"},
		{false, "False"},
		{"hi", `"hi"`},
		{int(3), "3"},
		{int64(4), "4"},
		{1.5, "1.5"},
		{[]any{1, "two"}, `[1, "two"]`},
		{map[string]any{}, "{}"},
	}
	for _, c := range cases {
		if got := renderDefault(c.in); got != c.want {
			t.Errorf("renderDefault(%#v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGenerateSchemaSortsComponents(t *testing.T) {
	mk := func(name string) docs.ComponentSpec {
		return docs.ComponentSpec{
			Name: name,
			Type: docs.TypeInput,
			Config: docs.FieldObject("", "").WithChildren(
				docs.FieldString("mapping", ""),
			),
		}
	}
	// Intentionally pass components out of alphabetical order.
	sch := schema.Full{
		Config: docs.FieldSpecs{docs.FieldInput("input", "")},
		Inputs: []docs.ComponentSpec{mk("zzz"), mk("aaa"), mk("mmm")},
	}
	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	src := string(out)

	// The per-component schemas should appear in alphabetical order.
	a := strings.Index(src, "schema AaaInput:")
	m := strings.Index(src, "schema MmmInput:")
	z := strings.Index(src, "schema ZzzInput:")
	if a < 0 || m < 0 || z < 0 {
		t.Fatalf("missing per-component schemas\n---\n%s", src)
	}
	if !(a < m && m < z) {
		t.Errorf("component schemas not sorted (a=%d m=%d z=%d)\n---\n%s", a, m, z, src)
	}

	// The union schema's variant list should also be sorted.
	if !strings.Contains(src, `len([x for x in [aaa, mmm, zzz] if x != Undefined]) == 1`) {
		t.Errorf("union variants not sorted alphabetically\n---\n%s", src)
	}
	// The `_variants` helper attribute must NOT be emitted — attributes
	// with leading underscores can still leak into rendered YAML, so the
	// variant list is inlined in the check expression instead.
	if strings.Contains(src, "_variants") {
		t.Errorf("generator still emits _variants helper attribute\n---\n%s", src)
	}
}

func TestGenerateSchemaTypeAliasDedupesSharedEnums(t *testing.T) {
	// Two distinct inputs share the same codec enum; a third option set
	// differs and should stay inline.
	codecOpts := []string{"lines", "all-bytes", "tar"}
	mk := func(name string, opts []string) docs.ComponentSpec {
		return docs.ComponentSpec{
			Name: name,
			Type: docs.TypeInput,
			Config: docs.FieldObject("", "").WithChildren(
				docs.FieldString("codec", "The codec.").HasOptions(opts...),
			),
		}
	}
	sch := schema.Full{
		Config: docs.FieldSpecs{docs.FieldInput("input", "")},
		Inputs: []docs.ComponentSpec{
			mk("alpha", codecOpts),
			mk("beta", codecOpts),
			mk("gamma", []string{"foo", "bar"}), // unique, stays inline
		},
	}
	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	src := string(out)

	if !strings.Contains(src, `type Codec = "lines" | "all-bytes" | "tar"`) {
		t.Errorf("expected `type Codec = ...` alias\n---\n%s", src)
	}
	if c := strings.Count(src, ": Codec"); c != 2 {
		t.Errorf("expected two references to Codec alias, got %d\n---\n%s", c, src)
	}
	// The unique option set must still be emitted inline.
	if !strings.Contains(src, `: "foo" | "bar"`) {
		t.Errorf("expected inline union for unique option set\n---\n%s", src)
	}
}

func TestGenerateSchemaLiteralUnionFromOptions(t *testing.T) {
	level := docs.FieldString("level", "Log level.").HasOptions("DEBUG", "INFO", "WARN", "ERROR").HasDefault("INFO")
	input := docs.ComponentSpec{
		Name: "generate",
		Type: docs.TypeInput,
		Config: docs.FieldObject("", "").WithChildren(
			level,
		),
	}
	sch := schema.Full{
		Config: docs.FieldSpecs{docs.FieldInput("input", "")},
		Inputs: []docs.ComponentSpec{input},
	}
	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	src := string(out)
	want := `level?: "DEBUG" | "INFO" | "WARN" | "ERROR" = "INFO"`
	if !strings.Contains(src, want) {
		t.Errorf("expected %q in output\n---\n%s", want, src)
	}
}

func TestWriteFieldAttrFlagsAndDeprecatedDecorator(t *testing.T) {
	secret := docs.FieldString("token", "API token.").Secret()
	advanced := docs.FieldString("tuning", "Tuning knob.").Advanced().HasDefault("x")
	deprecated := docs.FieldString("legacy", "Use foo instead.").Deprecated().HasDefault("")

	input := docs.ComponentSpec{
		Name: "generate",
		Type: docs.TypeInput,
		Config: docs.FieldObject("", "").WithChildren(
			secret, advanced, deprecated,
			docs.FieldString("mapping", "A bloblang mapping."),
		),
	}
	sch := schema.Full{
		Config: docs.FieldSpecs{docs.FieldInput("input", "")},
		Inputs: []docs.ComponentSpec{input},
	}

	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	src := string(out)

	for _, want := range []string{
		"# [SECRET] API token.",
		"# [ADVANCED] Tuning knob.",
		"# [DEPRECATED] Use foo instead.",
		`@deprecated(reason="Use foo instead.", strict=False)`,
	} {
		if !strings.Contains(src, want) {
			t.Errorf("expected %q in output\n---\n%s", want, src)
		}
	}
}

func TestGenerateSchemaShape(t *testing.T) {
	// Construct a minimal schema.Full with one of each component category
	// so that we exercise union emission and nested-object promotion.
	credsChildren := docs.FieldSpecs{
		docs.FieldString("id", "The access id.").HasDefault(""),
		docs.FieldString("secret", "The access secret.").HasDefault(""),
	}
	mapping := docs.FieldString("mapping", "A bloblang mapping.")
	// A field called "check" to exercise keyword escaping.
	checkField := docs.FieldString("check", "An optional check expression.").Optional()
	credentials := docs.FieldObject("credentials", "Credentials object.").WithChildren(credsChildren...)

	input := docs.ComponentSpec{
		Name:    "generate",
		Type:    docs.TypeInput,
		Summary: "Generate messages.",
		Config: docs.FieldObject("", "").WithChildren(
			mapping,
			checkField,
			credentials,
		),
	}

	sch := schema.Full{
		Version: "1.2.3",
		Date:    "2024-01-02",
		Config: docs.FieldSpecs{
			docs.FieldInput("input", "The input."),
		},
		Inputs: []docs.ComponentSpec{input},
	}

	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	src := string(out)

	for _, want := range []string{
		"# Generated from bento 1.2.3, built 2024-01-02.",
		"schema Config:",
		"input?: Input",
		"schema GenerateInput:",
		"mapping: str",
		"$check?: str",
		"credentials?: Credentials",
		"schema Credentials:",
		"schema Input:",
		"mixin [LabelMixin, ProcessorsMixin]",
		"generate?: GenerateInput",
		"check:",
		`len([x for x in [generate] if x != Undefined]) == 1, "exactly one input variant must be set"`,
		"schema LabelMixin:",
		"schema ProcessorsMixin:",
		"protocol InputProtocol:",
		"protocol ProcessorProtocol:",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated schema missing %q\n---\n%s", want, src)
		}
	}
}

func TestGenerateSchemaDedupesStructuralDuplicates(t *testing.T) {
	// Two components share a `tls` block with the same structure. The
	// generator should emit a single `Tls` schema and reuse it.
	tlsChildren := docs.FieldSpecs{
		docs.FieldBool("enabled", "Whether TLS is enabled.").HasDefault(false),
		docs.FieldString("root_cas", "PEM root CAs.").HasDefault(""),
	}
	mkInput := func(name string) docs.ComponentSpec {
		return docs.ComponentSpec{
			Name: name,
			Type: docs.TypeInput,
			Config: docs.FieldObject("", "").WithChildren(
				docs.FieldString("url", "The URL."),
				docs.FieldObject("tls", "TLS configuration.").WithChildren(tlsChildren...),
			),
		}
	}

	sch := schema.Full{
		Config: docs.FieldSpecs{docs.FieldInput("input", "")},
		Inputs: []docs.ComponentSpec{mkInput("alpha"), mkInput("beta")},
	}

	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	src := string(out)

	// There must be exactly one `schema Tls:` block.
	if got := strings.Count(src, "schema Tls:"); got != 1 {
		t.Errorf("expected exactly one `schema Tls:` block, got %d\n---\n%s", got, src)
	}

	// Neither input should reference a parent-prefixed tls schema.
	for _, bad := range []string{"AlphaInputTls", "BetaInputTls"} {
		if strings.Contains(src, bad) {
			t.Errorf("unexpected duplicate schema %q in output\n---\n%s", bad, src)
		}
	}

	// Both component schemas should reference the shared `Tls`.
	for _, parent := range []string{"schema AlphaInput:", "schema BetaInput:"} {
		if !strings.Contains(src, parent) {
			t.Errorf("expected %q in output\n---\n%s", parent, src)
		}
	}
	if c := strings.Count(src, "tls?: Tls"); c != 2 {
		t.Errorf("expected two references to shared `Tls`, got %d", c)
	}
}


// TestVariantsNotLeakedInYAML shells out to the `kcl` CLI to verify that
// the check-block variant guard does not introduce a `_variants` attribute
// that leaks into the rendered YAML. Skipped when `kcl` is not installed.
func TestVariantsNotLeakedInYAML(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl CLI not installed; skipping runtime yaml check")
	}

	sch := schema.Full{
		Version: "test",
		Date:    "now",
		Config: docs.FieldSpecs{
			docs.FieldInput("input", "The input."),
			docs.FieldOutput("output", "The output."),
		},
		Inputs: []docs.ComponentSpec{{
			Name: "generate",
			Type: docs.TypeInput,
			Config: docs.FieldObject("", "").WithChildren(
				docs.FieldString("mapping", "A bloblang mapping."),
			),
		}},
		Outputs: []docs.ComponentSpec{{
			Name: "stdout",
			Type: docs.TypeOutput,
			Config: docs.FieldObject("", "").WithChildren(
				docs.FieldString("codec", "Output codec.").HasDefault("lines"),
			),
		}},
	}

	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}
	if strings.Contains(string(out), "_variants") {
		t.Fatalf("generated schema still contains `_variants`:\n%s", out)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bento.k"), out, 0o644); err != nil {
		t.Fatalf("write bento.k: %v", err)
	}
	usage := `import bento as b

cfg: b.Config = {
    input = b.Input { generate = b.GenerateInput { mapping = "root = {}" } }
    output = b.Output { stdout = b.StdoutOutput {} }
}
`
	if err := os.WriteFile(filepath.Join(dir, "use.k"), []byte(usage), 0o644); err != nil {
		t.Fatalf("write use.k: %v", err)
	}

	cmd := exec.Command("kcl", "run", "use.k", "bento.k")
	cmd.Dir = dir
	gotOut, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("kcl run failed: %v\n%s", err, gotOut)
	}
	if strings.Contains(string(gotOut), "_variants") {
		t.Errorf("_variants leaked into YAML output:\n%s", gotOut)
	}
	// Sanity: the happy variant must round-trip.
	if !strings.Contains(string(gotOut), "generate:") || !strings.Contains(string(gotOut), "stdout:") {
		t.Errorf("expected generate/stdout in yaml output:\n%s", gotOut)
	}
}

// TestVariantCheckRejectsMultipleVariants confirms that the inlined
// single-variant check still enforces the invariant it used to enforce
// when phrased in terms of `_variants`.
func TestVariantCheckRejectsMultipleVariants(t *testing.T) {
	if _, err := exec.LookPath("kcl"); err != nil {
		t.Skip("kcl CLI not installed; skipping runtime check")
	}

	sch := schema.Full{
		Version: "test",
		Date:    "now",
		Config: docs.FieldSpecs{docs.FieldInput("input", "The input.")},
		Inputs: []docs.ComponentSpec{
			{
				Name: "a",
				Type: docs.TypeInput,
				Config: docs.FieldObject("", "").WithChildren(
					docs.FieldString("x", "").HasDefault(""),
				),
			},
			{
				Name: "b",
				Type: docs.TypeInput,
				Config: docs.FieldObject("", "").WithChildren(
					docs.FieldString("y", "").HasDefault(""),
				),
			},
		},
	}

	out, err := GenerateSchema(sch)
	if err != nil {
		t.Fatalf("GenerateSchema: %v", err)
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "bento.k"), out, 0o644); err != nil {
		t.Fatalf("write bento.k: %v", err)
	}
	// Two variants set — must trigger the check.
	bad := `import bento as b

cfg: b.Input = { a = b.AInput {}, b = b.BInput {} }
`
	if err := os.WriteFile(filepath.Join(dir, "bad.k"), []byte(bad), 0o644); err != nil {
		t.Fatalf("write bad.k: %v", err)
	}

	cmd := exec.Command("kcl", "run", "bad.k", "bento.k")
	cmd.Dir = dir
	gotOut, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected kcl check failure, got success:\n%s", gotOut)
	}
	if !strings.Contains(string(gotOut), "exactly one input variant must be set") {
		t.Errorf("expected single-variant check error in output, got:\n%s", gotOut)
	}
}
