-- Encrypted DNS by default: DoH/DoT to the same providers, IP-form so no
-- bootstrap is needed. Only stores still on the original plain defaults are
-- switched; operator-chosen lists are left alone.
UPDATE dns_config SET local_upstreams = '["https://223.5.5.5/dns-query","tls://1.12.12.12"]'
WHERE local_upstreams = '["119.29.29.29","223.5.5.5"]';
UPDATE dns_config SET remote_upstreams = '["https://1.1.1.1/dns-query","tls://8.8.8.8"]'
WHERE remote_upstreams = '["1.1.1.1","8.8.8.8"]';
