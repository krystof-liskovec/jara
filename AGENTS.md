# Repository guidance

- Keep every hand-maintained source, test, script, and documentation file below 500 lines (maximum 499, counting blank lines and comments).
- Split files by cohesive responsibility before they reach the limit. Keep related code together; avoid arbitrary line-based fragments and pass-through packages.
- Generated or format-constrained data may exceed the limit when splitting would break its consumer. The seccomp profile at `deploy/seccomp/codex.json` is exempt.
