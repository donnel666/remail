"""Exercise the actual Compose and deployment probes against both image contracts."""
import json
import os
from pathlib import Path
import re
import subprocess
import tempfile
import unittest

ROOT = Path(__file__).resolve().parents[1]


class MonitoringProbeTest(unittest.TestCase):
    def test_new_and_legacy_images_preserve_readiness(self):
        compose = (ROOT / "deploy/compose.yml").read_text().split("\n  server:", 1)[1]
        probe = json.loads(re.search(r'      test: (\["CMD-SHELL".*\])', compose)[1])[1]
        probe = probe.replace("$$", "$")
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        wait = re.search(r"          wait_for_health\(\) \{.*?\n          \}", workflow, re.S)[0]
        image_path = re.search(r'ENV REMAIL_READINESS_PATH="([^"]+)"', (ROOT / "Dockerfile").read_text())[1]
        with tempfile.TemporaryDirectory() as directory:
            task = Path(directory)
            scripts = {
                "docker": '#!/bin/sh\nwhile [ "$1" != sh ]; do shift; done\nexec "$@"\n',
                "sleep": "#!/bin/sh\nexit 0\n",
                "wget": '''#!/bin/sh
for url; do :; done
printf '%s\n' "$url" >> "$PROBE_LOG"
case "$url" in
  *'/healthz?ready=1')
    if [ "$IMAGE_KIND" = legacy ]; then printf '{"status":"ok"}'; exit 0; fi ;;
  *'/readyz')
    if [ "$IMAGE_KIND" = current ]; then exit 1; fi ;;
  *) exit 1 ;;
esac
if [ "$DEPENDENCY_FAILED" = 1 ]; then exit 1; fi
printf '{"status":"ok"}'
''',
            }
            for name, source in scripts.items():
                executable = task / name
                executable.write_text(source)
                executable.chmod(0o700)
            for kind in ("legacy", "current"):
                for failed in (False, True):
                    for name, command in (("compose", probe), ("deploy_and_rollback", wait + "\nwait_for_health")):
                        with self.subTest(image=kind, failed=failed, probe=name):
                            log = task / "requests"
                            log.write_text("")
                            env = dict(os.environ, PATH=f"{task}:{os.environ['PATH']}", IMAGE_KIND=kind,
                                       DEPENDENCY_FAILED=str(int(failed)), PROBE_LOG=str(log))
                            env.pop("REMAIL_READINESS_PATH", None)
                            if kind == "current":
                                env["REMAIL_READINESS_PATH"] = image_path
                            result = subprocess.run(["bash", "-c", command], env=env, capture_output=True, timeout=10)
                            self.assertEqual(result.returncode == 0, not failed, result.stderr.decode())
                            requests = log.read_text().splitlines()
                            self.assertTrue(requests)
                            expected = image_path if kind == "current" else "/readyz"
                            self.assertEqual(set(requests), {"http://127.0.0.1:8080" + expected})


if __name__ == "__main__":
    unittest.main()
