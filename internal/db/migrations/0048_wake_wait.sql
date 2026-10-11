-- A call waits longer for a woken iPhone or iPad (2026-10-11).
--
-- Six seconds wasn't enough on a phone whose own connection to Apple's push
-- service had just dropped and come back: the call went to voicemail
-- while the phone was still waking, and then rang for nobody. Ten seconds
-- is the new default. A phone that signs in sooner is rung at once (the
-- wait ends the moment it arrives), so this only costs time when a phone is
-- slow, and then it costs the phones already there up to four seconds more
-- before they ring too.
--
-- A server still on the old default moves with it. One that an admin set
-- to something else keeps their choice.
ALTER TABLE push_settings ALTER COLUMN wait_ms SET DEFAULT 10000;
UPDATE push_settings SET wait_ms = 10000 WHERE wait_ms = 6000;
