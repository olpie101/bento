package kclgen

import (
	"strings"
	"unicode"

	"github.com/warpstreamlabs/bento/internal/docs"
)

// kclKeywords is the set of reserved identifiers in KCL. When a Bento field
// name matches one of these it must be prefixed with `$` in the emitted
// schema, e.g. `check` -> `$check`.
var kclKeywords = map[string]struct{}{
	"True": {}, "False": {}, "None": {}, "Undefined": {},
	"import": {}, "as": {},
	"schema": {}, "mixin": {}, "protocol": {}, "rule": {},
	"check": {}, "relaxed": {}, "final": {},
	"type": {}, "lambda": {}, "assert": {},
	"if": {}, "elif": {}, "else": {},
	"for": {}, "in": {},
	"and": {}, "or": {}, "not": {}, "is": {},
	"all": {}, "any": {}, "filter": {}, "map": {}, "pass": {},
}

// isComponentRef reports whether t refers to a Bento component category.
func isComponentRef(t docs.FieldType) bool {
	switch t {
	case docs.FieldTypeInput, docs.FieldTypeOutput, docs.FieldTypeProcessor,
		docs.FieldTypeCache, docs.FieldTypeRateLimit, docs.FieldTypeBuffer,
		docs.FieldTypeMetrics, docs.FieldTypeTracer, docs.FieldTypeScanner:
		return true
	}
	return false
}

// escapeFieldName returns a KCL-safe schema attribute name for the given
// Bento field name. Reserved keywords are prefixed with `$`.
func escapeFieldName(name string) string {
	if _, ok := kclKeywords[name]; ok {
		return "$" + name
	}
	return name
}

// toSchemaName converts a component name such as `http_client` into a
// PascalCase identifier like `HttpClient` suitable for use as a KCL schema
// name. Non-identifier runes are dropped.
func toSchemaName(name string) string {
	var b strings.Builder
	upper := true
	for _, r := range name {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ':
			upper = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if upper {
				b.WriteRune(unicode.ToUpper(r))
				upper = false
			} else {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}

// Top-level KCL schema / type names. These mirror the role of the `#Config`
// and `#Input`/`#Output`/... definitions in internal/cuegen.
const (
	nameConfigSchema = "Config"

	nameInputSchema     = "Input"
	nameOutputSchema    = "Output"
	nameProcessorSchema = "Processor"
	nameCacheSchema     = "Cache"
	nameRateLimitSchema = "RateLimit"
	nameBufferSchema    = "Buffer"
	nameMetricSchema    = "Metric"
	nameTracerSchema    = "Tracer"
	nameScannerSchema   = "Scanner"

	// Shared schemas used as mixin hosts by the category union schemas.
	// KCL mixins are just schemas whose attributes are composed into the
	// host via the `mixin [...]` declaration; conventionally their names
	// end with `Mixin`.
	nameLabelMixin      = "LabelMixin"
	nameProcessorsMixin = "ProcessorsMixin"

	// Category protocols mirror the shape of each union schema. They are
	// emitted so that user-written helpers can annotate parameters with a
	// category contract (`theInput: InputProtocol`). See the note in
	// website/docs/configuration/using_kcl.md about their limited use: KCL
	// protocols cannot be inherited by non-mixin schemas, so the category
	// schemas do not formally implement them.
	//
	// KCL requires protocol names to end with `Protocol`.
	nameInputProtocol     = "InputProtocol"
	nameOutputProtocol    = "OutputProtocol"
	nameProcessorProtocol = "ProcessorProtocol"
	nameCacheProtocol     = "CacheProtocol"
	nameRateLimitProtocol = "RateLimitProtocol"
	nameBufferProtocol    = "BufferProtocol"
	nameMetricProtocol    = "MetricProtocol"
	nameTracerProtocol    = "TracerProtocol"
	nameScannerProtocol   = "ScannerProtocol"
)
