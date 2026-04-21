package kclgen

import (
	"fmt"
	"sort"
	"strings"

	"github.com/warpstreamlabs/bento/internal/config/schema"
	"github.com/warpstreamlabs/bento/internal/docs"
)

// typeAliasMinUses is the threshold at which a literal-union option set
// becomes worth promoting to a named type alias. Option sets below this
// are inlined at each use-site.
const typeAliasMinUses = 2

// typeAlias captures a named KCL `type` alias for a frequently-used
// literal-union option set.
type typeAlias struct {
	name    string   // emitted alias name, e.g. "Codec"
	values  []string // ordered set of allowed values (matches FieldSpec order)
	uses    int      // how many field occurrences reference this set
	primary string   // the most common field name that produced this alias
}

// aliasKey returns a stable hash key for an ordered set of option values.
// Order matters because KCL literal-union types are ordered.
func aliasKey(values []string) string {
	var b strings.Builder
	for _, v := range values {
		b.WriteByte('|')
		b.WriteString(v)
	}
	return b.String()
}

// collectTypeAliases walks every FieldSpec reachable from sch, counts
// how often each literal-union option set appears, and returns a map
// from alias key -> alias info for every set that reaches
// typeAliasMinUses uses. Names are derived from the most common field
// name that introduced the alias; collisions with existing reserved
// schema names or other aliases are resolved by suffixing a counter.
func collectTypeAliases(sch schema.Full, reserved map[string]struct{}) map[string]*typeAlias {
	type bucket struct {
		values    []string
		uses      int
		nameCount map[string]int
	}
	buckets := map[string]*bucket{}

	recordField := func(spec docs.FieldSpec) {
		if spec.Type != docs.FieldTypeString {
			return
		}
		values := collectOptionValues(spec)
		if len(values) < 2 {
			return
		}
		key := aliasKey(values)
		b, ok := buckets[key]
		if !ok {
			b = &bucket{values: values, nameCount: map[string]int{}}
			buckets[key] = b
		}
		b.uses++
		b.nameCount[spec.Name]++
	}

	var walk func(docs.FieldSpec)
	walk = func(spec docs.FieldSpec) {
		recordField(spec)
		for _, c := range spec.Children {
			walk(c)
		}
	}
	walkSpecs := func(specs docs.FieldSpecs) {
		for _, s := range specs {
			walk(s)
		}
	}
	walkComponents := func(cs []docs.ComponentSpec) {
		for _, c := range cs {
			walkSpecs(c.Config.Children)
			if len(c.Config.Children) == 0 && c.Config.Name != "" {
				walk(c.Config)
			}
		}
	}

	walkSpecs(sch.Config)
	walkComponents(sch.Inputs)
	walkComponents(sch.Outputs)
	walkComponents(sch.Processors)
	walkComponents(sch.Caches)
	walkComponents(sch.RateLimits)
	walkComponents(sch.Buffers)
	walkComponents(sch.Metrics)
	walkComponents(sch.Tracers)
	walkComponents(sch.Scanners)

	// Assign alias names. Process in sorted key order so the mapping is
	// deterministic across runs.
	keys := make([]string, 0, len(buckets))
	for k, b := range buckets {
		if b.uses >= typeAliasMinUses {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	used := map[string]struct{}{}
	for k := range reserved {
		used[k] = struct{}{}
	}

	aliases := map[string]*typeAlias{}
	fallbackCounter := 0
	for _, k := range keys {
		b := buckets[k]

		// Pick the most common field name as the naming source.
		primary := ""
		best := 0
		for name, count := range b.nameCount {
			if count > best || (count == best && name < primary) {
				primary = name
				best = count
			}
		}

		base := toSchemaName(primary)
		if base == "" {
			fallbackCounter++
			base = fmt.Sprintf("Enum%d", fallbackCounter)
		}
		name := base
		for i := 2; ; i++ {
			if _, taken := used[name]; !taken {
				break
			}
			name = fmt.Sprintf("%s%d", base, i)
		}
		used[name] = struct{}{}

		aliases[k] = &typeAlias{
			name:    name,
			values:  b.values,
			uses:    b.uses,
			primary: primary,
		}
	}
	return aliases
}

// writeTypeAliases emits `type Name = "a" | "b" | ...` declarations for
// each collected alias, sorted by name for readability.
func writeTypeAliases(w *writer, aliases map[string]*typeAlias) {
	if len(aliases) == 0 {
		return
	}
	names := make([]string, 0, len(aliases))
	byName := make(map[string]*typeAlias, len(aliases))
	for _, a := range aliases {
		names = append(names, a.name)
		byName[a.name] = a
	}
	sort.Strings(names)
	w.blank()
	w.line("# Shared enumerated string types reused across component configs.")
	for _, n := range names {
		a := byName[n]
		parts := make([]string, len(a.values))
		for i, v := range a.values {
			parts[i] = fmt.Sprintf("%q", v)
		}
		w.line(fmt.Sprintf("type %s = %s", n, strings.Join(parts, " | ")))
	}
}
