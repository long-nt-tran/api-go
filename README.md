# Temporal gRPC API and proto files compiled for Go

Generated Go files from Temporal [api](https://github.com/temporalio/api) repository.

_Note that any changes merged to [api](https://github.com/temporalio/api) will automatically trigger a [GitHub workflow](https://github.com/temporalio/api-go/blob/main/.github/workflows/update-proto.yml) that recompiles the proto files and commits the results to main._

## How to use

To install in your project run:
```
go get go.temporal.io/api
```

## Rebuild

Run `make` once to install all plugins and tools (`protoc` and `go` must be installed manually).

Run `make update-proto` to update the `proto/api` submodule and recompile proto files. The `proto/api-cloud` submodule
must be updated manually.

## RPC validation

Set `option (temporalvalidate.v1.rpc_validation).enabled = true;` once on an RPC
to enroll both its request and response. Server rejects invalid requests and
warns on invalid responses without changing the handler result.

Declared rule symbols (for example, `(temporalvalidate.v1.namespace) = true`)
generate the typed `Validator[C]` interface. API lint and the plugin enforce
canonical reuse and rule types; missing Server methods do not compile.
See the [authoring guide](proto/api/temporalvalidate/README.md)
for a complete example and commands run from each repository root.

## License

MIT License, please see [LICENSE](LICENSE) for details.
