# @bijectionhq/helpers maintenance

Use `scripts/land.py` for publication and checkout synchronization. Source edits
run under `python3 scripts/land.py --edit -- <command>`. Preserve other writers.
Do not stage, rewrite published history, or replace files from stale snapshots.
Keep upstream contracts intact. Read BIJECTION.md and concave.json before edits.
Every semantic divergence needs an invariant, a bounded source fence, a witness,
and an explicit removal condition. Record it with Concave before landing.
Do not add automatic CI, publish packages, deploy, or create paid runtime unless
explicitly requested. Fork examples are upstream comparison material.
