"""Exercise deployment and rollback with isolated service/Nix/Caddy substitutes."""
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


class DeploymentTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="buku-deploy-test-")
        self.root = Path(self.temp.name)
        self.config = self.root / "config"
        self.data = self.root / "data"
        self.state = self.root / "state"
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.bash = shutil.which("bash")
        script = Path(__file__).with_name("deploy.sh").read_text()
        self.script = self.root / "deploy.sh"
        self.script.write_text(script.replace("@APP@", "/nix/store/test-buku-journal"))
        self.env = dict(os.environ, XDG_CONFIG_HOME=str(self.config),
                        XDG_DATA_HOME=str(self.data), XDG_STATE_HOME=str(self.state),
                        TEST_ROOT=str(self.root), PATH=f"{self.bin}:{os.environ['PATH']}")
        self.stub("systemctl", r'''
shift
case "$1" in
  show-environment) exit 0 ;;
  show) [[ "$2" != hooddy.service ]] || echo /test/hooddy.service; exit 0 ;;
  is-active) [[ "$2" == --quiet ]] && shift; [[ -e "$TEST_ROOT/active" ]]; exit $? ;;
  is-enabled) [[ -e "$TEST_ROOT/enabled" ]]; exit $? ;;
  enable) touch "$TEST_ROOT/enabled" ;;
  disable) [[ ! -e "$TEST_ROOT/enabled" ]] || unlink "$TEST_ROOT/enabled" ;;
  start|restart) touch "$TEST_ROOT/active" ;;
  stop) [[ ! -e "$TEST_ROOT/active" ]] || unlink "$TEST_ROOT/active" ;;
esac
exit 0
''')
        self.stub("loginctl", "echo yes\n")
        self.stub("nix-store", 'ln -sfn "$5" "$2"\n')
        self.stub("curl", "exit 0\n")
        hooddy = self.config / "hooddy"
        (hooddy / "sites.d").mkdir(parents=True)
        (hooddy / "Caddyfile").write_text("import sites.d/*.caddy\n")
        caddy = hooddy / "caddy/bin/caddy"
        caddy.parent.mkdir(parents=True)
        caddy.write_text(f"#!{self.bash}\n[[ ! -e \"$TEST_ROOT/fail-validation\" ]]\n")
        caddy.chmod(0o755)
        self.unrelated = hooddy / "sites.d/other.caddy"
        self.unrelated.write_text("other.example.com { respond ok }\n")
        self.unit = self.config / "systemd/user/buku-journal.service"
        self.site = hooddy / "sites.d/buku-journal.caddy"

    def tearDown(self):
        self.temp.cleanup()

    def stub(self, name, source):
        path = self.bin / name
        path.write_text(f"#!{self.bash}\n" + source)
        path.chmod(0o755)

    def run_deploy(self, *args):
        return subprocess.run([self.bash, str(self.script), *args], env=self.env,
                              capture_output=True, text=True, timeout=10)

    def test_install_and_update_preserve_data(self):
        journal = self.data / "buku-journal/journal"
        journal.mkdir(parents=True)
        record = journal / "2026-01-01-example.md"
        record.write_text("A memory\n")
        for port in ("3002", "3003"):
            result = self.run_deploy("--port", port)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertIn(f"127.0.0.1:{port}", self.unit.read_text())
            self.assertIn(f"127.0.0.1:{port}", self.site.read_text())
        self.assertEqual(record.read_text(), "A memory\n")
        self.assertEqual(self.unrelated.read_text(), "other.example.com { respond ok }\n")
        self.assertEqual(self.unit.stat().st_mode & 0o777, 0o600)
        self.assertEqual(self.site.stat().st_mode & 0o777, 0o600)
        self.assertTrue((self.state / "buku-journal/package").is_symlink())

    def test_invalid_site_rolls_back_update(self):
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        unit, site = self.unit.read_bytes(), self.site.read_bytes()
        (self.root / "fail-validation").touch()
        result = self.run_deploy("--port", "3003")
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.unit.read_bytes(), unit)
        self.assertEqual(self.site.read_bytes(), site)
        self.assertTrue((self.root / "active").exists())

    def test_refuse_unmanaged_site(self):
        self.site.write_text("do not touch\n")
        result = self.run_deploy()
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self.site.read_text(), "do not touch\n")
        self.assertFalse(self.unit.exists())

    def test_invalid_domain_is_rejected(self):
        result = self.run_deploy("--domain", "example.com { respond exposed }")
        self.assertNotEqual(result.returncode, 0)
        self.assertFalse(self.unit.exists())

    @unittest.skipUnless(shutil.which("systemd-analyze"), "systemd-analyze unavailable")
    def test_generated_unit_passes_real_systemd_validation(self):
        result = self.run_deploy()
        self.assertEqual(result.returncode, 0, result.stderr)
        # Validate offline with a real executable; no service is installed or run.
        self.unit.write_text(self.unit.read_text().replace(
            "/nix/store/test-buku-journal/bin/buku-journal", shutil.which("true")))
        runtime = self.root / "runtime"
        runtime.mkdir(mode=0o700)
        result = subprocess.run(["systemd-analyze", "--user", "verify", str(self.unit)],
                                env=dict(self.env, XDG_RUNTIME_DIR=str(runtime)),
                                capture_output=True, text=True, timeout=10)
        self.assertEqual(result.returncode, 0, result.stderr)


if __name__ == "__main__":
    unittest.main()
