"""Compatibility entry tests; the single admission policy is tested in Go."""
import os
import unittest
from unittest.mock import patch
import admission


class CompatibilityTests(unittest.TestCase):
    def test_executes_formal_binary_with_explicit_materials(self):
        binary = "/approved/ani-modeldev-webhook"
        with patch.dict(os.environ, {"ANI_MODELDEV_WEBHOOK_BINARY": binary}), patch("sys.argv", ["admission.py", "--config", "/config/policy.json", "--certificate", "/tls/tls.crt", "--key", "/tls/tls.key"]), patch("os.execv") as execute:
            admission.main()
        execute.assert_called_once_with(binary, [binary, "--config", "/config/policy.json", "--tls-cert", "/tls/tls.crt", "--tls-key", "/tls/tls.key"])
        self.assertFalse(hasattr(admission, "review"))


if __name__ == "__main__":
    unittest.main()
