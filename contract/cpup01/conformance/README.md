# Current execution contract vectors

The v2.1 JSON files are independently generated with Python from synthetic inputs by `scripts/generate-contract-vectors.py`. Run generation and `--check` on Fedora. Go's encoder must match these bytes and hashes. All namespaces, registries and identities are test fixtures and establish no deployment or enablement facts. The exported `*V1` Go test helper names remain source-level fixture names; they produce only the one active v2.1 contract and do not decode historical requests.
