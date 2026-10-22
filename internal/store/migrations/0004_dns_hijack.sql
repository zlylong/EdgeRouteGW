-- DNS leak protection: redirect LAN port-53 queries to Mosdns and reject
-- DNS-over-TLS so clients fall back to plain DNS (which is then hijacked).
ALTER TABLE dns_config ADD COLUMN hijack INTEGER NOT NULL DEFAULT 1;
ALTER TABLE dns_config ADD COLUMN block_dot INTEGER NOT NULL DEFAULT 1;
