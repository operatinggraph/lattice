package grantchange

// GrantChangeEdgeSpansDeployment declares whether the D1 read-grant change edge
// reaches every process that hosts a personal lens, or only the one it fires in.
//
// It is FALSE, and that is a statement about the transport, not a switch: the
// edge is an in-process function call. A read-grant producer's guarded write
// calls notifyGrantChange, which calls Reprojector.GrantChanged, which drops the
// actor on a dirty set this process's drain worker owns. Nothing about that
// crosses a process boundary. A producer running on instance A therefore
// announces to no personal lens hosted on instance B, while every wiring
// conjunct a consumer could test — "a reprojector is wired", "the read gate is
// threaded" — stays true on both.
//
// Two mechanisms read it, and neither is decoration:
//
//   - the personal derivation licence's cardinality conjunct
//     (pipeline.PersonalHealerVerdict.EdgeSpansDeployment) refuses the narrowing
//     above one live Refractor instance while this is false, and stops asking
//     once it is true;
//   - scripts/lint-refractor-single-instance.go refuses the deployment
//     AFFORDANCE — a replica count, a second launch, an instance/shard knob —
//     while this is false, so the transition is caught at the author who makes
//     Refractor multi-instance rather than at the runtime that discovers it.
//
// FLIPPING IT IS A BUILD, NOT AN EDIT. It becomes true when the edge is carried
// by something durable and deployment-wide — a JetStream signal every instance
// consumes, per personal-lens-derivation-licence-design.md §8 alternative #6 —
// and that build is the precondition of re-licensing a personal lens on a
// multi-instance deployment. Setting it true without that build removes both
// mechanisms above and leaves the narrowing running over an edge that reaches
// one process in N.
const GrantChangeEdgeSpansDeployment = false
