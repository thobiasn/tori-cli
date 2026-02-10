# Test

Write or improve tests for the specified code.

## Process

1. **Read the code under test** and identify its public behavior — what does it accept, what does it return, what side effects does it have?
2. **Read CLAUDE.md testing section** for conventions.
3. **Write tests that verify behavior, not implementation.** If you're testing private methods or internal state, stop — test the public interface instead.
4. **Use table-driven tests** for anything with multiple input/output cases:
   ```go
   tests := []struct {
       name     string
       input    InputType
       expected OutputType
   }{
       {"descriptive name", input, expected},
   }
   for _, tt := range tests {
       t.Run(tt.name, func(t *testing.T) {
           // ...
       })
   }
   ```
5. **For `internal/protocol` tests:** always test the full roundtrip — create a message, encode it to msgpack, decode it back, verify all fields match. This is the contract.
6. **For `internal/agent` tests:** prefer integration tests that use a real SQLite database (in-memory with `:memory:`) and real `/proc` parsing (read the host's actual `/proc` in CI). Only mock the Docker API when testing container-specific logic that can't run without Docker.
7. **For `internal/tui` tests:** test the bubbletea model's `Update` function with specific messages and verify the resulting model state. Don't try to test visual rendering.

## Rules

- Test file goes next to the code: `foo.go` → `foo_test.go`.
- Test function names: `TestFunctionName_scenario` (e.g., `TestCollectCPU_parsesMultipleCores`).
- No test helpers that hide assertions. Keep `if got != want` visible in the test.
- No `testify` or assertion libraries. Use the stdlib `testing` package.
- If a test needs fixtures, put them in `testdata/` in the same package.
- Tests must not depend on external state or ordering. Each test sets up what it needs.
