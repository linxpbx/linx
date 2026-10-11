-- A phone's crash reports go where its call records go (owner, 2026-10-11).
--
-- iOS hands the app a report of a crash the next time it runs (Apple's
-- MetricKit), and the app sends it on, so a crash can be understood
-- without a cable to the phone. It is kept with that phone's call records,
-- under the same limits (16 KB, seven days, the newest hundred), as a
-- record of kind 'crash'. A crash has no direction and no other end.
ALTER TABLE phone_call_report ADD COLUMN kind text NOT NULL DEFAULT 'call'
    CHECK (kind IN ('call', 'crash'));
ALTER TABLE phone_call_report DROP CONSTRAINT phone_call_report_direction_check;
ALTER TABLE phone_call_report ADD CONSTRAINT phone_call_report_direction_check
    CHECK ((kind = 'call' AND direction IN ('incoming', 'outgoing')) OR (kind = 'crash' AND direction = ''));
