---
status: accepted
---

# ModelDev decides when its original quota occupancy ends

ModelDev decides when its accepted execution's quota occupancy ends from its
existing close and cleanup facts. Governance authenticates the original owner,
validates the original CREATE and execution, and releases the original ledger
charges idempotently. Accelerator independently observes bindings, capacity and
physical resource release. Its availability or findings do not authorize or
veto ModelDev's release event.

The existing lifecycle transaction first stores the immutable scope and outbox
together. Delivery happens after commit and retries the same persisted event
until a matching Governance ACK is saved. Owner readiness, NoDispatch rules,
identity checks, scope anchors, event and ACK immutability remain unchanged.
Optional observation data is diagnostic only; no observation worker or new
release RPC is introduced.

Scope without an outbox is incompatible with this single contract. It is never
repaired by the normal release path. The authorized cutover settles each listed
original charge through the existing authenticated Governance interface before
removing that incompatible ModelDev aggregate. The fixed maintenance journal
preserves the event identity across unknown results and restarts. Legitimate
pending outboxes retain their normal retry path.

Deleting an execution removes its original query, log, artifact-list and new
artifact authorization entries. Object files, retained workspaces and models
independently registered READY in Model remain separate assets. Existing signed
URLs and authorized requests retain their original lifetime; deletion does not
provide immediate URL revocation. This decision preserves existing training
precision, log retention, owner registration and Pod signing behavior.
