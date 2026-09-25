-- Each authenticator code works once (docs/WEB.md §4): the 30-second step
-- of the last code accepted for this account. A code is accepted only for a
-- later step, so one seen over someone's shoulder (or replayed from a
-- captured request) can't be used again within its ±30 s window. Set when
-- MFA is confirmed too, so the enrollment code can't sign in afterwards.
ALTER TABLE app_user ADD COLUMN mfa_last_step bigint;
