package main

import (
	"database/sql"
	"strings"
)

type RulesRepository struct {
	db *sql.DB
}

func NewRulesRepository() *RulesRepository {
	return &RulesRepository{db: db}
}

func (r *RulesRepository) CollectRuleGroups() []map[string]interface{} {
	rows, err := r.db.Query(`SELECT group_id, COALESCE(NULLIF(group_name, ''), group_id) AS display_name, COUNT(*) AS rule_count FROM rules WHERE COALESCE(group_id, '') <> '' GROUP BY group_id, group_name ORDER BY MIN(id) ASC`)
	if err != nil {
		return []map[string]interface{}{}
	}
	defer rows.Close()
	groups := make([]map[string]interface{}, 0)
	for rows.Next() {
		var groupID, displayName string
		var ruleCount int
		if err := rows.Scan(&groupID, &displayName, &ruleCount); err != nil {
			continue
		}
		groups = append(groups, map[string]interface{}{
			"group_id":   groupID,
			"group_name": strings.TrimSpace(displayName),
			"rule_count": ruleCount,
		})
	}
	if groups == nil {
		return []map[string]interface{}{}
	}
	return groups
}

func (r *RulesRepository) CountNodeByID(nodeID int) (int, error) {
	var cnt int
	err := r.db.QueryRow("SELECT COUNT(*) FROM nodes WHERE id=?", nodeID).Scan(&cnt)
	return cnt, err
}

func (r *RulesRepository) CountNodesByIDs(aID, bID int) (int, error) {
	var cnt int
	err := r.db.QueryRow("SELECT COUNT(*) FROM nodes WHERE id IN (?,?)", aID, bID).Scan(&cnt)
	return cnt, err
}

func (r *RulesRepository) ListRules(groupFilter string) ([]map[string]interface{}, error) {
	query := "SELECT id, type, value, policy, COALESCE(group_id, ''), COALESCE(group_name, '') FROM rules"
	args := make([]interface{}, 0, 1)
	if groupFilter != "" {
		query += " WHERE COALESCE(group_id, '') = ?"
		args = append(args, groupFilter)
	}
	query += " ORDER BY id ASC"

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var rules []map[string]interface{}
	for rows.Next() {
		var id int
		var rtype, value, policy, groupID, groupName string
		if err := rows.Scan(&id, &rtype, &value, &policy, &groupID, &groupName); err != nil {
			continue
		}
		rules = append(rules, map[string]interface{}{"id": id, "type": rtype, "value": value, "policy": policy, "group_id": groupID, "group_name": groupName})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if rules == nil {
		rules = make([]map[string]interface{}, 0)
	}
	return rules, nil
}

func (r *RulesRepository) UpdateRuleGroupName(groupID, groupName string) (int64, error) {
	res, err := r.db.Exec("UPDATE rules SET group_name=? WHERE group_id=?", groupName, groupID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *RulesRepository) CountDomainRulesInGroup(groupID string) (int, error) {
	var domainCount int
	err := r.db.QueryRow("SELECT COUNT(*) FROM rules WHERE group_id=? AND type='domain'", groupID).Scan(&domainCount)
	return domainCount, err
}

func (r *RulesRepository) DeleteRulesByGroupID(groupID string) (int64, error) {
	res, err := r.db.Exec("DELETE FROM rules WHERE group_id=?", groupID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (r *RulesRepository) GetRuleTypeByID(ruleID string) (string, error) {
	var ruleType string
	err := r.db.QueryRow("SELECT type FROM rules WHERE id=?", ruleID).Scan(&ruleType)
	return ruleType, err
}

func (r *RulesRepository) DeleteRuleByID(ruleID string) error {
	_, err := r.db.Exec("DELETE FROM rules WHERE id=?", ruleID)
	return err
}
