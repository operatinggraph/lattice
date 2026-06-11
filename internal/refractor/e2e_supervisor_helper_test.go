package refractor_test

import (
	"github.com/asolgan/lattice/internal/refractor/subjects"
	"github.com/asolgan/lattice/internal/substrate"
)

// e2eSpec builds the supervised-consumer spec the e2e tests pass to
// pipeline.RunOn, mirroring production wiring (durable refractor-<ruleID>, queue
// group = same name, DeliverLastPerSubject, Core KV stream + filter).
func e2eSpec(ruleID, bucket string) substrate.ConsumerSpec {
	return substrate.ConsumerSpec{
		Name:          "refractor-" + ruleID,
		Stream:        subjects.CoreKVStream(bucket),
		FilterSubject: subjects.CoreKVFilter(bucket),
		DeliverPolicy: substrate.DeliverLastPerSubject,
		DeliverGroup:  "refractor-" + ruleID,
	}
}
