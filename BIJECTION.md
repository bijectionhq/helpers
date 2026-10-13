# @bijectionhq/helpers

This is a maintained fork of get-convex/convex-helpers at `d9dedf7a83259aa677a390a3e50a641218d10170`.
The package keeps inherited validators, function references, serialization, and
transaction behavior. Its SDK imports resolve through the application's single
`bijection` installation. This package is a stateless helper library. The native engine owns database
records, transactions and agent runs; the Agent component owns conversation
storage. These helpers compose the existing SDK contracts and acquire no
authority or independent persistent state.

Every inherited difference is checked by the same Concave source in Bijection.
Mechanical module and public identifier changes have explicit recipes in
`concave.json`. Runtime symbols and validator markers are never rebranded.
Semantic changes require narrow `concave:begin` / `concave:end` fences with
`why`, `witness`, `preserves`, and `remove-when`, followed by a recorded binding.
Never approve an entire source file to avoid explaining its changed invariants.

Run `just concave check`, then the selected build and tests. Land through
`scripts/land.py`; it refuses source-parity regressions. Upstream updates use
ordinary merge commits, followed by inspection and narrowly justified repairs.
Original examples and documentation are retained for comparison; they do not
advertise Bijection's native authority, billing, or recovery guarantees.
GitHub Actions are disabled. Verification is explicit and local.
