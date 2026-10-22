"""Deprecated process entry: execute the formal Go webhook, with no policy here.

Flat historical images config is not converted into namespace authority. Supply
ani.modeldev.webhook.v1 and canonical Release materials explicitly.
"""
import argparse
import os


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", required=True)
    parser.add_argument("--certificate", required=True)
    parser.add_argument("--key", required=True)
    args = parser.parse_args()
    binary = os.environ.get("ANI_MODELDEV_WEBHOOK_BINARY", "/usr/local/bin/ani-modeldev-webhook")
    os.execv(binary, [binary, "--config", args.config, "--tls-cert", args.certificate, "--tls-key", args.key])


if __name__ == "__main__":
    main()
