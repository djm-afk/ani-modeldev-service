#!/usr/bin/env python3
"""Fresh task certificates must pass real OpenSSL and Python strict TLS checks.

The Cluster boundary is local-only. This regression does not read task secrets,
register any cluster object, or start a pipeline execution.
"""
import base64
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import socket
import ssl
import subprocess
import tempfile
import unittest

from materials import SYSTEM, certificate_materials


class ObjectBoundary:
    def __init__(self, root):
        self.root = root
        self.applied = []

    def get(self, resource, name, namespace):
        if (resource, name, namespace) not in [
            ("secret", "ani-rustfs-tls", "ani-platform"),
            ("secret", "ani-kfp-ca", "kubeflow"),
        ]:
            raise AssertionError("unexpected external object read")
        # A real public certificate stands in only for the cluster object.
        public_ca = (self.root / ".private/task-ca.crt").read_bytes()
        return {"data": {"ca.crt": base64.b64encode(public_ca).decode()}}

    def apply(self, value):
        if value["kind"] != "Secret" or value["metadata"]["name"] != "core-flow-modeldev-tls" or value["metadata"]["namespace"] != SYSTEM:
            raise AssertionError("unexpected external object mutation")
        self.applied.append(value)


class StrictCertificateRegression(unittest.TestCase):
    def test_fresh_materials_pass_strict_chain_and_hostname_validation(self):
        with tempfile.TemporaryDirectory(prefix="strict-tls-", dir=".") as temporary:
            root = Path(temporary).resolve()
            boundary = ObjectBoundary(root)
            certificate_materials(boundary, root)
            self.assertEqual(len(boundary.applied), 1)
            # Verify the actual Secret payload that the assembly would apply.
            material = root / "applied"
            material.mkdir()
            for key, value in boundary.applied[0]["data"].items():
                (material / key).write_bytes(base64.b64decode(value, validate=True))
            verify = subprocess.run(
                ["openssl", "verify", "-x509_strict", "-purpose", "sslserver", "-verify_hostname", "ani-modeldev-service", "-CAfile", str(material / "ca.crt"), str(material / "tls.crt")],
                capture_output=True, text=True, timeout=15,
            )
            with self.subTest(verifier="OpenSSL strict chain"):
                self.assertEqual(verify.returncode, 0, verify.stdout + verify.stderr)
            context = ssl.create_default_context(cafile=str(material / "ca.crt"))
            # Python 3.14's default is strict; never relax it to hide bad chains.
            self.assertTrue(context.verify_flags & ssl.VERIFY_X509_STRICT)
            self.assertTrue(context.check_hostname)
            server_context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            server_context.load_cert_chain(str(material / "tls.crt"), str(material / "tls.key"))

            def handshake(hostname, permitted):
                with socket.socket() as listener:
                    listener.bind(("127.0.0.1", 0))
                    listener.listen(1)
                    listener.settimeout(5)

                    def serve():
                        raw, _ = listener.accept()
                        raw.settimeout(5)
                        try:
                            with server_context.wrap_socket(raw, server_side=True) as connection:
                                connection.sendall(connection.recv(64))
                        except ssl.SSLError:
                            if permitted:
                                raise
                        finally:
                            raw.close()

                    with ThreadPoolExecutor(max_workers=1) as executor:
                        serving = executor.submit(serve)
                        with socket.create_connection(listener.getsockname(), timeout=5) as raw:
                            if permitted:
                                with context.wrap_socket(raw, server_hostname=hostname) as connection:
                                    connection.sendall(b"strict TLS")
                                    self.assertEqual(connection.recv(64), b"strict TLS")
                            else:
                                with self.assertRaisesRegex(ssl.SSLCertVerificationError, "Hostname mismatch"):
                                    context.wrap_socket(raw, server_hostname=hostname)
                        serving.result(timeout=10)

            with self.subTest(verifier="Python strict TLS"):
                handshake("ani-modeldev-service", True)
                handshake("modeldev." + SYSTEM + ".svc", True)
                handshake("unapproved-service.invalid", False)
            # Reassembling a task preserves the original cert/key identities.
            original = {path.name: path.read_bytes() for path in (root / ".private").iterdir() if path.suffix in [".crt", ".key"]}
            certificate_materials(boundary, root)
            retained = {path.name: path.read_bytes() for path in (root / ".private").iterdir() if path.suffix in [".crt", ".key"]}
            self.assertEqual(original, retained)


if __name__ == "__main__":
    unittest.main()
