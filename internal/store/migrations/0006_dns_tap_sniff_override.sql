-- DNS tap: copies of Mosdns answers feed the observe page (domain behind an
-- IP target). Observation only; on by default.
ALTER TABLE dns_config ADD COLUMN tap INTEGER NOT NULL DEFAULT 1;
-- Sniff override: let Xray replace the destination with the sniffed domain
-- (logs show names; direct flows are re-resolved by Xray through Mosdns).
ALTER TABLE network_config ADD COLUMN sniff_override INTEGER NOT NULL DEFAULT 0;
