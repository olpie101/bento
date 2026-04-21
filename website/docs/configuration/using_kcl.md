---
title: Using KCL
---

:::warning EXPERIMENTAL
KCL support is experimental. It may change between Bento releases as the generator evolves, which can introduce new validation errors when upgrading.
:::

[**KCL**](https://www.kcl-lang.io/) is a constraint-based configuration language that makes it easier and safer to author Bento configurations. In this guide we will build a Bento configuration in KCL, render it to YAML and execute it.

## Prerequisites

Before you get started, install the KCL CLI by [following this guide](https://www.kcl-lang.io/docs/user_docs/getting-started/install). If this is your first time using KCL, the [quick start](https://www.kcl-lang.io/docs/user_docs/getting-started/kcl-quick-start) is a good primer.

## Create

Create a directory for your project and initialise a KCL module:

```shell
mkdir hello-kcl
cd hello-kcl
kcl mod init hello-kcl
```

The `bento list` command will generate a KCL module containing the schemas we need. Write this module into our project:

```shell
mkdir bento
bento list --format kcl > bento/schema.k
```

At this point your directory structure should look like:

```
hello-kcl/
    bento/
        schema.k
    kcl.mod
    kcl.mod.lock
```

Now author a `config.k` file that uses the generated schemas:

```python
import bento

bento.Config {
    input = bento.Input {
        generate = bento.GenerateInput {
            mapping = """
                root = {"message": "Hello, KCL!"}
            """
        }
    }
    pipeline = {
        processors = [
            bento.Processor {
                mapping = bento.MappingProcessor {
                    mapping = """
                        root = this
                        root.id = uuid_v4()
                    """
                }
            },
        ]
    }
    output = bento.Output {
        stdout = bento.StdoutOutput {}
    }
}
```

Render it to YAML:

```shell
kcl run config.k -o yaml
```

You should see output similar to:

```yaml
input:
  generate:
    mapping: |
      root = {"message": "Hello, KCL!"}
pipeline:
  processors:
    - mapping:
        mapping: |
          root = this
          root.id = uuid_v4()
output:
  stdout: {}
```

Run it via Bento using process substitution:

```shell
bento -c <(kcl run config.k -o yaml)
```

## How the schemas are shaped

The generator emits several kinds of top-level declarations:

- **Mixins** — `LabelMixin` and `ProcessorsMixin` provide the optional `label` and `processors` attributes that Bento allows on most components. Category schemas compose them via `mixin [...]` rather than redeclaring the attributes on every schema.
- **Per-component schemas** — one schema per component (e.g. `GenerateInput`, `HttpClientInput`) describing that component's configuration fields.
- **Category schemas** — `Input`, `Output`, `Processor`, `Cache`, `RateLimit`, `Buffer`, `Metric`, `Tracer`, `Scanner`. Each exposes every variant as an optional attribute keyed by its Bento name, along with the composed mixins.
- **Protocols** — `InputProtocol`, `OutputProtocol`, and friends are structural contracts that user-written helpers can use as type annotations (e.g. `lambda wrap = (o: OutputProtocol) -> OutputProtocol: ...`). They are not implemented by the category schemas directly because KCL does not allow non-mixin schemas to inherit from protocols; the contract is therefore purely structural. For most use cases the category schemas themselves are a better choice — the protocols exist mainly to document the minimum surface a helper can rely on.

### Shared sub-schemas

Bento's configuration reuses the same sub-structure — `tls`, `auth`, `batching`, `retries`, and similar — across many components. The generator hashes each object subtree and emits a single shared schema for every structurally-identical occurrence, so you will see one `Tls` schema referenced from dozens of components instead of dozens of slightly-different `<Component>Tls` duplicates. This keeps the generated module roughly 40% smaller and makes it far easier to browse.

### Escaped keywords

KCL reserves several identifiers (`check`, `type`, `map`, `filter`, `assert`, ...) that also appear as Bento field names. The generator prefixes these with `$` so they remain usable. For example, the `switch` output's `check` field becomes `$check`:

```python
bento.SwitchOutput {
    cases = [
        bento.SwitchOutputCasesItem {
            $check = "errored()"
            output = bento.Output {
                reject = "failed: ${! error() }"
            }
        },
    ]
}
```

Rendered YAML preserves the original field name (`check:`).

### Limitations: single-variant unions

Bento's core component fields (inputs, outputs, ...) are discriminated unions at the YAML level: a config should specify **exactly one** variant per slot. The generator enforces this with a `check:` block on each category schema, using a hidden `_variants` helper to collect the component attributes:

```kcl
# Excerpt from the generated schema.
schema Input:
    mixin [LabelMixin, ProcessorsMixin]
    generate?: GenerateInput
    http_client?: HttpClientInput
    # ...

    _variants?: [any] = [generate, http_client, ...]
    check:
        len([x for x in _variants if x != Undefined]) == 1, "exactly one input variant must be set"
```

Assigning two sibling variants on the same `Input` now fails at `kcl run` time rather than silently using the last assignment. Buffer, metrics, and tracer slots are optional, so their checks allow zero or one (`<= 1`).

One case that still cannot be caught at schema level: assigning to the **same** attribute twice inside one schema body. KCL merges attribute assignments with last-write-wins, and the check only sees the merged result. This is rare in practice; when in doubt, lint the rendered YAML:

```shell
kcl run config.k -o yaml | bento lint
```

## Composing reusable helpers

Because KCL is a real language you can factor repeated patterns into schemas or functions. A typical pattern is to wrap an output with error handling and retry logic:

```python
# bento/helpers.k
import bento

schema Guarded:
    output: bento.Output
    errorMessage: str
    maxRetries: int = 3
    retryErrorMessage: str
    errorHandling: "drop" | "reject"

guarded = lambda g: Guarded -> bento.Output {
    bento.Output {
        $switch = bento.SwitchOutput {
            cases = [
                bento.SwitchOutputCasesItem {
                    $check = "errored()"
                    output = bento.Output {
                        reject = g.errorMessage if g.errorHandling == "reject" else Undefined
                        drop = bento.DropOutput {} if g.errorHandling == "drop" else Undefined
                    }
                },
                bento.SwitchOutputCasesItem {
                    output = bento.Output {
                        fallback = [
                            bento.Output {
                                retry = bento.RetryOutput {
                                    max_retries = g.maxRetries
                                    output = g.output
                                }
                            },
                        ]
                    }
                },
            ]
        }
    }
}
```

Then in `config.k`:

```python
import bento
import bento.helpers

bento.Config {
    input = bento.Input {
        generate = bento.GenerateInput {
            mapping = "root = {\"message\": \"Hello, KCL!\"}"
        }
    }
    output = helpers.guarded(helpers.Guarded {
        errorMessage = "failed to process: ${! error() }"
        retryErrorMessage = "exhausted retries"
        errorHandling = "drop"
        output = bento.Output {
            http_client = bento.HttpClientOutput {
                url = "http://localhost:4195/example"
                retries = 0
            }
        }
    })
}
```

As with the simpler example, render with `kcl run config.k -o yaml` and lint the result with `bento lint` before running.
