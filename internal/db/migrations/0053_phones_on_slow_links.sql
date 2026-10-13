-- Phones on slow links (2026-10-13).
--
-- 1. Asterisk gives a phone this long to answer "are you there?" (OPTIONS)
--    before it counts the contact unreachable and won't ring it. Its own
--    default is 3 s. An iPhone woken for a call on mobile data took longer
--    (2.8 s and more, owner's iPhone on 5G), so Asterisk marked it
--    unreachable seconds after it had signed in, and the call it had been
--    woken for was never sent to it. The app and the browser get 10 s;
--    desk phones, on the office network, keep Asterisk's 3.
--
-- 2. A phone is also known by the certificate before its newest one. Each
--    renewal used to forget the old certificate at once, so a phone that
--    never got the answer carrying its new one (a reply lost on mobile
--    data, two requests at once) was told to be set up again. Accepting the
--    one it last had is as safe as before: a certificate is nothing without
--    the Secure Enclave key it was issued for, which every request proves.

CREATE OR REPLACE VIEW asterisk.ps_aors AS
SELECT
    l.sip_username AS id,
    '1'            AS max_contacts,
    'yes'          AS remove_existing,
    '30'           AS qualify_frequency,
    NULL::text     AS contact,
    -- A phone that subscribes to its own mailbox is answered from here.
    CASE WHEN l.kind NOT IN ('web', 'ios') THEN b.id::text END AS mailboxes,
    CASE WHEN l.kind IN ('web', 'ios') THEN '10' ELSE '3' END AS qualify_timeout
FROM device_live l
LEFT JOIN voicemail_box b ON b.extension_id = l.extension_id AND b.enabled;

ALTER TABLE device_identity ADD COLUMN prev_cert_fingerprint bytea;
CREATE INDEX device_identity_prev_fingerprint_idx ON device_identity (prev_cert_fingerprint)
    WHERE prev_cert_fingerprint IS NOT NULL;
