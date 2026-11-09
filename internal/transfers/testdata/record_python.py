"""Record actual Python Transfers.start/_offer/incoming outputs in an isolated temp state.

Usage: python record_python.py --python-src /checkout/tmx/src
Reference: blackcode-ch/terminal-matrix v2, 39a6ae9267783f76f7feaecdf9ae45aee2ff3590.
No daemon is started and no network request or user state is touched.
"""
import argparse
import asyncio
import json
import os
from pathlib import Path
import sys
import tempfile
from unittest.mock import patch

async def record(source):
    with tempfile.TemporaryDirectory(prefix="tmx-wire-") as tmp:
        base = Path(tmp)
        os.environ["TMX_HOME"] = str(base / "state")
        sys.path.insert(0, str(Path(source).resolve()))
        from tmx import transfers, paths
        data = b"hello from Python\n"
        src = base / "source" / "hello.txt"
        src.parent.mkdir()
        src.write_bytes(data)
        dest = base / "destination"
        dest.mkdir()
        calls = []

        class Daemon:
            host = "source"
            cfg = {}
            def trusted_peers(self):
                return [{"name": "target"}, {"name": "source"}]
            async def forward(self, host, method, path, body=None):
                calls.append({"method": method, "path": path, "body": body})
                return {"id": "2222222222222222", "state": "queued"}

        class Receiver(transfers.Transfers):
            async def _pull(self, rec):
                # Record the admission response only; do not run a network pull.
                return

        with patch.object(paths, "home", lambda: base / "state"), \
             patch.object(transfers, "_now", lambda: 1800000000.0), \
             patch.object(transfers.secrets, "token_hex", lambda n: "1111111111111111"), \
             patch.object(transfers.secrets, "token_urlsafe", lambda n: "abcdefghijklmnopqrstuvwxABCDEFGH"):
            d = Daemon()
            sender = transfers.Transfers(d)
            request = {"src": str(src), "to": "target", "dest": str(dest), "force": False}
            started = await sender.start(request, "source")
            start_response = dict(started)
            await sender.tasks[started["id"]]
            d.host = "target"
            receiver = Receiver(d, root=base / "receiver")
            with patch.object(transfers.secrets, "token_hex", lambda n: "2222222222222222"):
                response = await receiver.incoming(calls[0]["body"], "source")
                await asyncio.gather(*receiver.tasks.values())
            # web.Response carries its JSON value in .body on the reference version.
            received = receiver.get("2222222222222222")
            status = getattr(response, "status", 202)
            fixture = {
                "reference_commit": "39a6ae9267783f76f7feaecdf9ae45aee2ff3590",
                "payload": data.decode(),
                "start": {"method": "POST", "path": "/api/transfers", "request": request,
                          "status": 202, "response": start_response},
                "incoming": {**calls[0], "status": status, "response": received},
                "list": {"host": "target", "transfers": receiver.list()},
                "offered": sender.get(started["id"]),
            }
            def normalize(v):
                if isinstance(v, dict): return {k: normalize(x) for k, x in v.items()}
                if isinstance(v, list): return [normalize(x) for x in v]
                if isinstance(v, str):
                    return v.replace(str(src), "$SRC").replace(str(dest), "$DEST")
                return v
            output = Path(__file__).with_name("python_wire.json")
            output.write_text(json.dumps(normalize(fixture), indent=2, sort_keys=True) + "\n", encoding="utf-8")

if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("--python-src", required=True)
    asyncio.run(record(parser.parse_args().python_src))
