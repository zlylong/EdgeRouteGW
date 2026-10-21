-- The built-in default rule set: always first, cannot be deleted or moved.
ALTER TABLE rule_sets ADD COLUMN builtin INTEGER NOT NULL DEFAULT 0;
