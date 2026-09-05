package main

import "github.com/operatinggraph/lattice/internal/refractor/adapter"

// applicantProtectedColumns is the full read_lease_applications column set
// (D1.3 Fire 2/3 + D1.5), shared by every RLS-gated test that provisions this
// table so a column added to the lens (packages/lease-signing/lenses.go's
// leaseApplicationsReadSpec Columns) needs updating in exactly one test place.
func applicantProtectedColumns() []adapter.ColumnDef {
	return []adapter.ColumnDef{
		{Name: "entity_key", Type: "text"},
		{Name: "applicant", Type: "text"},
		{Name: "unit_key", Type: "text"},
		{Name: "unit_address", Type: "text"},
		{Name: "unit_city", Type: "text"},
		{Name: "unit_region", Type: "text"},
		{Name: "unit_rent", Type: "double precision"},
		{Name: "unit_currency", Type: "text"},
		{Name: "unit_status", Type: "text"},
		{Name: "unit_bedrooms", Type: "double precision"},
		{Name: "unit_bathrooms", Type: "double precision"},
		{Name: "unit_available_from", Type: "text"},
		{Name: "signed_at", Type: "text"},
		{Name: "landlord_decision", Type: "text"},
		{Name: "decline_reason", Type: "text"},
		{Name: "terms_move_in_date", Type: "text"},
		{Name: "terms_lease_term_months", Type: "double precision"},
		{Name: "terms_requested_rent", Type: "double precision"},
		{Name: "doc_store_name", Type: "text"},
		{Name: "doc_filename", Type: "text"},
		{Name: "doc_content_type", Type: "text"},
		{Name: "profile_submitted", Type: "boolean"},
		{Name: "income_to_rent_met", Type: "boolean"},
		{Name: "employment_verified", Type: "boolean"},
		{Name: "reference_count", Type: "double precision"},
		{Name: "has_co_applicant", Type: "boolean"},
		{Name: "has_guarantor", Type: "boolean"},
		{Name: "guarantor_income_to_rent_met", Type: "boolean"},
		{Name: "missing_onboarding", Type: "boolean"},
		{Name: "missing_bgcheck", Type: "boolean"},
		{Name: "missing_payment", Type: "boolean"},
		{Name: "missing_signature", Type: "boolean"},
		{Name: "missing_decision", Type: "boolean"},
		{Name: "inflight_bgcheck", Type: "boolean"},
		{Name: "inflight_payment", Type: "boolean"},
		{Name: "declined_bgcheck", Type: "boolean"},
		{Name: "declined_payment", Type: "boolean"},
		{Name: "declined", Type: "boolean"},
	}
}

// landlordProtectedColumns is the full read_landlord_lease_applications column
// set (D1.3 Increment 2/3 + D1.5's doc-pointer follow-up), shared by every
// RLS-gated test that provisions this table.
func landlordProtectedColumns() []adapter.ColumnDef {
	return []adapter.ColumnDef{
		{Name: "entity_key", Type: "text"},
		{Name: "applicant", Type: "text"},
		{Name: "landlord_key", Type: "text"},
		{Name: "unit_key", Type: "text"},
		{Name: "unit_address", Type: "text"},
		{Name: "unit_city", Type: "text"},
		{Name: "unit_region", Type: "text"},
		{Name: "unit_rent", Type: "double precision"},
		{Name: "unit_currency", Type: "text"},
		{Name: "unit_status", Type: "text"},
		{Name: "signed_at", Type: "text"},
		{Name: "landlord_decision", Type: "text"},
		{Name: "decline_reason", Type: "text"},
		{Name: "terms_move_in_date", Type: "text"},
		{Name: "terms_lease_term_months", Type: "double precision"},
		{Name: "terms_requested_rent", Type: "double precision"},
		{Name: "doc_store_name", Type: "text"},
		{Name: "doc_filename", Type: "text"},
		{Name: "doc_content_type", Type: "text"},
		{Name: "profile_submitted", Type: "boolean"},
		{Name: "income_to_rent_met", Type: "boolean"},
		{Name: "employment_verified", Type: "boolean"},
		{Name: "reference_count", Type: "double precision"},
		{Name: "has_co_applicant", Type: "boolean"},
		{Name: "has_guarantor", Type: "boolean"},
		{Name: "guarantor_income_to_rent_met", Type: "boolean"},
		{Name: "applicant_name", Type: "text"},
		{Name: "applicant_email", Type: "text"},
		{Name: "applicant_phone", Type: "text"},
		{Name: "qualified", Type: "boolean"},
	}
}
