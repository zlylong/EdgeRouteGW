-- Unmatched traffic goes direct by default. Only gateways without any
-- operator data are switched (fresh installs); imported or configured ones
-- keep their setting.
UPDATE network_config SET lan_default_policy = 'direct'
WHERE (SELECT COUNT(*) FROM nodes) = 0
  AND (SELECT COUNT(*) FROM rules r JOIN rule_sets s ON s.id = r.set_id WHERE s.builtin = 0) = 0;
