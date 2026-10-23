-- A call a person has taken out of **their own** call list (owner,
-- 2026-10-23: "in the call log it show show the duration of the call and
-- ability to swipe to delete the record from the app/browser phone but not
-- server recored").
--
-- The company's record in call_record is untouched: Admin → Calls, the CSV
-- download and the call-quality screens all still show the call, and the
-- retention rules still decide when it really goes. This only hides the row
-- from one person's own list — their app, their browser phone, every device
-- they sign in on — and a row removed here brings it straight back, which
-- is what Undo does.
--
-- Keyed by the person, not the phone, so deleting it on an iPhone also
-- clears it in the browser: one call log per person, as the badge and the
-- voicemail counts already are.
CREATE TABLE call_hidden (
    tenant_id uuid        NOT NULL REFERENCES tenant (id),
    user_id   uuid        NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
    call_id   uuid        NOT NULL REFERENCES call_record (id) ON DELETE CASCADE,
    hidden_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, call_id)
);

-- For the listing's "not hidden for me" check, and for the cascade when a
-- call ages out of history.
CREATE INDEX call_hidden_call ON call_hidden (call_id);
