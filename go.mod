module github.com/universaltill/ut-plugin-tax-uk

go 1.22.0

// Test-only: src/wasmrun runs the compiled plugin.wasm through the same
// engine universal-till uses. Nothing shipped in bin/plugin.wasm imports it.
require github.com/tetratelabs/wazero v1.9.0
