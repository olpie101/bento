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
		yamlName string
		// typeExpr is the full right-hand side of the union attribute
		// declaration, e.g. `MyProcessor`, `[Processor]`, or `str`.
		typeExpr string
		summary  string
	}
	entries := make([]componentEntry, 0, len(specs))

	for _, cs := range specs {
		entry := componentEntry{
			yamlName: cs.Name,
			summary:  cs.Summary,
		}

		// Classify the component root shape. Bento models several
		// list/scalar-rooted components (e.g. `try: [Processor]`,
		// `mapping: str`, `switch: [SwitchCase]`) as field specs with
		// a non-empty Kind at the root. These cannot be represented as
		// a KCL record schema, so we inline the type at the union site
		// and skip emitting a dedicated component schema.
		switch {
		case cs.Config.Type == docs.FieldTypeObject && cs.Config.Kind == docs.KindArray:
			// Array-of-object root (e.g. switch). Emit a dedicated
			// element schema from Children and reference it as a list
			// at the union site.
			elementName := w.uniqueName(toSchemaName(cs.Name) + kind.componentSuffix + "Case")
			w.pending = append(w.pending, pendingSchema{
				name:   elementName,
				doc:    cs.Summary,
				fields: cs.Config.Children,
			})
			entry.typeExpr = "[" + elementName + "]"

		case cs.Config.Type == docs.FieldTypeObject:
			// Object root (Kind == "" or KindScalar): emit a dedicated
			// record schema. An empty Children list renders as the
			// `_empty?: any` placeholder — intentional for YAML-empty
			// components like `drop: {}`, `noop: {}`, `sync_response: {}`.
			schemaName := w.uniqueName(toSchemaName(cs.Name) + kind.componentSuffix)
			w.pending = append(w.pending, pendingSchema{
				name:   schemaName,
				doc:    cs.Summary,
				fields: cs.Config.Children,
			})
			entry.typeExpr = schemaName

		default:
			// Non-object root: render the type inline via the generic
			// renderType path. Handles, for example:
			//   Type=string  Kind=""/Scalar -> str
			//   Type=string  Kind=Array     -> [str]
			//   Type=processor Kind=Array   -> [Processor]
			//   Type=output  Kind=""/Scalar -> Output
			//   Type=output  Kind=Array     -> [Output]
			expr, err := w.renderType(cs.Config, "")
			if err != nil {
				return fmt.Errorf("rendering root type for %s: %w", cs.Name, err)
			}
			entry.typeExpr = expr
		}

		entries = append(entries, entry)
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
		w.line(attr + "?: " + e.typeExpr)
		attrNames = append(attrNames, attr)
	}

	// Emit a `check:` block enforcing the single-variant constraint.
	// The variant list is inlined directly in the expression so no
	// helper attribute is added to the schema — KCL's `_` prefix hides
	// attributes from the rendered YAML in most contexts, but inlining
	// removes any chance of leakage.
	if len(attrNames) > 0 {
		w.blank()
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
			"len([x for x in [%s] if x != Undefined]) %s 1, %q",
			strings.Join(attrNames, ", "), op, msg,
		))
		w.indent--
	}
	return nil
}
