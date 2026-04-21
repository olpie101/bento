package kclgen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/warpstreamlabs/bento/internal/docs"
)

// writer is a small indentation-aware buffer for emitting KCL source.
// Pending is a queue of deferred nested schemas that were encountered while
// emitting a parent schema body; they are flushed once the parent finishes.
type writer struct {
	b            strings.Builder
	indent       int
	pending      []pendingSchema
	used         map[string]struct{}
	// dedupedNames maps a structural hash of an object subtree to the
	// schema name used to represent it. Two FieldSpec trees that hash to
	// the same value share a single emitted schema, so common blocks like
	// TLS/auth/batching are no longer duplicated dozens of times.
	dedupedNames map[string]string
	// typeAliases maps an option-set key (see aliasKey) to a named KCL
	// `type` alias. When a literal-union field matches an entry here the
	// emitted type is the alias name rather than the inline union.
	typeAliases map[string]*typeAlias
}

// pendingSchema represents a named schema that still needs to be written,
// typically generated on the fly when a parent field references a nested
// object structure.
type pendingSchema struct {
	name    string
	doc     string
	fields  docs.FieldSpecs
	wrapper []string // extra synthetic attributes (e.g. label/processors)
}

func newWriter() *writer {
	return &writer{
		used:         map[string]struct{}{},
		dedupedNames: map[string]string{},
		typeAliases:  map[string]*typeAlias{},
	}
}

// uniqueName returns name, suffixed with a counter if it collides with an
// already-reserved schema name.
func (w *writer) uniqueName(name string) string {
	if _, ok := w.used[name]; !ok {
		w.used[name] = struct{}{}
		return name
	}
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s%d", name, i)
		if _, ok := w.used[candidate]; !ok {
			w.used[candidate] = struct{}{}
			return candidate
		}
	}
}

// pickNestedName chooses a schema name for a nested object field. It prefers
// the bare PascalCase form of the field name (e.g. `tls` -> `Tls`) so that
// common blocks like `tls`, `auth`, `batching` get short, reusable names.
// When that bare form is already reserved, it falls back to a parent-
// prefixed variant and finally to a counter-suffixed form via uniqueName.
func (w *writer) pickNestedName(fieldName, parentSchema string) string {
	preferred := toSchemaName(fieldName)
	if preferred != "" {
		if _, ok := w.used[preferred]; !ok {
			w.used[preferred] = struct{}{}
			return preferred
		}
	}
	return w.uniqueName(parentSchema + toSchemaName(fieldName))
}

func (w *writer) line(s string) {
	for range w.indent {
		w.b.WriteString("    ")
	}
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

func (w *writer) blank() { w.b.WriteByte('\n') }

// writeEmptyBody emits a placeholder attribute that KCL accepts as a body
// for schemas that have no real attributes. Underscore-prefixed attributes
// are excluded from the rendered YAML, so Bento never sees this.
func (w *writer) writeEmptyBody() {
	w.line("_empty?: any = Undefined")
}

func (w *writer) String() string { return w.b.String() }

// writeDoc emits description text as a KCL triple-quoted docstring at the
// current indent, skipping empty descriptions.
func (w *writer) writeDoc(desc string) {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return
	}
	// Split into lines; KCL docstrings preserve content verbatim.
	lines := strings.Split(desc, "\n")
	if len(lines) == 1 {
		w.line(`r"""` + escapeDocstring(lines[0]) + `"""`)
		return
	}
	w.line(`r"""`)
	for _, ln := range lines {
		w.line(escapeDocstring(ln))
	}
	w.line(`"""`)
}

// escapeDocstring strips any embedded `"""` sequences that would terminate a
// KCL triple-quoted string.
func escapeDocstring(s string) string {
	return strings.ReplaceAll(s, `"""`, `\"\"\"`)
}

// writeLineComment emits a single-line `#` comment at the current indent,
// used for shorter field descriptions above attribute declarations.
func (w *writer) writeLineComment(desc string) {
	desc = strings.TrimSpace(desc)
	if desc == "" {
		return
	}
	for ln := range strings.SplitSeq(desc, "\n") {
		w.line("# " + ln)
	}
}

// renderLiteralUnion returns a KCL type expression for an enumerated
// string field, preferring a named type alias when one has been
// collected for this option set. When no alias applies and the field
// has options, the union is inlined at the use-site. Returns an empty
// string when the field is not an enumerated string.
func (w *writer) renderLiteralUnion(spec docs.FieldSpec) string {
	if spec.Type != docs.FieldTypeString {
		return ""
	}
	values := collectOptionValues(spec)
	if len(values) == 0 {
		return ""
	}
	if w != nil {
		if alias, ok := w.typeAliases[aliasKey(values)]; ok {
			return alias.name
		}
	}
	parts := make([]string, len(values))
	for i, v := range values {
		parts[i] = strconv.Quote(v)
	}
	return strings.Join(parts, " | ")
}

// collectOptionValues returns the ordered list of allowed string values
// for a field, preferring AnnotatedOptions when present.
func collectOptionValues(spec docs.FieldSpec) []string {
	if len(spec.AnnotatedOptions) > 0 {
		out := make([]string, 0, len(spec.AnnotatedOptions))
		for _, opt := range spec.AnnotatedOptions {
			out = append(out, opt[0])
		}
		return out
	}
	if len(spec.Options) > 0 {
		return append([]string(nil), spec.Options...)
	}
	return nil
}

// renderType returns the KCL type expression corresponding to a FieldSpec,
// independent of whether the field is optional. When the field is a
// structured object with children, a nested schema is queued on the writer
// and its generated name is returned as the type reference.
func (w *writer) renderType(spec docs.FieldSpec, parentSchema string) (string, error) {
	switch spec.Kind {
	case "", docs.KindScalar:
		return w.renderScalarType(spec, parentSchema)
	case docs.KindArray:
		inner, err := w.renderScalarType(spec, parentSchema)
		if err != nil {
			return "", err
		}
		return "[" + inner + "]", nil
	case docs.Kind2DArray:
		inner, err := w.renderScalarType(spec, parentSchema)
		if err != nil {
			return "", err
		}
		return "[[" + inner + "]]", nil
	case docs.KindMap:
		inner, err := w.renderScalarType(spec, parentSchema)
		if err != nil {
			return "", err
		}
		return "{str:" + inner + "}", nil
	default:
		return "", fmt.Errorf("unrecognised field kind: %s", spec.Kind)
	}
}

// renderScalarType maps a FieldSpec's Type to the underlying KCL type
// expression. Structured objects generate a nested schema; component
// references resolve to the matching union schema (e.g. `Input`). When
// the spec carries an `Options` list the type tightens to a KCL literal
// union (e.g. `"a" | "b" | "c"`) so the emitted schema validates the
// allowed values rather than accepting any string.
func (w *writer) renderScalarType(spec docs.FieldSpec, parentSchema string) (string, error) {
	if lit := w.renderLiteralUnion(spec); lit != "" {
		return lit, nil
	}
	switch spec.Type {
	case docs.FieldTypeString:
		return "str", nil
	case docs.FieldTypeInt:
		return "int", nil
	case docs.FieldTypeFloat:
		return "float", nil
	case docs.FieldTypeBool:
		return "bool", nil
	case docs.FieldTypeUnknown:
		return "any", nil
	case docs.FieldTypeObject:
		if len(spec.Children) == 0 {
			return "{str:any}", nil
		}
		// Dedupe structurally identical object subtrees. Two fields
		// with the same child layout resolve to one shared schema.
		hash := structuralHash(spec.Children)
		if existing, ok := w.dedupedNames[hash]; ok {
			return existing, nil
		}
		nested := w.pickNestedName(spec.Name, parentSchema)
		w.dedupedNames[hash] = nested
		w.pending = append(w.pending, pendingSchema{
			name:   nested,
			doc:    spec.Description,
			fields: spec.Children,
		})
		return nested, nil
	case docs.FieldTypeInput:
		return nameInputSchema, nil
	case docs.FieldTypeBuffer:
		return nameBufferSchema, nil
	case docs.FieldTypeCache:
		return nameCacheSchema, nil
	case docs.FieldTypeProcessor:
		return nameProcessorSchema, nil
	case docs.FieldTypeRateLimit:
		return nameRateLimitSchema, nil
	case docs.FieldTypeOutput:
		return nameOutputSchema, nil
	case docs.FieldTypeMetrics:
		return nameMetricSchema, nil
	case docs.FieldTypeTracer:
		return nameTracerSchema, nil
	case docs.FieldTypeScanner:
		return nameScannerSchema, nil
	default:
		return "", fmt.Errorf("unrecognised field type: %s", spec.Type)
	}
}

// renderDefault returns a KCL literal expression for a Bento default value,
// or an empty string if the value cannot be represented as a KCL literal.
func renderDefault(v any) string {
	switch t := v.(type) {
	case nil:
		return "None"
	case bool:
		if t {
			return "True"
		}
		return "False"
	case string:
		// KCL treats `${...}` as string interpolation, which clashes with
		// Bento's bloblang interpolation syntax in default strings. Emit
		// as a raw string literal to suppress interpolation when the
		// value contains `$`.
		if strings.Contains(t, "$") {
			return "r" + strconv.Quote(t)
		}
		return strconv.Quote(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		// Render integer-valued floats without a decimal component so
		// they round-trip into int-typed fields. Bento occasionally
		// carries large defaults as float64 (e.g. 1e9) even for int
		// fields, and KCL's type checker rejects float defaults on int
		// attributes.
		if t == float64(int64(t)) {
			return strconv.FormatInt(int64(t), 10)
		}
		return strconv.FormatFloat(t, 'g', -1, 64)
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			r := renderDefault(e)
			if r == "" {
				return ""
			}
			parts = append(parts, r)
		}
		return "[" + strings.Join(parts, ", ") + "]"
	case map[string]any:
		if len(t) == 0 {
			return "{}"
		}
		// Give up on arbitrary map defaults rather than risk producing
		// invalid KCL; the field will simply be optional without a default.
		return ""
	default:
		return ""
	}
}

// writeFieldSpecs emits a list of FieldSpec entries as schema attributes at
// the current indent. The parentSchema name is used when synthesising names
// for nested object schemas. When specs is empty a placeholder attribute
// is written so KCL sees a valid schema body.
func (w *writer) writeFieldSpecs(parentSchema string, specs docs.FieldSpecs) error {
	if len(specs) == 0 {
		w.writeEmptyBody()
		return nil
	}
	for _, spec := range specs {
		if err := w.writeFieldAttr(parentSchema, spec); err != nil {
			return err
		}
	}
	return nil
}

func (w *writer) writeFieldAttr(parentSchema string, spec docs.FieldSpec) error {
	typ, err := w.renderType(spec, parentSchema)
	if err != nil {
		return fmt.Errorf("field %q: %w", spec.Name, err)
	}

	required := spec.CheckRequired()
	// Component-reference fields are always treated as optional in the
	// emitted schema. This mirrors the CUE generator's handling of
	// FieldTypeInput/Output/etc. to avoid cycles and matches real-world
	// usage where users populate these via separate assignments.
	if isComponentRef(spec.Type) {
		required = false
	}
	name := escapeFieldName(spec.Name)

	attr := name
	if !required {
		attr += "?"
	}
	attr += ": " + typ

	if spec.Default != nil {
		if lit := renderDefault(*spec.Default); lit != "" && defaultCompatibleWithOptions(spec, *spec.Default) {
			attr += " = " + lit
		}
	}

	// Documentation: prefix the field comment with status tags derived
	// from the FieldSpec flags. KCL surfaces line comments in editor
	// hover output, so this is the lightest way to carry the metadata
	// through to authors.
	w.writeLineComment(describeField(spec))

	// KCL's built-in @deprecated decorator raises a warning (strict=False)
	// or error (strict=True) when a deprecated field is assigned. We use
	// strict=False so existing configs that still reference a deprecated
	// field render but produce a visible warning.
	if spec.IsDeprecated {
		reason := strings.TrimSpace(spec.Description)
		if reason == "" {
			reason = "this field is deprecated"
		}
		w.line(fmt.Sprintf(
			"@deprecated(reason=%s, strict=False)",
			strconv.Quote(firstLine(reason)),
		))
	}

	w.line(attr)
	return nil
}

// describeField composes the line-comment body for a field, prefixing
// status tags (`[DEPRECATED]`, `[SECRET]`, `[ADVANCED]`) onto the
// description so authors see the flags in editor hover text.
func describeField(spec docs.FieldSpec) string {
	var tags []string
	if spec.IsDeprecated {
		tags = append(tags, "[DEPRECATED]")
	}
	if spec.IsSecret {
		tags = append(tags, "[SECRET]")
	}
	if spec.IsAdvanced {
		tags = append(tags, "[ADVANCED]")
	}
	desc := strings.TrimSpace(spec.Description)
	if len(tags) == 0 {
		return desc
	}
	prefix := strings.Join(tags, " ")
	if desc == "" {
		return prefix
	}
	return prefix + " " + desc
}

// defaultCompatibleWithOptions reports whether the default value is safe to
// emit alongside a literal-union type. When a string field carries an
// enumerated set of options and the default isn't one of them, emitting the
// default produces invalid KCL (`str(X) got str(Y)`). In that case we drop
// the default and let the field remain optional.
func defaultCompatibleWithOptions(spec docs.FieldSpec, def any) bool {
	opts := collectOptionValues(spec)
	if len(opts) == 0 {
		return true
	}
	s, ok := def.(string)
	if !ok {
		return true
	}
	for _, o := range opts {
		if o == s {
			return true
		}
	}
	return false
}

// firstLine returns s up to the first newline, trimmed. Used to keep
// decorator arguments on a single line.
func firstLine(s string) string {
	if idx := strings.IndexByte(s, '\n'); idx >= 0 {
		return strings.TrimSpace(s[:idx])
	}
	return strings.TrimSpace(s)
}

// flushPending writes any schemas queued while rendering parent attributes.
// New pending schemas produced by those schemas are drained in order.
func (w *writer) flushPending() error {
	for len(w.pending) > 0 {
		next := w.pending[0]
		w.pending = w.pending[1:]
		if err := w.writeNamedSchema(next); err != nil {
			return err
		}
	}
	return nil
}

// writeNamedSchema emits a top-level `schema Name:` block for ps.
func (w *writer) writeNamedSchema(ps pendingSchema) error {
	w.blank()
	w.line("schema " + ps.name + ":")
	w.indent++
	defer func() { w.indent-- }()

	if strings.TrimSpace(ps.doc) != "" {
		w.writeDoc(ps.doc)
	}
	for _, extra := range ps.wrapper {
		w.line(extra)
	}
	if len(ps.fields) == 0 && len(ps.wrapper) == 0 {
		w.writeEmptyBody()
		return nil
	}
	if len(ps.fields) == 0 {
		return nil
	}
	return w.writeFieldSpecs(ps.name, ps.fields)
}
