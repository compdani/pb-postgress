"""One-time maintenance tool: export historical fixtures without a Go SQLite driver.

Run from the repository root with Python's standard-library sqlite3 module.
The test runtime reads fixtures.json and never opens the historical .db files.
"""
import json
import sqlite3
from pathlib import Path

root = Path(__file__).parent
connection = sqlite3.connect(root / "data.db")
connection.row_factory = sqlite3.Row
fixtures = {}
for (name,) in connection.execute("SELECT name FROM sqlite_master WHERE type='table'"):
    if name.startswith("sqlite_") or name == "_migrations":
        continue
    fixtures[name] = [
        {key: value.decode() if isinstance(value, bytes) else value
         for key, value in dict(row).items()}
        for row in connection.execute('SELECT * FROM "' + name + '"')
    ]
(root / "fixtures.json").write_text(json.dumps(fixtures, indent=2) + "\n")
connection.close()
