-- Clearer greetings (owner, 2026-10-02): a box's own greeting is kept as
-- 16 kHz 16-bit audio (Asterisk's slin16, as clear as Linx's own
-- messages) instead of 8 kHz mu-law; messages stay as recorded. 32 KB a
-- second, 0.5 to 30 seconds. Greetings shipped in the same day's
-- migration 0035 and nobody had recorded one, so any there are dropped
-- rather than converted.
DELETE FROM voicemail_greeting;
ALTER TABLE voicemail_greeting DROP CONSTRAINT voicemail_greeting_audio_check;
ALTER TABLE voicemail_greeting ADD CONSTRAINT voicemail_greeting_audio_check
    CHECK (length(audio) BETWEEN 16000 AND 992000 AND length(audio) % 2 = 0);
