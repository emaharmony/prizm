# ADR 0001: Runtime owns authority

Status: Accepted

Prizm models are untrusted generators inside a deterministic lifecycle. The
runtime owns routing, policy, capability checks, approvals, validation,
persistence, and execution boundaries. Integrations may request actions but
must use the same Core gates. Models may not approve their own mutations or
select arbitrary shell validation commands.

This increases explicit wiring and test burden, but makes effects auditable and
keeps optional providers and integrations from becoming authority boundaries.
