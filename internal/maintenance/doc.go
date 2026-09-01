// Package maintenance coordinates one fail-closed admission control plane.
// BootstrapControlRoot is the only creator. ControlRoot/.maintenance.bootstrap.lock
// is permanent initialization proof only; admission is defined by the bound
// maintenance record and writer lock, never by holding the bootstrap file.
//
// Shared callers acquire one outer Permit for the complete workflow. Lower
// layers receive WithShared context and must not acquire nested permits.
// A mutated exclusive workflow may enter REOPENING and transfer to one target
// process; only its CanaryLease can durably record readiness and reopen.
package maintenance
