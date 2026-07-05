SELECT entity_key, applicant, applicant_name, applicant_email, applicant_phone,
       landlord_key, unit_key, unit_address, unit_city,
       unit_region, unit_rent, unit_currency, unit_status, signed_at,
       landlord_decision, decline_reason, terms_move_in_date,
       terms_lease_term_months, terms_requested_rent,
       COALESCE(profile_submitted, false) AS profile_submitted, income_to_rent_met, employment_verified,
       reference_count, has_co_applicant, has_guarantor,
       guarantor_income_to_rent_met, COALESCE(qualified, false) AS qualified
FROM read_landlord_lease_applications
ORDER BY unit_key, app_id
