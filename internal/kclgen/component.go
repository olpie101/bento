package kclgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/warpstreamlabs/bento/internal/docs"
)

// componentKind captures the top-level schema metadata for a family of
// Bento components (inputs, outputs, processors, ...).
type componentKind struct {
	// unionSchema is the name of the outer schema that acts as a
	// discriminated union over components, e.g. `Input`.
	unionSchema string
	// componentSuffix is appended to each component's PascalCase name
	// when generating its dedicated schema, e.g. `Input` -> `GenerateInput`.
	componentSuffix string
	// canLabel indicates the union schema should accept an optional
	// `label` attribute (mirrors cuegen).
	canLabel bool
	// canPreProcess indicates the union schema should accept an optional
	// `processors` list (mirrors cuegen).
	canPreProcess bool
	// requiresExactlyOne controls the union's variant-count `check:`
	// constraint. When true, the schema enforces that exactly one variant
	// is set (`len(...) == 1`); otherwise the check permits zero or one
	// (`len(...) <= 1`) which is appropriate for optional slots like
	// buffers, metrics, and tracers.
	requiresExactlyOne bool
	// noun is the lowercase singular label used in `check:` error
	// messages, e.g. "input", "buffer".
	noun string
}

// writeComponents emits, for each component spec:
//
//   - a dedicated schema `<PascalName><componentSuffix>` containing its
//     configuration fields;
//
// and afterwards emits the union schema `<unionSchema>` containing one
// optional attribute per component (keyed by the component's YAML name)
// plus any `label`/`processors` attributes requested by the kind.
//
// Component specs are sorted alphabetically by Name before emission so the
// generated output — including which component gets the first chance at a
// bare shared name during dedupe — is stable across Bento releases even if
// component registration order changes.
//
// KCL does not have a first-class "exactly one of these fields" constraint
// compatible with free-form config authoring, so callers should treat the
// union schema as a structural contract rather than a strict discriminator.
// See website/docs/configuration/using_kcl.md for the author-side caveats.
func (w *writer) writeComponents(specs []docs.ComponentSpec, kind componentKind) error {
	// Copy+sort so we don't mutate the caller's slice.
	sorted := make([]docs.ComponentSpec, len(specs))
	copy(sorted, specs)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Name < sorted[j].Name
	})
	specs = sorted

	// Stable ordering comes from the caller; we preserve it.
	type componentEntry struct {
		yamlName   string
		schemaName string
		summary    string
	}
	entries := make([]componentEntry, 0, len(specs))

	for _, cs := range specs {
		schemaName := w.uniqueName(toSchemaName(cs.Name) + kind.componentSuffix)

		fields := cs.Config.Children
		// If the config isn't an object (e.g. a scalar-only component),
		// surface the single field as the sole attribute.
		if cs.Config.Type != docs.FieldTypeObject && cs.Config.Kind == "" && len(cs.Config.Children) == 0 {
			fields = docs.FieldSpecs{cs.Config}
		}

		w.pending = append(w.pending, pendingSchema{
			name:   schemaName,
			doc:    cs.Summary,
			fields: fields,
		})

		entries = append(entries, componentEntry{
			yamlName:   cs.Name,
			schemaName: schemaName,
			summary:    cs.Summary,
		})
	}

	// Drain component schemas first so the union schema appears after all
	// of its referenced component schemas have been written.
	if err := w.flushPending(); err != nil {
		return err
	}

	// Emit the outer union schema.
	w.blank()
	w.line(fmt.Sprintf("schema %s:", kind.unionSchema))
	w.indent++
	defer func() { w.indent-- }()

	// Compose label/processors via shared mixins so the attributes are not
	// re-declared on every category schema. The mixin schemas themselves
	// are emitted once at the top of the module by GenerateSchema.
	mixins := []string{}
	if kind.canLabel {
		mixins = append(mixins, nameLabelMixin)
	}
	if kind.canPreProcess {
		mixins = append(mixins, nameProcessorsMixin)
	}
	if len(mixins) > 0 {
		w.line("mixin [" + strings.Join(mixins, ", ") + "]")
	}

	if len(entries) == 0 && len(mixins) == 0 {
		w.writeEmptyBody()
		return nil
	}

	attrNames := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.summary != "" {
			w.writeLineComment(e.summary)
		}
		attr := escapeFieldName(e.yamlName)
		w.line(attr + "?: " + e.schemaName)
		attrNames = append(attrNames, attr)
	}

	// Emit a `_variants` helper list and a `check:` block enforcing the
	// single-variant constraint. Attributes prefixed with `_` are excluded
	// from the rendered YAML by KCL, so this is invisible to Bento.
	if len(attrNames) > 0 {
		w.blank()
		w.line("_variants?: [any] = [" + strings.Join(attrNames, ", ") + "]")
		w.line("check:")
		w.indent++
		op := "<="
		if kind.requiresExactlyOne {
			op = "=="
		}
		msg := fmt.Sprintf("at most one %s variant may be set", kind.noun)
		if kind.requiresExactlyOne {
			msg = fmt.Sprintf("exactly one %s variant must be set", kind.noun)
		}
		w.line(fmt.Sprintf(
			"len([x for x in _variants if x != Undefined]) %s 1, %q",
			op, msg,
		))
		w.indent--
	}
	return nil
}
