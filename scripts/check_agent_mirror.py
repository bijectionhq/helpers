from pathlib import Path
assert Path("AGENTS.md").read_bytes() == Path("CLAUDE.md").read_bytes(), "Regenerate CLAUDE.md from AGENTS.md"
