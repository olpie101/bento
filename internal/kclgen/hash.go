package kclgen

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/warpstreamlabs/bento/internal/docs"
)

// structuralHash produces a stable hash identifying the structural shape of
// a slice of FieldSpec entries. Two subtrees that render to the same KCL
// types (same field names, kinds, types, required-ness, defaults, and
// children) hash to the same value so the generator can emit one shared
// schema for them instead of repeating a near-identical schema per
// containing component.
//
// Documentation-only metadata such as Description, Examples, Summary and
// the advanced/secret/deprecated flags are intentionally excluded from the
// hash: they do not affect the emitted KCL type.
func structuralHash(specs docs.FieldSpecs) string {
	h := sha256.New()
	writeFieldSpecsHash(h, specs)
	return hex.EncodeToString(h.Sum(nil))
}

type hashWriter interface {
	Write(p []byte) (int, error)
}

func writeFieldSpecsHash(h hashWriter, specs docs.FieldSpecs) {
	_, _ = fmt.Fprintf(h, "fields(%d)\n", len(specs))
	for _, s := range specs {
		writeFieldSpecHash(h, s)
	}
}

func writeFieldSpecHash(h hashWriter, s docs.FieldSpec) {
	_, _ = fmt.Fprintf(h, "name=%q\n", s.Name)
	_, _ = fmt.Fprintf(h, "kind=%q\n", s.Kind)
	_, _ = fmt.Fprintf(h, "type=%q\n", s.Type)
	_, _ = fmt.Fprintf(h, "req=%t\n", s.CheckRequired())

	// Options participate in the structural shape because they tighten
	// the emitted type (string -> literal union), so subtrees that
	// differ only in their enum values must hash differently.
	if len(s.AnnotatedOptions) > 0 {
		for _, opt := range s.AnnotatedOptions {
			_, _ = fmt.Fprintf(h, "aopt=%q\n", opt[0])
		}
	} else {
		for _, opt := range s.Options {
			_, _ = fmt.Fprintf(h, "opt=%q\n", opt)
		}
	}

	if s.Default != nil {
		_, _ = fmt.Fprintf(h, "default=%s\n", renderDefaultHash(*s.Default))
	} else {
		_, _ = fmt.Fprintln(h, "default=<nil>")
	}

	writeFieldSpecsHash(h, s.Children)
}

// renderDefaultHash produces a deterministic string form of an arbitrary
// default value. It is similar in spirit to renderDefault but also handles
// unrepresentable defaults so the hash stays stable.
func renderDefaultHash(v any) string {
	switch t := v.(type) {
	case nil:
		return "<nil>"
	case bool:
		return strconv.FormatBool(t)
	case string:
		return strconv.Quote(t)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case float64:
		return strconv.FormatFloat(t, 'g', -1, 64)
	default:
		// Fall back to Go's default verb for types we do not inspect.
		return fmt.Sprintf("%#v", v)
	}
}
