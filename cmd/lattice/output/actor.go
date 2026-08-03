package output

import "github.com/spf13/cobra"

// AddActorFlags registers the --actor and --actor-token flags shared by every
// control-plane subcommand across the lens, loom and weaver groups, each of
// which defaults --actor at RunE time to the credential file's actorKey when
// unset. A resolved-empty actor is NOT an error here (unlike the write-path
// `op submit`): with no JWT trust root configured server-side the capability
// gate is not enforced, so an anonymous request must keep working.
// --actor-token carries a signed actor JWT (mint one with
// `gateway dev-token -sub <identityNanoID>` against a dev-mode server); when
// set it is stamped in place of --actor and wins if both are given, since
// presenting a token is the deliberate opt-in to verified-actor mode.
func AddActorFlags(cmd *cobra.Command, actor, actorToken *string) {
	cmd.Flags().StringVar(actor, "actor", "", "actor key stamped on the control request (defaults to credential file actorKey)")
	cmd.Flags().StringVar(actorToken, "actor-token", "", "signed actor JWT stamped on the control request (verified-actor mode; overrides --actor)")
}

// ResolveActorHeader picks the control-request Lattice-Actor header value:
// actorToken wins when non-empty (verified-actor mode), otherwise the raw actor
// key (self-asserted mode).
func ResolveActorHeader(actor, actorToken string) string {
	if actorToken != "" {
		return actorToken
	}
	return actor
}
