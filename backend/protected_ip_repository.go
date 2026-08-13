package main

type ProtectedIPRepository struct{}

type ProtectedIPItem struct {
	ID        int
	Value     string
	Remark    string
	CreatedAt string
}

func NewProtectedIPRepository() *ProtectedIPRepository { return &ProtectedIPRepository{} }

func (r *ProtectedIPRepository) List() ([]ProtectedIPItem, error) {
	rows, err := getDB().Query("SELECT id, value, remark, created_at FROM protected_ips ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProtectedIPItem, 0)
	for rows.Next() {
		var it ProtectedIPItem
		if err := rows.Scan(&it.ID, &it.Value, &it.Remark, &it.CreatedAt); err != nil {
			continue
		}
		items = append(items, it)
	}
	return items, nil
}

func (r *ProtectedIPRepository) Create(value, remark string) error {
	_, err := getDB().Exec("INSERT INTO protected_ips (value, remark) VALUES (?, ?)", value, remark)
	return err
}

func (r *ProtectedIPRepository) Delete(id string) error {
	_, err := getDB().Exec("DELETE FROM protected_ips WHERE id=?", id)
	return err
}
