package wellnessledger

import (
	"encoding/json"
	"testing"
)

// schemaRequired decodes an OpMeta InputSchema and returns its top-level
// `required` array as a set.
func schemaRequired(t *testing.T, inputSchema string) map[string]bool {
	t.Helper()
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(inputSchema), &schema); err != nil {
		t.Fatalf("InputSchema is not valid JSON: %v", err)
	}
	set := map[string]bool{}
	for _, f := range schema.Required {
		set[f] = true
	}
	return set
}

// TestOpMetas_DebitRequiresMemo pins the WellnessDebitAccount descriptor to the
// write side's manual-charge rule (scripts.go post_entry): a debit carrying
// neither bookingRef nor priceBookingRef needs a non-blank memo. This
// descriptor exposes neither ref — both are Weaver's, minted by a settlement
// target (targets.go) — so every submission it drives is a manual charge, and a
// schema that let memo go empty would render a form whose submission the op
// refuses with nothing in the UI to explain it.
//
// WellnessCreditAccount is the control: a payment or waiver records money
// moved against a balance already itemised, so its memo stays optional and the
// same descriptor-driven form must keep offering it as such.
func TestOpMetas_DebitRequiresMemo(t *testing.T) {
	var sawDebit, sawCredit bool
	for _, m := range OpMetas() {
		switch m.OperationType {
		case "WellnessDebitAccount":
			sawDebit = true
			required := schemaRequired(t, m.InputSchema)
			for _, field := range []string{"accountKey", "amountCents", "memo"} {
				if !required[field] {
					t.Errorf("WellnessDebitAccount InputSchema: %q missing from required", field)
				}
			}
			if m.FieldDescriptions["memo"] == "" {
				t.Error("WellnessDebitAccount: memo carries no FieldDescription")
			}
		case "WellnessCreditAccount":
			sawCredit = true
			if schemaRequired(t, m.InputSchema)["memo"] {
				t.Error("WellnessCreditAccount InputSchema: memo is required, want optional — a credit names the balance it moves")
			}
		}
	}
	if !sawDebit || !sawCredit {
		t.Fatalf("OpMetas: debit found=%v, credit found=%v — both expected", sawDebit, sawCredit)
	}
}

// TestOpMetas_DebitDescriptorExposesNoSettlementRefs is the other half of the
// rule above: memo is required BECAUSE this descriptor can only ever author a
// manual charge. Exposing bookingRef or priceBookingRef here would make that
// premise false — a settlement debit is memo-optional — so a field added to the
// schema without revisiting the memo requirement reds this rather than shipping
// a form that demands a note for a charge whose ref already names its booking.
func TestOpMetas_DebitDescriptorExposesNoSettlementRefs(t *testing.T) {
	for _, m := range OpMetas() {
		if m.OperationType != "WellnessDebitAccount" {
			continue
		}
		var schema struct {
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal([]byte(m.InputSchema), &schema); err != nil {
			t.Fatalf("InputSchema is not valid JSON: %v", err)
		}
		for _, ref := range []string{"bookingRef", "priceBookingRef"} {
			if _, ok := schema.Properties[ref]; ok {
				t.Errorf("WellnessDebitAccount InputSchema declares %q — the descriptor would no longer author only manual charges, so the required memo needs re-deriving", ref)
			}
		}
		return
	}
	t.Fatal("WellnessDebitAccount not found in OpMetas")
}
