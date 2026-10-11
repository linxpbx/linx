-- What a phone saw of each call it made or took (owner's idea, 2026-10-11).
--
-- After every call the Linx app sends the same words its Call details
-- screen copies out, plus a list of the call's SIP messages by their first
-- line and time: the steps of setting the call up, the routes the phone
-- offered and tried, the sound in and out, and anything that went wrong.
-- No sound, no message bodies, no passwords. An admin reads them on the
-- phone's page, so a call that went wrong can be understood without a
-- cable to the phone or someone pasting text.
--
-- Small and short-lived: at most 16 KB each, kept seven days, and no more
-- than the newest hundred per phone. Saving one clears what is past that,
-- so nothing extra runs to tidy up.
CREATE TABLE phone_call_report (
    id          uuid PRIMARY KEY,
    tenant_id   uuid NOT NULL REFERENCES tenant (id),
    device_id   uuid NOT NULL REFERENCES device (id) ON DELETE CASCADE,
    call_id     text NOT NULL CHECK (length(call_id) <= 200),
    direction   text NOT NULL CHECK (direction IN ('incoming', 'outgoing')),
    peer        text NOT NULL CHECK (length(peer) <= 100),
    started_at  timestamptz NOT NULL,
    ended_at    timestamptz,
    report      text NOT NULL CHECK (length(report) <= 16384),
    created_at  timestamptz NOT NULL
);

CREATE INDEX phone_call_report_device ON phone_call_report (device_id, created_at DESC);
