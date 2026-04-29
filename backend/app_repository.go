package main

// AppRepository encapsulates persistence/bootstrap concerns.
// At this stage it adapts existing DB bootstrap functions without changing behavior.
type AppRepository struct{}

func NewAppRepository() *AppRepository {
	return &AppRepository{}
}

func (r *AppRepository) InitDB() {
	initDB()
}
