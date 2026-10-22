-- Mosdns extras: ECS client subnet for the local upstreams, internal-domain
-- forwarding rules (suffix -> upstream, ahead of everything else) and AAAA
-- filtering (off / all / proxied domains only).
ALTER TABLE dns_config ADD COLUMN ecs_subnet TEXT NOT NULL DEFAULT '';
ALTER TABLE dns_config ADD COLUMN forward_rules TEXT NOT NULL DEFAULT '[]';
ALTER TABLE dns_config ADD COLUMN ipv6_mode TEXT NOT NULL DEFAULT '';
