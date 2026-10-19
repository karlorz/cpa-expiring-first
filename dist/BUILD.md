# Build artifacts

## darwin/arm64 (built here)

`dist/darwin_arm64/expiring-first.dylib` was built on this Mac (macOS, Apple Silicon):

```sh
CGO_ENABLED=1 go build -trimpath -buildmode=c-shared \
  -ldflags "-s -w -X main.version=0.3.0" \
  -o dist/darwin_arm64/expiring-first.dylib .
```

It exports the plugin ABI symbols (`cliproxy_plugin_init`, `cliproxyPluginCall`,
`cliproxyPluginFree`, `cliproxyPluginShutdown`). The generated `expiring-first.h` header is
removed, as the release workflow does.

## linux/arm64 (not built here)

The linux/arm64 `c-shared` build cannot be produced on this Mac. `GOOS=linux GOARCH=arm64
CGO_ENABLED=1` compiles the cgo runtime with the host toolchain, and the macOS clang against the
macOS SDK rejects the linux cgo sources (`clearenv`, `setresgid`, `setresuid` are undeclared). A
linux cross toolchain (`aarch64-linux-gnu-gcc`) or `zig cc` is not installed, and installing one
is out of scope for this change. No `.so` was faked.

Build it on a Linux host (or any host with an aarch64 linux cross toolchain) with:

```sh
CGO_ENABLED=1 GOOS=linux GOARCH=arm64 go build -trimpath -buildmode=c-shared \
  -ldflags "-s -w -X main.version=0.3.0" \
  -o dist/linux_arm64/expiring-first.so .
```

The CI workflow (`.github/workflows/build.yml`) already builds every release target, including
linux/arm64 on `ubuntu-24.04-arm`, so release artifacts come from CI rather than this machine.
