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
	_, err := r.CreateReturningID(value, remark)
	return err
}

func (r *ProtectedIPRepository) CreateReturningID(value, remark string) (int64, error) {
	res, err := getDB().Exec("INSERT INTO protected_ips (value, remark) VALUES (?, ?)", value, remark)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (r *ProtectedIPRepository) Get(id string) (ProtectedIPItem, error) {
	var it ProtectedIPItem
	err := getDB().QueryRow("SELECT id, value, remark, created_at FROM protected_ips WHERE id=?", id).Scan(&it.ID, &it.Value, &it.Remark, &it.CreatedAt)
	return it, err
}

func (r *ProtectedIPRepository) Restore(it ProtectedIPItem) error {
	_, err := getDB().Exec("INSERT INTO protected_ips (id, value, remark, created_at) VALUES (?, ?, ?, ?)", it.ID, it.Value, it.Remark, it.CreatedAt)
	return err
}

func (r *ProtectedIPRepository) Delete(id string) error {
	return execExpectingRow("DELETE FROM protected_ips WHERE id=?", id)
}

func (r *ProtectedIPRepository) DeleteByID(id int64) error {
	_, err := getDB().Exec("DELETE FROM protected_ips WHERE id=?", id)
	return err
}
