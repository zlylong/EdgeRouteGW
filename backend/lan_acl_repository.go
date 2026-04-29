package main

type LanACLRepository struct{}

type LanACLRecord struct {
	ID        int
	Type      string
	Value     string
	Policy    string
	Remark    string
	CreatedAt string
}

func NewLanACLRepository() *LanACLRepository { return &LanACLRepository{} }

func (r *LanACLRepository) List() ([]LanACLRecord, error) {
	rows, err := db.Query("SELECT id, type, value, policy, remark, created_at FROM lan_acls ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make([]LanACLRecord, 0)
	for rows.Next() {
		var rec LanACLRecord
		if err := rows.Scan(&rec.ID, &rec.Type, &rec.Value, &rec.Policy, &rec.Remark, &rec.CreatedAt); err != nil {
			continue
		}
		out = append(out, rec)
	}
	return out, nil
}

func (r *LanACLRepository) GetDefaultPolicy() string {
	var defaultPolicy string
	if err := db.QueryRow("SELECT value FROM settings WHERE key='lan_default_policy'").Scan(&defaultPolicy); err != nil {
		return "proxy"
	}
	return defaultPolicy
}

func (r *LanACLRepository) Create(typ, value, policy, remark string) error {
	_, err := db.Exec("INSERT INTO lan_acls (type, value, policy, remark) VALUES (?, ?, ?, ?)", typ, value, policy, remark)
	return err
}

func (r *LanACLRepository) Delete(id string) error {
	_, err := db.Exec("DELETE FROM lan_acls WHERE id=?", id)
	return err
}

func (r *LanACLRepository) SetDefaultPolicy(policy string) error {
	_, err := db.Exec("UPDATE settings SET value=? WHERE key='lan_default_policy'", policy)
	return err
}
